//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

func TestLocalQuotaWindow_Sources(t *testing.T) {
	now := time.Date(2026, 8, 5, 14, 30, 0, 0, time.Local)

	daily := localQuotaWindow(QuotaWindowDaily, now)
	if daily.Source != QuotaWindowSourceCalendar {
		t.Fatalf("daily source = %q, want calendar", daily.Source)
	}
	if !daily.Start.Equal(timezone.StartOfDay(now)) {
		t.Errorf("daily start = %v, want %v", daily.Start, timezone.StartOfDay(now))
	}

	weekly := localQuotaWindow(QuotaWindowWeekly, now)
	if weekly.Source != QuotaWindowSourceCalendar {
		t.Fatalf("weekly source = %q, want calendar", weekly.Source)
	}
	if !weekly.Start.Equal(timezone.StartOfWeek(now)) {
		t.Errorf("weekly start = %v, want %v", weekly.Start, timezone.StartOfWeek(now))
	}

	for _, w := range []string{QuotaWindowFiveHour, QuotaWindowMonthly} {
		got := localQuotaWindow(w, now)
		if got.Source != QuotaWindowSourceRolling {
			t.Errorf("%s source = %q, want rolling", w, got.Source)
		}
		if got.Start != nil || got.End != nil {
			t.Errorf("%s rolling window must not carry fixed bounds, got start=%v end=%v", w, got.Start, got.End)
		}
	}
}

func TestResolvedQuotaWindow_IsExpired_PerSource(t *testing.T) {
	now := time.Date(2026, 8, 5, 14, 30, 0, 0, time.Local)

	t.Run("account window expires the moment the boundary moves", func(t *testing.T) {
		start := now.Add(-2 * time.Hour)
		end := start.Add(QuotaWindowFiveHourDuration)
		w := ResolvedQuotaWindow{Start: &start, End: &end, Source: QuotaWindowSourceAccount}

		if w.IsExpired(&start, now, QuotaWindowFiveHourDuration) {
			t.Error("same start must not be expired")
		}
		other := start.Add(-QuotaWindowFiveHourDuration)
		if !w.IsExpired(&other, now, QuotaWindowFiveHourDuration) {
			t.Error("previous window start must be expired")
		}
		// 账号窗口即便被校准到「更早」的边界，也应视为换窗（而不是只看 Before）。
		later := start.Add(37 * time.Minute)
		if !w.IsExpired(&later, now, QuotaWindowFiveHourDuration) {
			t.Error("any start != frame.Start must be expired for account source")
		}
	})

	t.Run("calendar window expires only when start is before boundary", func(t *testing.T) {
		start := timezone.StartOfDay(now)
		end := start.Add(QuotaWindowDailyDuration)
		w := ResolvedQuotaWindow{Start: &start, End: &end, Source: QuotaWindowSourceCalendar}

		if w.IsExpired(&start, now, QuotaWindowDailyDuration) {
			t.Error("current day start must not be expired")
		}
		yesterday := start.Add(-QuotaWindowDailyDuration)
		if !w.IsExpired(&yesterday, now, QuotaWindowDailyDuration) {
			t.Error("yesterday start must be expired")
		}
	})

	t.Run("rolling window expires after the full duration", func(t *testing.T) {
		w := ResolvedQuotaWindow{Source: QuotaWindowSourceRolling}

		fresh := now.Add(-4*time.Hour - 59*time.Minute)
		if w.IsExpired(&fresh, now, QuotaWindowFiveHourDuration) {
			t.Error("4h59m old rolling window must still be alive")
		}
		stale := now.Add(-QuotaWindowFiveHourDuration)
		if !w.IsExpired(&stale, now, QuotaWindowFiveHourDuration) {
			t.Error("exactly 5h old rolling window must be expired")
		}
	})

	t.Run("nil start is always expired", func(t *testing.T) {
		for _, src := range []string{QuotaWindowSourceAccount, QuotaWindowSourceCalendar, QuotaWindowSourceRolling} {
			w := ResolvedQuotaWindow{Source: src}
			if !w.IsExpired(nil, now, QuotaWindowFiveHourDuration) {
				t.Errorf("%s: nil start must be expired", src)
			}
		}
	})
}

