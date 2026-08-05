// Package quotaview provides shared quota response helpers for user and admin handlers.
// Extracted to avoid import cycles between handler and handler/admin packages.
package quotaview

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// LazyZeroQuotaForResponse 按 D14 规则把过期档位归零（不写 DB）。
// includeWindowStart=true 时输出 *_window_start 字段（admin 视角调试用）。
//
// windows 是已解析的四档窗口边界（见 service.QuotaWindowResolver）：5h 与周窗口
// 可能跟随基准账号的上游窗口，展示层必须和 enforcement 用同一份边界，
// 否则用户会看到「显示还有额度但请求被拒」这类自相矛盾的状态。
func LazyZeroQuotaForResponse(
	r service.UserPlatformQuotaRecord,
	windows map[string]service.ResolvedQuotaWindow,
	now time.Time,
	includeWindowStart bool,
) map[string]any {
	slice := func(window string, usage float64, limit *float64, start *time.Time) windowSlice {
		win := windows[window]
		dur := service.QuotaWindowDuration(window)
		return buildWindowSlice(usage, limit, start,
			win.IsExpired(start, now, dur),
			win.NextReset(start, now, dur),
			// 跟随基准账号的窗口边界由上游给定，与用户本轮是否消费无关，
			// 所以即便该用户的记录还停在上一轮（或压根没消费过）也应报出刷新时刻 ——
			// 否则前端无法显示倒计时，用户看到的是「有限额但不知道何时刷新」。
			// calendar / rolling 保持 D14 原语义不变。
			win.Source == service.QuotaWindowSourceAccount,
			includeWindowStart)
	}

	fiveHour := slice(service.QuotaWindowFiveHour, r.FiveHourUsageUSD, r.FiveHourLimitUSD, r.FiveHourWindowStart)
	daily := slice(service.QuotaWindowDaily, r.DailyUsageUSD, r.DailyLimitUSD, r.DailyWindowStart)
	weekly := slice(service.QuotaWindowWeekly, r.WeeklyUsageUSD, r.WeeklyLimitUSD, r.WeeklyWindowStart)
	monthly := slice(service.QuotaWindowMonthly, r.MonthlyUsageUSD, r.MonthlyLimitUSD, r.MonthlyWindowStart)

	out := map[string]any{
		"platform":                   r.Platform,
		"five_hour_usage_usd":        fiveHour.usage,
		"five_hour_limit_usd":        fiveHour.limit,
		"five_hour_window_resets_at": fiveHour.resetsAt,
		"daily_usage_usd":            daily.usage,
		"daily_limit_usd":            daily.limit,
		"daily_window_resets_at":     daily.resetsAt,
		"weekly_usage_usd":           weekly.usage,
		"weekly_limit_usd":           weekly.limit,
		"weekly_window_resets_at":    weekly.resetsAt,
		"monthly_usage_usd":          monthly.usage,
		"monthly_limit_usd":          monthly.limit,
		"monthly_window_resets_at":   monthly.resetsAt,
		// 窗口来源让前端能标注「同步」并解释为什么刷新时刻不是整点。
		"five_hour_window_source": windows[service.QuotaWindowFiveHour].Source,
		"weekly_window_source":    windows[service.QuotaWindowWeekly].Source,
	}
	if includeWindowStart {
		out["five_hour_window_start"] = fiveHour.windowStart
		out["daily_window_start"] = daily.windowStart
		out["weekly_window_start"] = weekly.windowStart
		out["monthly_window_start"] = monthly.windowStart
	}
	return out
}

type windowSlice struct {
	usage       float64
	limit       *float64
	resetsAt    *string
	windowStart *string
}

// alwaysReportReset=true 时，即使窗口已跨轮也报出 resets_at（用量仍归零）。
// 仅用于边界由上游账号给定的窗口 —— 那种窗口任何时刻都有确定的刷新时刻。
func buildWindowSlice(usage float64, limit *float64, start *time.Time, expired bool, nextReset time.Time, alwaysReportReset bool, includeStart bool) windowSlice {
	out := windowSlice{usage: usage, limit: limit}
	if expired {
		out.usage = 0
		out.resetsAt = nil
	}
	if (!expired && start != nil) || alwaysReportReset {
		s := nextReset.Format(time.RFC3339)
		out.resetsAt = &s
	}
	if includeStart && start != nil {
		s := start.Format(time.RFC3339)
		out.windowStart = &s
	}
	return out
}
