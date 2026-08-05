package service

import (
	"context"
	"log/slog"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// ---------------------------------------------------------------------------
// 上游账号用量窗口校准
//
// OAuth 账号的 5h/7d 窗口来自上游 Anthropic API 的真实 resets_at；setup-token 账号
// 拿不到 profile scope，只能靠响应头，拿不到头时退化成「当前整点 +5h」的预测
// （见 ratelimit_service.go 的 UpdateSessionWindow），会漂且无法自愈。
// 本文件提供管理员手动纠偏的入口。
//
// 语义：写入的是**本地跟踪状态**，属于种子值/纠偏。对能查上游的账号，下一次成功
// 探测或带 anthropic-ratelimit-unified-5h-reset 的响应头会用真实值覆盖它 ——
// 这是正确行为，上游才是权威。
// ---------------------------------------------------------------------------

// 可校准的窗口标识。
const (
	CalibratableWindow5h    = "5h"
	CalibratableWindow7d    = "7d"
	CalibratableWindow7dOI  = "7d_oi"
	fiveHourWindowDuration  = 5 * time.Hour
	sevenDayWindowDuration  = 7 * 24 * time.Hour
	fiveHourCalibrationSpan = 6 * time.Hour      // 5h 窗口 resets_at 的合理上界
	sevenDayCalibrationSpan = 8 * 24 * time.Hour // 7d 窗口 resets_at 的合理上界
)

var (
	ErrCalibrateWindowUnsupportedAccount = infraerrors.BadRequest(
		"CALIBRATE_WINDOW_UNSUPPORTED_ACCOUNT",
		"usage window calibration is only supported for Anthropic OAuth / setup-token accounts",
	)
	ErrCalibrateWindowInvalidWindow = infraerrors.BadRequest(
		"CALIBRATE_WINDOW_INVALID_WINDOW",
		"window must be one of 5h, 7d, 7d_oi",
	)
	ErrCalibrateWindowInvalidResetsAt = infraerrors.BadRequest(
		"CALIBRATE_WINDOW_INVALID_RESETS_AT",
		"resets_at must be in the future and within the window's plausible range",
	)
)

// calibrationSpan 返回该窗口 resets_at 允许的最大提前量。
// 与 ratelimit_service.parseAnthropicResetTimestamp 的边界保持一致：
// 上游真实头都落在这个区间内，超出即为误输入（例如把日期填错一年）。
func calibrationSpan(window string) (time.Duration, bool) {
	switch window {
	case CalibratableWindow5h:
		return fiveHourCalibrationSpan, true
	case CalibratableWindow7d, CalibratableWindow7dOI:
		return sevenDayCalibrationSpan, true
	default:
		return 0, false
	}
}

// CalibrateUsageWindow 手动校正账号某个用量窗口的刷新时刻。
//
// resetUsage=true 时同时清零该窗口的 utilization 采样 —— 窗口起点被挪动后，
// 上一轮采样的百分比对新窗口没有意义，留着会让 UI 显示矛盾状态。
func (s *AccountUsageService) CalibrateUsageWindow(
	ctx context.Context,
	accountID int64,
	window string,
	resetsAt time.Time,
	resetUsage bool,
) error {
	span, ok := calibrationSpan(window)
	if !ok {
		return ErrCalibrateWindowInvalidWindow
	}

	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return err
	}
	if account == nil || !account.IsAnthropicOAuthOrSetupToken() {
		return ErrCalibrateWindowUnsupportedAccount
	}

	now := time.Now()
	if !resetsAt.After(now) || resetsAt.After(now.Add(span)) {
		return ErrCalibrateWindowInvalidResetsAt
	}

	switch window {
	case CalibratableWindow5h:
		// 5h 窗口的权威存储是 session_window_start/end 两列（estimateSetupTokenUsage
		// 与 quota_window_resolver 都读 end），故走 UpdateSessionWindow 而非 Extra。
		start := resetsAt.Add(-fiveHourWindowDuration)
		status := account.SessionWindowStatus
		if resetUsage || status == "" {
			// 清零用量等同于「开一个全新的窗口」，状态必须回到 allowed，
			// 否则残留的 rejected 会让 UI 继续显示 100% 且调度侧误判仍在限流。
			status = "allowed"
		}
		if err := s.accountRepo.UpdateSessionWindow(ctx, accountID, &start, &resetsAt, status); err != nil {
			return err
		}
		if resetUsage {
			if err := s.accountRepo.UpdateExtra(ctx, accountID, map[string]any{
				"session_window_utilization": nil,
			}); err != nil {
				slog.Warn("calibrate_window_reset_utilization_failed", "account_id", accountID, "window", window, "error", err)
			}
		}

	case CalibratableWindow7d, CalibratableWindow7dOI:
		// 7d / 7d_oi 只存在于 Extra 的被动采样字段里（samplePassiveUsageFromHeaders 写、
		// buildPassiveUsageWindow 读），键名必须与那两处完全一致。
		resetKey, utilKey := "passive_usage_7d_reset", "passive_usage_7d_utilization"
		if window == CalibratableWindow7dOI {
			resetKey, utilKey = "passive_usage_7d_oi_reset", "passive_usage_7d_oi_utilization"
		}
		updates := map[string]any{
			resetKey:                   resetsAt.Unix(),
			"passive_usage_sampled_at": now.UTC().Format(time.RFC3339),
		}
		if resetUsage {
			updates[utilKey] = nil
		}
		if err := s.accountRepo.UpdateExtra(ctx, accountID, updates); err != nil {
			return err
		}
	}

	// 清进程内用量缓存，让管理端刷新后立刻看到新值，而不是等 3 分钟 TTL。
	if s.cache != nil {
		s.cache.apiCache.Delete(accountID)
		s.cache.windowStatsCache.Delete(accountID)
	}

	// 若该账号是某平台的用量基准账号，用户的 5h/周 限额窗口会随之平移，
	// 必须立刻刷新解析器快照，否则最长 15s 内 enforcement 仍按旧边界判定。
	if s.quotaWindowSync != nil {
		s.quotaWindowSync.Refresh(ctx)
	}

	slog.Info("account_usage_window_calibrated",
		"account_id", accountID,
		"window", window,
		"resets_at", resetsAt,
		"reset_usage", resetUsage)
	return nil
}

// SetQuotaWindowSync 注入窗口同步服务（可选依赖）：校准基准账号后立即刷新快照。
func (s *AccountUsageService) SetQuotaWindowSync(sync *QuotaWindowSyncService) {
	if s == nil {
		return
	}
	s.quotaWindowSync = sync
}