// 基准账号闲置时 session_window_end 会停在过去。若直接采用该端点，窗口边界
// 永远算不到未来，用户用量永不清零 —— 所有人被永久挡在限额外。
func TestAdvanceStaleWindow_SelfHealsFrozenReferenceWindow(t *testing.T) {
	now := time.Date(2026, 8, 5, 14, 30, 0, 0, time.Local)
	dur := QuotaWindowFiveHourDuration

	t.Run("future end is left untouched", func(t *testing.T) {
		end := now.Add(90 * time.Minute)
		if got := advanceStaleWindow(end, dur, now); !got.Equal(end) {
			t.Errorf("got %v, want unchanged %v", got, end)
		}
	})

	t.Run("stale end steps forward onto the same grid", func(t *testing.T) {
		// 账号闲置 3 天，端点停在 72 小时前
		end := now.Add(-72 * time.Hour)
		got := advanceStaleWindow(end, dur, now)

		if !got.After(now) {
			t.Fatalf("advanced end %v must be in the future (now=%v)", got, now)
		}
		if got.Sub(now) > dur {
			t.Errorf("advanced end %v overshot: more than one window past now", got)
		}
		// 必须落在原网格上，否则刷新时刻会和 Claude 官方错位
		if off := got.Sub(end) % dur; off != 0 {
			t.Errorf("advanced end drifted off the original grid by %v", off)
		}
	})

	t.Run("end exactly at now still advances", func(t *testing.T) {
		got := advanceStaleWindow(now, dur, now)
		if !got.After(now) {
			t.Errorf("end==now must advance, got %v", got)
		}
	})

	t.Run("absurdly stale end falls back instead of looping", func(t *testing.T) {
		end := now.Add(-time.Duration(maxStaleWindowSteps+10) * dur)
		got := advanceStaleWindow(end, dur, now)
		if !got.After(now) {
			t.Errorf("fallback end %v must be in the future", got)
		}
	})
}

func TestQuotaWindowSyncService_ResolveFollowsReferenceAccount(t *testing.T) {
	now := time.Date(2026, 8, 5, 14, 30, 0, 0, time.Local)
	fiveHourEnd := time.Date(2026, 8, 5, 16, 12, 0, 0, time.Local) // 故意不是整点
	weeklyEnd := time.Date(2026, 8, 9, 3, 47, 0, 0, time.Local)

	s := &QuotaWindowSyncService{}
	s.snapshot.Store(&quotaWindowSnapshot{byPlatform: map[string]*referenceWindowEnds{
		"anthropic": {accountID: 7, fiveHourEnd: fiveHourEnd, weeklyEnd: weeklyEnd},
	}})

	got := s.ResolveAllQuotaWindows("anthropic", now)

	five := got[QuotaWindowFiveHour]
	if five.Source != QuotaWindowSourceAccount {
		t.Fatalf("five_hour source = %q, want account", five.Source)
	}
	if !five.End.Equal(fiveHourEnd) {
		t.Errorf("five_hour end = %v, want %v", five.End, fiveHourEnd)
	}
	if want := fiveHourEnd.Add(-QuotaWindowFiveHourDuration); !five.Start.Equal(want) {
		t.Errorf("five_hour start = %v, want %v", five.Start, want)
	}

	weekly := got[QuotaWindowWeekly]
	if weekly.Source != QuotaWindowSourceAccount {
		t.Fatalf("weekly source = %q, want account", weekly.Source)
	}
	if !weekly.End.Equal(weeklyEnd) {
		t.Errorf("weekly end = %v, want %v", weekly.End, weeklyEnd)
	}

	// 日/月永远是本地语义，不受基准账号影响
	if got[QuotaWindowDaily].Source != QuotaWindowSourceCalendar {
		t.Errorf("daily must stay calendar, got %q", got[QuotaWindowDaily].Source)
	}
	if got[QuotaWindowMonthly].Source != QuotaWindowSourceRolling {
		t.Errorf("monthly must stay rolling, got %q", got[QuotaWindowMonthly].Source)
	}
}

// 日/周窗口的 End 必须用 AddDate 推进，而不是 Add(24h)/Add(7*24h)。
// 过期判定走的是 StartOfDay/StartOfWeek，在有夏令时的时区里「下一个本地 0 点」
// 与「+24 小时」相差一小时 —— 两者不一致会让 429 的 Retry-After 早/晚一小时，
// 客户端按提示重试却仍被拒。
func TestLocalQuotaWindow_DailyWeeklyEndSurvivesDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	if err := timezone.Init("America/New_York"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = timezone.Init("UTC") })

	// 2026-11-01 是美东夏令时结束日，当天有 25 小时。
	now := time.Date(2026, 11, 1, 22, 0, 0, 0, loc)

	daily := localQuotaWindow(QuotaWindowDaily, now)
	wantDailyEnd := timezone.StartOfDay(now).AddDate(0, 0, 1)
	if !daily.End.Equal(wantDailyEnd) {
		t.Errorf("daily end = %v, want 次日本地 0 点 %v", daily.End, wantDailyEnd)
	}
	// End 必须正好是「过期判定翻转」的那一刻：在 End 之前不过期，在 End 之后过期。
	justBefore := daily.End.Add(-time.Minute)
	if localQuotaWindow(QuotaWindowDaily, justBefore).IsExpired(daily.Start, justBefore, QuotaWindowDailyDuration) {
		t.Error("End 之前一分钟不应过期 —— resets_at 报早了")
	}
	justAfter := daily.End.Add(time.Minute)
	if !localQuotaWindow(QuotaWindowDaily, justAfter).IsExpired(daily.Start, justAfter, QuotaWindowDailyDuration) {
		t.Error("End 之后一分钟应已过期 —— resets_at 报晚了")
	}

	weekly := localQuotaWindow(QuotaWindowWeekly, now)
	wantWeeklyEnd := timezone.StartOfWeek(now).AddDate(0, 0, 7)
	if !weekly.End.Equal(wantWeeklyEnd) {
		t.Errorf("weekly end = %v, want 下周一本地 0 点 %v", weekly.End, wantWeeklyEnd)
	}
}

