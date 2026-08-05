package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// userPlatformQuotaServiceAdapter 将 repository 层的 userPlatformQuotaRepository
// 适配为 service.UserPlatformQuotaRepository 接口（返回 *service.UserPlatformQuotaRecord）。
type userPlatformQuotaServiceAdapter struct {
	inner *userPlatformQuotaRepository
}

// NewUserPlatformQuotaServiceAdapter 将 UserPlatformQuotaRepository 实现包装为
// 满足 service.UserPlatformQuotaRepository 接口的适配器。
func NewUserPlatformQuotaServiceAdapter(repo UserPlatformQuotaRepository) service.UserPlatformQuotaRepository {
	impl, ok := repo.(*userPlatformQuotaRepository)
	if !ok {
		// 非标准实现（如测试 fake），通过通用适配器包装
		return &genericUserPlatformQuotaAdapter{inner: repo}
	}
	return &userPlatformQuotaServiceAdapter{inner: impl}
}

func (a *userPlatformQuotaServiceAdapter) GetByUserPlatform(ctx context.Context, userID int64, platform string) (*service.UserPlatformQuotaRecord, error) {
	rec, err := a.inner.GetByUserPlatform(ctx, userID, platform)
	if err != nil || rec == nil {
		return nil, err
	}
	return toServiceRecord(rec), nil
}

// IncrementUsageWithReset 原子累加 cost 到 (user, platform) 四个窗口的用量。
func (a *userPlatformQuotaServiceAdapter) IncrementUsageWithReset(ctx context.Context, userID int64, platform string, cost float64, now time.Time, windows map[string]service.ResolvedQuotaWindow) error {
	return a.inner.IncrementUsageWithReset(ctx, userID, platform, cost, now, windows)
}

// ListByUser 查询用户的所有平台配额记录。
func (a *userPlatformQuotaServiceAdapter) ListByUser(ctx context.Context, userID int64) ([]service.UserPlatformQuotaRecord, error) {
	return toServiceRecords(a.inner.ListByUser(ctx, userID))
}

// BulkInsertInitial 将 service.UserPlatformQuotaRecord 切片转换后调用底层 repo。
func (a *userPlatformQuotaServiceAdapter) BulkInsertInitial(ctx context.Context, records []service.UserPlatformQuotaRecord) error {
	return a.inner.BulkInsertInitial(ctx, toRepoLimitRecords(records))
}

// UpsertForUser 全量替换该用户所有平台限额。
func (a *userPlatformQuotaServiceAdapter) UpsertForUser(ctx context.Context, userID int64, records []service.UserPlatformQuotaRecord) error {
	return a.inner.UpsertForUser(ctx, userID, toRepoRecords(records))
}

// ResetExpiredWindow 转发至 repository.ResetExpiredWindow，并将 repository sentinel 包装为 service sentinel。
func (a *userPlatformQuotaServiceAdapter) ResetExpiredWindow(ctx context.Context, userID int64, platform string, window string, newStart time.Time) error {
	return wrapQuotaNotFound(a.inner.ResetExpiredWindow(ctx, userID, platform, window, newStart))
}

// BatchSnapshotUsage 转换 snapshot 切片，调底层 repo，并包装 FK sentinel。
func (a *userPlatformQuotaServiceAdapter) BatchSnapshotUsage(ctx context.Context, snapshots []service.UserPlatformQuotaSnapshot, now time.Time) error {
	return wrapQuotaFKViolation(a.inner.BatchSnapshotUsage(ctx, toRepoSnapshots(snapshots), now))
}

// genericUserPlatformQuotaAdapter 通过通用接口适配（用于测试 fake 或非标准实现）。
type genericUserPlatformQuotaAdapter struct {
	inner UserPlatformQuotaRepository
}

func (a *genericUserPlatformQuotaAdapter) GetByUserPlatform(ctx context.Context, userID int64, platform string) (*service.UserPlatformQuotaRecord, error) {
	rec, err := a.inner.GetByUserPlatform(ctx, userID, platform)
	if err != nil || rec == nil {
		return nil, err
	}
	return toServiceRecord(rec), nil
}

func (a *genericUserPlatformQuotaAdapter) IncrementUsageWithReset(ctx context.Context, userID int64, platform string, cost float64, now time.Time, windows map[string]service.ResolvedQuotaWindow) error {
	return a.inner.IncrementUsageWithReset(ctx, userID, platform, cost, now, windows)
}

func (a *genericUserPlatformQuotaAdapter) ListByUser(ctx context.Context, userID int64) ([]service.UserPlatformQuotaRecord, error) {
	return toServiceRecords(a.inner.ListByUser(ctx, userID))
}

func (a *genericUserPlatformQuotaAdapter) BulkInsertInitial(ctx context.Context, records []service.UserPlatformQuotaRecord) error {
	return a.inner.BulkInsertInitial(ctx, toRepoLimitRecords(records))
}

func (a *genericUserPlatformQuotaAdapter) UpsertForUser(ctx context.Context, userID int64, records []service.UserPlatformQuotaRecord) error {
	return a.inner.UpsertForUser(ctx, userID, toRepoRecords(records))
}

func (a *genericUserPlatformQuotaAdapter) ResetExpiredWindow(ctx context.Context, userID int64, platform string, window string, newStart time.Time) error {
	return wrapQuotaNotFound(a.inner.ResetExpiredWindow(ctx, userID, platform, window, newStart))
}

func (a *genericUserPlatformQuotaAdapter) BatchSnapshotUsage(ctx context.Context, snapshots []service.UserPlatformQuotaSnapshot, now time.Time) error {
	return wrapQuotaFKViolation(a.inner.BatchSnapshotUsage(ctx, toRepoSnapshots(snapshots), now))
}

