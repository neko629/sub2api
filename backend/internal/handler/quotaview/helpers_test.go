package quotaview

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// resetsAtOf 取出响应里某档的 *_window_resets_at，未设置时返回 ""。
func resetsAtOf(out map[string]any, key string) string {
	v, ok := out[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(*string)
	if !ok || s == nil {
		return ""
	}
	return *s
}

// 月窗口是 30 天滚动：resetsAt 必须锚在 windowStart+30d，不随 now 漂移。
func TestLazyZeroQuotaForResponse_MonthlyResetsAt_NotDrifting(t *testing.T) {
	windowStart := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	want := windowStart.Add(30 * 24 * time.Hour).Format(time.RFC3339)

	r := service.UserPlatformQuotaRecord{
		Platform:           "openai",
		MonthlyUsageUSD:    5.0,
		MonthlyWindowStart: &windowStart,
	}

	for _, offset := range []time.Duration{5 * 24 * time.Hour, 10 * 24 * time.Hour} {
		now := windowStart.Add(offset)
		out := LazyZeroQuotaForResponse(r, service.LocalQuotaWindows(now), now, false)
		if got := resetsAtOf(out, "monthly_window_resets_at"); got != want {
			t.Errorf("now=windowStart+%v: monthly_window_resets_at = %q, want %q", offset, got, want)
		}
	}
}

// 日窗口按全局服务器时区切分，而不是 UTC。
func TestLazyZeroQuotaForResponse_DailyFollowsServerTimezone(t *testing.T) {
	if err := timezone.Init("Asia/Shanghai"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = timezone.Init("UTC") })

	// now = 2026-05-25 23:00 UTC = 2026-05-26 07:00 +08（北京 5/26）
	now := time.Date(2026, 5, 25, 23, 0, 0, 0, time.UTC)
	windows := service.LocalQuotaWindows(now)

	t.Run("上一个北京日的窗口已过期，用量归零且不给 resets_at", func(t *testing.T) {
		// 2026-05-25 10:00 UTC = 北京 5/25 18:00
		start := time.Date(2026, 5, 25, 10, 0, 0, 0, time.UTC)
		out := LazyZeroQuotaForResponse(service.UserPlatformQuotaRecord{
			Platform: "openai", DailyUsageUSD: 3.5, DailyWindowStart: &start,
		}, windows, now, false)

		if got := out["daily_usage_usd"]; got != 0.0 {
			t.Errorf("daily_usage_usd = %v, want 0 (窗口已过期)", got)
		}
		if got := resetsAtOf(out, "daily_window_resets_at"); got != "" {
			t.Errorf("过期窗口不应给 resets_at, got %q", got)
		}
	})

	t.Run("同一北京日的窗口存活，resets_at = 次日北京 0 点", func(t *testing.T) {
		// 2026-05-25 20:00 UTC = 北京 5/26 04:00
		start := time.Date(2026, 5, 25, 20, 0, 0, 0, time.UTC)
		out := LazyZeroQuotaForResponse(service.UserPlatformQuotaRecord{
			Platform: "openai", DailyUsageUSD: 3.5, DailyWindowStart: &start,
		}, windows, now, false)

		if got := out["daily_usage_usd"]; got != 3.5 {
			t.Errorf("daily_usage_usd = %v, want 3.5 (窗口未过期)", got)
		}
		want := time.Date(2026, 5, 27, 0, 0, 0, 0, timezone.Location()).Format(time.RFC3339)
		if got := resetsAtOf(out, "daily_window_resets_at"); got != want {
			t.Errorf("daily_window_resets_at = %q, want %q", got, want)
		}
	})
}

// 未配置基准账号时，周窗口仍是自然周（下周一服务器时区 0 点）。
func TestLazyZeroQuotaForResponse_WeeklyFallsBackToCalendarWeek(t *testing.T) {
	if err := timezone.Init("Asia/Shanghai"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = timezone.Init("UTC") })

	now := time.Date(2026, 5, 25, 23, 0, 0, 0, time.UTC) // 北京 5/26 周二
	windows := service.LocalQuotaWindows(now)
	start := timezone.StartOfWeek(now)

	out := LazyZeroQuotaForResponse(service.UserPlatformQuotaRecord{
		Platform: "openai", WeeklyUsageUSD: 1.0, WeeklyWindowStart: &start,
	}, windows, now, false)

	want := time.Date(2026, 6, 1, 0, 0, 0, 0, timezone.Location()).Format(time.RFC3339)
	if got := resetsAtOf(out, "weekly_window_resets_at"); got != want {
		t.Errorf("weekly_window_resets_at = %q, want %q", got, want)
	}
	if got := out["weekly_window_source"]; got != service.QuotaWindowSourceCalendar {
		t.Errorf("weekly_window_source = %v, want calendar", got)
	}
}