// 陈旧窗口的推进是纯计算，热路径每个请求都会调用 —— 不得在其中打日志，
// 否则「基准账号闲置」这种正常状态会让日志每秒刷上百行。
func TestAdvanceStaleWindow_IsPureArithmetic(t *testing.T) {
	now := time.Date(2026, 8, 5, 14, 30, 0, 0, time.Local)
	end := now.Add(-72 * time.Hour)
	// 同一输入必须稳定输出（无副作用、无状态）
	first := advanceStaleWindow(end, QuotaWindowFiveHourDuration, now)
	second := advanceStaleWindow(end, QuotaWindowFiveHourDuration, now)
	if !first.Equal(second) {
		t.Errorf("同一输入两次结果不同: %v vs %v", first, second)
	}
	if !first.After(now) {
		t.Errorf("推进后仍不在未来: %v", first)
	}
}

// account 来源的过期判定要求起点「完全相等」，而已存起点经 Redis 按秒往返。
// 上游端点若带亚秒精度，往返后将永远不等 —— 每次预检都判跨窗清零，限额彻底失效。
// 边界必须在生成处就截断到秒。
func TestQuotaWindowSyncService_TruncatesSubSecondAccountWindow(t *testing.T) {
	now := time.Date(2026, 8, 5, 7, 0, 0, 0, time.Local)
	end := time.Date(2026, 8, 5, 9, 12, 0, 500_000_000, time.Local) // 带 .5s

	s := &QuotaWindowSyncService{}
	s.snapshot.Store(&quotaWindowSnapshot{byPlatform: map[string]*referenceWindowEnds{
		"anthropic": {accountID: 7, fiveHourEnd: end},
	}})

	got := s.ResolveQuotaWindow("anthropic", QuotaWindowFiveHour, now)
	if got.End.Nanosecond() != 0 {
		t.Errorf("End 仍带亚秒精度: %v", got.End)
	}
	if got.Start.Nanosecond() != 0 {
		t.Errorf("Start 仍带亚秒精度: %v", got.Start)
	}
	// 模拟 Redis 往返（按 Unix 秒存取）后再判定：必须仍视为同一轮窗口。
	roundTripped := time.Unix(got.Start.Unix(), 0)
	if got.IsExpired(&roundTripped, now, QuotaWindowFiveHourDuration) {
		t.Error("经 Redis 秒级往返后被误判为跨窗 —— 限额会被反复清零")
	}
}

// 基准账号读取瞬时失败时必须沿用上一轮端点。若把该平台从快照里丢掉，
// 窗口来源会从 account 掉回 calendar/rolling，两者过期判定不同 ——
// 已存起点被判跨窗，全站用户的周/5h 用量瞬间清零；恢复后再清一次。
func TestQuotaWindowSyncService_KeepsPreviousEndsWhenAccountReadFails(t *testing.T) {
	prevFive := time.Date(2026, 8, 5, 9, 12, 0, 0, time.Local)
	prevWeekly := time.Date(2026, 8, 9, 3, 47, 0, 0, time.Local)

	settings := newStubSettingRepo()
	settings.values[SettingKeyQuotaReferenceAccounts] = `{"anthropic":7}`
	s := &QuotaWindowSyncService{
		settingRepo: settings,
		accountRepo: &failingAccountRepo{},
	}
	s.snapshot.Store(&quotaWindowSnapshot{byPlatform: map[string]*referenceWindowEnds{
		"anthropic": {accountID: 7, fiveHourEnd: prevFive, weeklyEnd: prevWeekly},
	}})

	s.Refresh(context.Background())

	now := time.Date(2026, 8, 5, 7, 0, 0, 0, time.Local)
	got := s.ResolveAllQuotaWindows("anthropic", now)
	if got[QuotaWindowFiveHour].Source != QuotaWindowSourceAccount {
		t.Fatalf("账号读取失败后 5h 来源退化为 %q —— 会触发全站误清零", got[QuotaWindowFiveHour].Source)
	}
	if !got[QuotaWindowFiveHour].End.Equal(prevFive) {
		t.Errorf("5h 端点 = %v, want 沿用 %v", got[QuotaWindowFiveHour].End, prevFive)
	}
	if got[QuotaWindowWeekly].Source != QuotaWindowSourceAccount {
		t.Errorf("weekly 来源 = %q, want account", got[QuotaWindowWeekly].Source)
	}
}