// ---------------------------------------------------------------------------
// 转换 helper
//
// 两个 adapter × 多个方法原本各自手写同一份字段列表，新增一档窗口就要改六处，
// 漏掉任意一处都会静默丢字段（限额配好了却不生效）。这里收敛成单一定义。
// ---------------------------------------------------------------------------

// toServiceRecord 将 repository record 转换为 service record。
func toServiceRecord(rec *UserPlatformQuotaRecord) *service.UserPlatformQuotaRecord {
	if rec == nil {
		return nil
	}
	out := toServiceRecordValue(*rec)
	return &out
}

func toServiceRecordValue(r UserPlatformQuotaRecord) service.UserPlatformQuotaRecord {
	return service.UserPlatformQuotaRecord{
		UserID:              r.UserID,
		Platform:            r.Platform,
		FiveHourLimitUSD:    r.FiveHourLimitUSD,
		DailyLimitUSD:       r.DailyLimitUSD,
		WeeklyLimitUSD:      r.WeeklyLimitUSD,
		MonthlyLimitUSD:     r.MonthlyLimitUSD,
		FiveHourUsageUSD:    r.FiveHourUsageUSD,
		DailyUsageUSD:       r.DailyUsageUSD,
		WeeklyUsageUSD:      r.WeeklyUsageUSD,
		MonthlyUsageUSD:     r.MonthlyUsageUSD,
		FiveHourWindowStart: r.FiveHourWindowStart,
		DailyWindowStart:    r.DailyWindowStart,
		WeeklyWindowStart:   r.WeeklyWindowStart,
		MonthlyWindowStart:  r.MonthlyWindowStart,
	}
}

func toServiceRecords(rows []UserPlatformQuotaRecord, err error) ([]service.UserPlatformQuotaRecord, error) {
	if err != nil {
		return nil, err
	}
	out := make([]service.UserPlatformQuotaRecord, len(rows))
	for i, r := range rows {
		out[i] = toServiceRecordValue(r)
	}
	return out, nil
}

// toRepoRecords 转换为 repository record（含 limit / usage / window_start 全字段）。
func toRepoRecords(records []service.UserPlatformQuotaRecord) []UserPlatformQuotaRecord {
	out := make([]UserPlatformQuotaRecord, len(records))
	for i, r := range records {
		out[i] = UserPlatformQuotaRecord{
			UserID:              r.UserID,
			Platform:            r.Platform,
			FiveHourLimitUSD:    r.FiveHourLimitUSD,
			DailyLimitUSD:       r.DailyLimitUSD,
			WeeklyLimitUSD:      r.WeeklyLimitUSD,
			MonthlyLimitUSD:     r.MonthlyLimitUSD,
			FiveHourUsageUSD:    r.FiveHourUsageUSD,
			DailyUsageUSD:       r.DailyUsageUSD,
			WeeklyUsageUSD:      r.WeeklyUsageUSD,
			MonthlyUsageUSD:     r.MonthlyUsageUSD,
			FiveHourWindowStart: r.FiveHourWindowStart,
			DailyWindowStart:    r.DailyWindowStart,
			WeeklyWindowStart:   r.WeeklyWindowStart,
			MonthlyWindowStart:  r.MonthlyWindowStart,
		}
	}
	return out
}

// toRepoLimitRecords 只携带 limit 字段，供 BulkInsertInitial 使用
// （usage 由 DB 默认 0、window_start 留 NULL，绝不能被调用方的零值覆盖）。
func toRepoLimitRecords(records []service.UserPlatformQuotaRecord) []UserPlatformQuotaRecord {
	out := make([]UserPlatformQuotaRecord, len(records))
	for i, r := range records {
		out[i] = UserPlatformQuotaRecord{
			UserID:           r.UserID,
			Platform:         r.Platform,
			FiveHourLimitUSD: r.FiveHourLimitUSD,
			DailyLimitUSD:    r.DailyLimitUSD,
			WeeklyLimitUSD:   r.WeeklyLimitUSD,
			MonthlyLimitUSD:  r.MonthlyLimitUSD,
		}
	}
	return out
}

func toRepoSnapshots(snapshots []service.UserPlatformQuotaSnapshot) []UserPlatformQuotaSnapshot {
	out := make([]UserPlatformQuotaSnapshot, len(snapshots))
	for i, s := range snapshots {
		out[i] = UserPlatformQuotaSnapshot{
			UserID:              s.UserID,
			Platform:            s.Platform,
			FiveHourUsageUSD:    s.FiveHourUsageUSD,
			DailyUsageUSD:       s.DailyUsageUSD,
			WeeklyUsageUSD:      s.WeeklyUsageUSD,
			MonthlyUsageUSD:     s.MonthlyUsageUSD,
			FiveHourWindowStart: s.FiveHourWindowStart,
			DailyWindowStart:    s.DailyWindowStart,
			WeeklyWindowStart:   s.WeeklyWindowStart,
			MonthlyWindowStart:  s.MonthlyWindowStart,
		}
	}
	return out
}

func wrapQuotaNotFound(err error) error {
	if errors.Is(err, ErrUserPlatformQuotaNotFound) {
		return fmt.Errorf("%w: %w", service.ErrUserPlatformQuotaNotFound, err)
	}
	return err
}

func wrapQuotaFKViolation(err error) error {
	if errors.Is(err, ErrUserPlatformQuotaFKViolation) {
		return fmt.Errorf("%w: %v", service.ErrUserPlatformQuotaFKViolation, err)
	}
	return err
}
