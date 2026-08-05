//go:build unit

package repository

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// applyWindowUsage 取代了原先的 maybeReset / monthlyMaybeReset：
// 过期判定按窗口来源分派，三种来源必须各自正确。

// 日历窗口（日 / 未配基准账号时的周）：起点早于本轮边界才算过期。
func TestApplyWindowUsage_CalendarSource(t *testing.T) {
	now := time.Date(2026, 5, 22, 9, 0, 0, 0, time.UTC)
	curr := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	prev := curr.AddDate(0, 0, -1)
	end := curr.Add(service.QuotaWindowDailyDuration)
	win := service.ResolvedQuotaWindow{Start: &curr, End: &end, Source: service.QuotaWindowSourceCalendar}

	cases := []struct {
		name      string
		prevStart *time.Time
		wantUsage float64
		wantStart time.Time
	}{
		{"nil 起点重置", nil, 1.5, curr},
		{"上一日的起点重置", &prev, 1.5, curr},
		{"同日起点累加", &curr, 11.5, curr},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			usage, start := applyWindowUsage(10, c.prevStart, 1.5, now, win, service.QuotaWindowDailyDuration)
			if usage != c.wantUsage {
				t.Errorf("usage = %v, want %v", usage, c.wantUsage)
			}
			if !start.Equal(c.wantStart) {
				t.Errorf("start = %v, want %v", start, c.wantStart)
			}
		})
	}
}

// 滚动窗口（月 / 未配基准账号时的 5h）：满时长才重置，起点锚在首次消费时刻。
func TestApplyWindowUsage_RollingSource(t *testing.T) {
	win := service.ResolvedQuotaWindow{Source: service.QuotaWindowSourceRolling}
	dur := service.QuotaWindowMonthlyDuration

	t.Run("nil 起点重置并锚定 now", func(t *testing.T) {
		now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
		usage, start := applyWindowUsage(10.0, nil, 1.5, now, win, dur)
		if usage != 1.5 || !start.Equal(now) {
			t.Errorf("got (%v, %v), want (1.5, %v)", usage, start, now)
		}
	})

	t.Run("满 30 天重置", func(t *testing.T) {
		windowStart := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
		now := windowStart.Add(dur)
		usage, start := applyWindowUsage(8.0, &windowStart, 2.0, now, win, dur)
		if usage != 2.0 || !start.Equal(now) {
			t.Errorf("got (%v, %v), want (2.0, %v)", usage, start, now)
		}
	})

	// 30 天滚动而非自然月：跨月但不足 30 天必须继续累加。
	t.Run("跨自然月但不足 30 天累加", func(t *testing.T) {
		windowStart := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
		now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
		usage, start := applyWindowUsage(5.0, &windowStart, 1.0, now, win, dur)
		if usage != 6.0 || !start.Equal(windowStart) {
			t.Errorf("got (%v, %v), want (6.0, %v)", usage, start, windowStart)
		}
	})
}

// 账号窗口（跟随基准账号的 5h / 周）：起点只要与本轮边界不等就算换窗。
// 这是「基准账号窗口一滚动，所有用户同刻清零」的落库侧保证。
func TestApplyWindowUsage_AccountSource(t *testing.T) {
	now := time.Date(2026, 5, 22, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 22, 11, 12, 0, 0, time.UTC) // 上游给的非整点边界
	start := end.Add(-service.QuotaWindowFiveHourDuration)
	win := service.ResolvedQuotaWindow{Start: &start, End: &end, Source: service.QuotaWindowSourceAccount}
	dur := service.QuotaWindowFiveHourDuration

	t.Run("同一轮窗口累加", func(t *testing.T) {
		usage, got := applyWindowUsage(4.0, &start, 1.0, now, win, dur)
		if usage != 5.0 || !got.Equal(start) {
			t.Errorf("got (%v, %v), want (5.0, %v)", usage, got, start)
		}
	})

	t.Run("上一轮窗口的起点重置", func(t *testing.T) {
		prev := start.Add(-dur)
		usage, got := applyWindowUsage(4.0, &prev, 1.0, now, win, dur)
		if usage != 1.0 || !got.Equal(start) {
			t.Errorf("got (%v, %v), want (1.0, %v)", usage, got, start)
		}
	})

	// 管理员把上游窗口往回校准时，起点比本轮更晚也必须算换窗。
	t.Run("任意不等的起点都重置", func(t *testing.T) {
		skewed := start.Add(37 * time.Minute)
		usage, got := applyWindowUsage(4.0, &skewed, 1.0, now, win, dur)
		if usage != 1.0 || !got.Equal(start) {
			t.Errorf("got (%v, %v), want (1.0, %v)", usage, got, start)
		}
	})
}

// TestUpdateLimitsRowQuery_HasDeletedAtGuard 通过读取源文件验证 updateLimitsRow
// 的 SQL WHERE 子句包含 deleted_at IS NULL 守卫（I-NEW-1）。
// 此防回归测试可在无 DB 的 CI 环境中运行，防止意外删除该守卫。
func TestUpdateLimitsRowQuery_HasDeletedAtGuard(t *testing.T) {
	src, err := os.ReadFile("user_platform_quota_repo.go")
	if err != nil {
		t.Fatalf("failed to read source file: %v", err)
	}
	const guard = "AND deleted_at IS NULL"
	if !strings.Contains(string(src), guard) {
		t.Errorf("updateLimitsRow SQL must contain %q to prevent bulk reactivation of soft-deleted rows (I-NEW-1)", guard)
	}
}