// 未配置基准账号（默认）时行为必须和改动前完全一致，否则升级会静默改变
// 线上已有的周限额语义。
func TestQuotaWindowSyncService_FallsBackToLocalSemantics(t *testing.T) {
	now := time.Date(2026, 8, 5, 14, 30, 0, 0, time.Local)

	cases := map[string]*quotaWindowSnapshot{
		"no reference configured": {byPlatform: map[string]*referenceWindowEnds{}},
		"reference without window data": {byPlatform: map[string]*referenceWindowEnds{
			"anthropic": {accountID: 7}, // 两个端点都是零值
		}},
	}
	for name, snap := range cases {
		t.Run(name, func(t *testing.T) {
			s := &QuotaWindowSyncService{}
			s.snapshot.Store(snap)
			got := s.ResolveAllQuotaWindows("anthropic", now)

			if got[QuotaWindowFiveHour].Source != QuotaWindowSourceRolling {
				t.Errorf("five_hour = %q, want rolling fallback", got[QuotaWindowFiveHour].Source)
			}
			if got[QuotaWindowWeekly].Source != QuotaWindowSourceCalendar {
				t.Errorf("weekly = %q, want calendar fallback", got[QuotaWindowWeekly].Source)
			}
			if !got[QuotaWindowWeekly].Start.Equal(timezone.StartOfWeek(now)) {
				t.Errorf("weekly fallback start = %v, want StartOfWeek", got[QuotaWindowWeekly].Start)
			}
		})
	}
}

// nil resolver 必须退回本地语义而不是 panic —— 简易模式和大量单测都不注入 resolver。
func TestResolveQuotaWindowsWith_NilResolver(t *testing.T) {
	now := time.Date(2026, 8, 5, 14, 30, 0, 0, time.Local)
	got := ResolveQuotaWindowsWith(nil, "anthropic", now)
	if len(got) != 4 {
		t.Fatalf("got %d windows, want 4", len(got))
	}
	if got[QuotaWindowWeekly].Source != QuotaWindowSourceCalendar {
		t.Errorf("weekly = %q, want calendar", got[QuotaWindowWeekly].Source)
	}
}

func TestParseQuotaReferenceAccounts(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want map[string]int64
	}{
		{"empty", "", map[string]int64{}},
		{"valid", `{"anthropic":7}`, map[string]int64{"anthropic": 7}},
		{"drops unknown platform", `{"anthropic":7,"bogus":9}`, map[string]int64{"anthropic": 7}},
		{"drops non-positive id", `{"anthropic":0,"openai":-3}`, map[string]int64{}},
		{"malformed json falls open", `{oops`, map[string]int64{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseQuotaReferenceAccounts(tc.raw)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("key %q: got %d, want %d", k, got[k], v)
				}
			}
		})
	}
}

func TestResolvedQuotaWindow_NextReset(t *testing.T) {
	now := time.Date(2026, 8, 5, 14, 30, 0, 0, time.Local)

	t.Run("account window returns the upstream end", func(t *testing.T) {
		start := now.Add(-2 * time.Hour)
		end := start.Add(QuotaWindowFiveHourDuration)
		w := ResolvedQuotaWindow{Start: &start, End: &end, Source: QuotaWindowSourceAccount}
		if got := w.NextReset(&start, now, QuotaWindowFiveHourDuration); !got.Equal(end) {
			t.Errorf("got %v, want %v", got, end)
		}
	})

	t.Run("rolling window counts from the stored start", func(t *testing.T) {
		w := ResolvedQuotaWindow{Source: QuotaWindowSourceRolling}
		start := now.Add(-2 * time.Hour)
		want := start.Add(QuotaWindowFiveHourDuration)
		if got := w.NextReset(&start, now, QuotaWindowFiveHourDuration); !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("expired rolling window counts from now", func(t *testing.T) {
		w := ResolvedQuotaWindow{Source: QuotaWindowSourceRolling}
		start := now.Add(-9 * time.Hour)
		want := now.Add(QuotaWindowFiveHourDuration)
		if got := w.NextReset(&start, now, QuotaWindowFiveHourDuration); !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}


// failingAccountRepo 只让 GetByID 失败（模拟 DB 抖动 / 账号被删），
// 其余方法沿用 accountRepoStub 的 panic 行为以暴露非预期调用。
type failingAccountRepo struct {
	accountRepoStub
}

func (r *failingAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	return nil, errors.New("transient db failure")
}