// 配置了基准账号时，5h/周 的 resets_at 直接取上游窗口端点，
// 且 window_source 标记为 account —— 前端据此显示「同步」标记。
func TestLazyZeroQuotaForResponse_FollowsAccountWindow(t *testing.T) {
	now := time.Date(2026, 5, 26, 7, 0, 0, 0, time.UTC)
	upstreamEnd := time.Date(2026, 5, 26, 9, 12, 0, 0, time.UTC) // 故意不是整点
	start := upstreamEnd.Add(-service.QuotaWindowFiveHourDuration)

	windows := service.LocalQuotaWindows(now)
	windows[service.QuotaWindowFiveHour] = service.ResolvedQuotaWindow{
		Start: &start, End: &upstreamEnd, Source: service.QuotaWindowSourceAccount,
	}

	out := LazyZeroQuotaForResponse(service.UserPlatformQuotaRecord{
		Platform: "anthropic", FiveHourUsageUSD: 2.25, FiveHourWindowStart: &start,
	}, windows, now, false)

	if got := out["five_hour_usage_usd"]; got != 2.25 {
		t.Errorf("five_hour_usage_usd = %v, want 2.25", got)
	}
	if got := resetsAtOf(out, "five_hour_window_resets_at"); got != upstreamEnd.Format(time.RFC3339) {
		t.Errorf("five_hour_window_resets_at = %q, want %q", got, upstreamEnd.Format(time.RFC3339))
	}
	if got := out["five_hour_window_source"]; got != service.QuotaWindowSourceAccount {
		t.Errorf("five_hour_window_source = %v, want account", got)
	}
}

// 跟随基准账号的窗口，其边界由上游给定，与该用户本轮是否消费无关。
// 即便用户从未消费（start=nil）或记录还停在上一轮，也必须报出 resets_at，
// 否则前端拿不到倒计时，用户看到「有限额却不知何时刷新」。
// 这是本地 E2E 抓到的缺陷，单测未覆盖 —— 回归锁定。
func TestLazyZeroQuotaForResponse_AccountWindowAlwaysReportsResetsAt(t *testing.T) {
	now := time.Date(2026, 5, 26, 7, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 26, 9, 12, 0, 0, time.UTC)
	start := end.Add(-service.QuotaWindowFiveHourDuration)
	limit := 5.0

	windows := service.LocalQuotaWindows(now)
	windows[service.QuotaWindowFiveHour] = service.ResolvedQuotaWindow{
		Start: &start, End: &end, Source: service.QuotaWindowSourceAccount,
	}

	prev := start.Add(-service.QuotaWindowFiveHourDuration)
	cases := map[string]*time.Time{
		"用户从未消费（start=nil）": nil,
		"用户记录停在上一轮窗口":       &prev,
	}
	for name, storedStart := range cases {
		t.Run(name, func(t *testing.T) {
			out := LazyZeroQuotaForResponse(service.UserPlatformQuotaRecord{
				Platform:            "anthropic",
				FiveHourLimitUSD:    &limit,
				FiveHourUsageUSD:    3.0,
				FiveHourWindowStart: storedStart,
			}, windows, now, false)

			if got := out["five_hour_usage_usd"]; got != 0.0 {
				t.Errorf("跨轮用量应归零, got %v", got)
			}
			want := end.Format(time.RFC3339)
			if got := resetsAtOf(out, "five_hour_window_resets_at"); got != want {
				t.Errorf("five_hour_window_resets_at = %q, want %q", got, want)
			}
		})
	}
}

// 反向保证：calendar / rolling 的展示语义（D14 过期不给 resets_at）不受上面的改动影响。
func TestLazyZeroQuotaForResponse_NonAccountWindowKeepsD14Semantics(t *testing.T) {
	now := time.Date(2026, 5, 26, 7, 0, 0, 0, time.UTC)
	windows := service.LocalQuotaWindows(now)
	yesterday := timezone.StartOfDay(now).AddDate(0, 0, -1)

	out := LazyZeroQuotaForResponse(service.UserPlatformQuotaRecord{
		Platform: "openai", DailyUsageUSD: 2.0, DailyWindowStart: &yesterday,
	}, windows, now, false)

	if got := resetsAtOf(out, "daily_window_resets_at"); got != "" {
		t.Errorf("calendar 窗口过期后仍不应给 resets_at, got %q", got)
	}
}

// 基准账号窗口滚动后，上一轮的起点必须被判定为跨窗并归零 ——
// 这是「账号窗口一滚，所有用户同刻清零」的核心行为。
func TestLazyZeroQuotaForResponse_ZeroesUsageWhenAccountWindowRolled(t *testing.T) {
	now := time.Date(2026, 5, 26, 7, 0, 0, 0, time.UTC)
	newEnd := time.Date(2026, 5, 26, 9, 12, 0, 0, time.UTC)
	newStart := newEnd.Add(-service.QuotaWindowFiveHourDuration)
	prevStart := newStart.Add(-service.QuotaWindowFiveHourDuration) // 上一轮窗口

	windows := service.LocalQuotaWindows(now)
	windows[service.QuotaWindowFiveHour] = service.ResolvedQuotaWindow{
		Start: &newStart, End: &newEnd, Source: service.QuotaWindowSourceAccount,
	}

	out := LazyZeroQuotaForResponse(service.UserPlatformQuotaRecord{
		Platform: "anthropic", FiveHourUsageUSD: 9.99, FiveHourWindowStart: &prevStart,
	}, windows, now, false)

	if got := out["five_hour_usage_usd"]; got != 0.0 {
		t.Errorf("five_hour_usage_usd = %v, want 0 (账号窗口已滚动)", got)
	}
}
