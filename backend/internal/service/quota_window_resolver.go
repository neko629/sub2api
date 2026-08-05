package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// ---------------------------------------------------------------------------
// user × platform quota 的窗口边界解析
//
// 背景：日/月窗口一直是本地语义（自然日 / 30 天滚动），但 5h 与周窗口需要和
// Claude 官方对齐——即跟随某个「基准账号」的真实上游窗口。管理员在系统设置里
// 为每个平台指定一个基准账号后：
//
//	5h 窗口 = [account.session_window_end - 5h, account.session_window_end]
//	周窗口  = [account 7d resets_at - 7d, account 7d resets_at]
//
// 基准账号的窗口一滚动，所有用户的对应用量在同一时刻清零，与 Claude 官方一致。
// 未配置基准账号时（默认）行为完全不变：5h 退回滚动窗口、周退回自然周。
// ---------------------------------------------------------------------------

// Quota window keys（与 user_platform_quotas 的列前缀一一对应）。
const (
	QuotaWindowFiveHour = "five_hour"
	QuotaWindowDaily    = "daily"
	QuotaWindowWeekly   = "weekly"
	QuotaWindowMonthly  = "monthly"
)

// Quota window durations.
const (
	QuotaWindowFiveHourDuration = 5 * time.Hour
	QuotaWindowDailyDuration    = 24 * time.Hour
	QuotaWindowWeeklyDuration   = 7 * 24 * time.Hour
	QuotaWindowMonthlyDuration  = 30 * 24 * time.Hour
)

// 窗口边界的三种来源。过期判定与新起点的算法完全由 Source 决定，
// 三者不可混用——用错会得到「窗口未过期用量却被清零」这类隐蔽 bug。
const (
	// QuotaWindowSourceAccount 跟随基准账号的上游窗口。
	// 过期判定：start != frame.Start（账号窗口一滚动，上一轮立刻作废）。
	QuotaWindowSourceAccount = "account"
	// QuotaWindowSourceCalendar 自然日历边界（日；未配基准账号时的周）。
	// 过期判定：start < frame.Start。
	QuotaWindowSourceCalendar = "calendar"
	// QuotaWindowSourceRolling 从首次消费时刻起算的滚动窗口（月；未配基准账号时的 5h）。
	// 过期判定：now - start >= 窗口时长；新起点 = now。
	QuotaWindowSourceRolling = "rolling"
)

// SettingKeyQuotaReferenceAccounts —— 系统全局：每平台的用量基准账号。
// 值为 JSON map[platform]accountID，例如 {"anthropic": 7}。
// 缺省/为空 = 不跟随任何账号，5h 与周窗口使用本地语义（向后兼容）。
const SettingKeyQuotaReferenceAccounts = "quota_reference_accounts"

// ResolvedQuotaWindow 是某个 (platform, window) 在指定时刻的窗口边界。
//
// Start 为 nil 表示滚动窗口的起点由「首次消费时刻」决定，调用方需自行用
// 当前时刻作为新起点；End 为 nil 时同理，下次重置时刻 = 起点 + 窗口时长。
type ResolvedQuotaWindow struct {
	Start  *time.Time
	End    *time.Time
	Source string
}

// IsExpired 按 Source 判断已存起点是否属于上一轮窗口。
// start 为 nil（从未初始化）一律视为过期。
func (w ResolvedQuotaWindow) IsExpired(start *time.Time, now time.Time, dur time.Duration) bool {
	if start == nil {
		return true
	}
	switch w.Source {
	case QuotaWindowSourceAccount:
		// 账号窗口滚动后 frame.Start 会变，任何不等都意味着换窗。
		return w.Start == nil || !start.Equal(*w.Start)
	case QuotaWindowSourceCalendar:
		return w.Start != nil && start.Before(*w.Start)
	default:
		return now.Sub(*start) >= dur
	}
}

// NewStart 返回本轮窗口应写入 DB 的起点。滚动窗口锚在当前时刻。
func (w ResolvedQuotaWindow) NewStart(now time.Time) time.Time {
	if w.Start != nil {
		return *w.Start
	}
	return now
}

// NextReset 返回本轮窗口的重置时刻，用于 429 的 Retry-After / window_resets_at。
// start 为已存起点（可为 nil）。
func (w ResolvedQuotaWindow) NextReset(start *time.Time, now time.Time, dur time.Duration) time.Time {
	if w.End != nil {
		return *w.End
	}
	// 滚动窗口：未初始化或已过期时，下次重置 = now + 时长（下一次消费会把起点设为 now）。
	if start == nil || w.IsExpired(start, now, dur) {
		return now.Add(dur)
	}
	return start.Add(dur)
}

// QuotaWindowDuration 返回窗口档位对应的时长。未知档位返回 0。
func QuotaWindowDuration(window string) time.Duration {
	switch window {
	case QuotaWindowFiveHour:
		return QuotaWindowFiveHourDuration
	case QuotaWindowDaily:
		return QuotaWindowDailyDuration
	case QuotaWindowWeekly:
		return QuotaWindowWeeklyDuration
	case QuotaWindowMonthly:
		return QuotaWindowMonthlyDuration
	default:
		return 0
	}
}

// QuotaWindowResolver 解析 (platform, window) 在当前时刻的窗口边界。
// 计费热路径每个请求都会调用，实现必须无阻塞、不查 DB。
type QuotaWindowResolver interface {
	ResolveQuotaWindow(platform, window string, now time.Time) ResolvedQuotaWindow
	// ResolveAllQuotaWindows 一次解析四个档位，避免热路径上重复取快照。
	ResolveAllQuotaWindows(platform string, now time.Time) map[string]ResolvedQuotaWindow
}

// localQuotaWindow 是不跟随任何账号时的本地兜底语义。
// 独立成函数：resolver 缺失、基准账号未配置、账号窗口数据不可用三条路径共用，
// 保证「兜底行为只有一个定义」。
func localQuotaWindow(window string, now time.Time) ResolvedQuotaWindow {
	switch window {
	case QuotaWindowDaily:
		// 必须用 AddDate 而不是 Add(24h)：过期判定走的是 StartOfDay(now)，
		// 在有夏令时的时区里「下一个本地 0 点」与「+24 小时」相差一小时，
		// 两者不一致会让 429 报出的 resets_at / Retry-After 早一小时或晚一小时 ——
		// 客户端按 Retry-After 重试却仍被拒，或白等一小时。
		start := timezone.StartOfDay(now)
		end := start.AddDate(0, 0, 1)
		return ResolvedQuotaWindow{Start: &start, End: &end, Source: QuotaWindowSourceCalendar}
	case QuotaWindowWeekly:
		start := timezone.StartOfWeek(now)
		end := start.AddDate(0, 0, 7)
		return ResolvedQuotaWindow{Start: &start, End: &end, Source: QuotaWindowSourceCalendar}
	default:
		// five_hour / monthly：滚动窗口，起点由首次消费决定。
		return ResolvedQuotaWindow{Source: QuotaWindowSourceRolling}
	}
}

// LocalQuotaWindows 返回全部四档的本地兜底窗口。
// resolver 为 nil 时（单元测试、简易模式）供调用方直接使用。
func LocalQuotaWindows(now time.Time) map[string]ResolvedQuotaWindow {
	return map[string]ResolvedQuotaWindow{
		QuotaWindowFiveHour: localQuotaWindow(QuotaWindowFiveHour, now),
		QuotaWindowDaily:    localQuotaWindow(QuotaWindowDaily, now),
		QuotaWindowWeekly:   localQuotaWindow(QuotaWindowWeekly, now),
		QuotaWindowMonthly:  localQuotaWindow(QuotaWindowMonthly, now),
	}
}

// ResolveQuotaWindowsWith 是 resolver 可为 nil 的安全包装。
func ResolveQuotaWindowsWith(r QuotaWindowResolver, platform string, now time.Time) map[string]ResolvedQuotaWindow {
	if r == nil {
		return LocalQuotaWindows(now)
	}
	return r.ResolveAllQuotaWindows(platform, now)
}

// ---------------------------------------------------------------------------
// QuotaWindowSyncService：定时把基准账号的上游窗口刷进内存快照
// ---------------------------------------------------------------------------

// quotaWindowSnapshot 是热路径读取的不可变快照。
type quotaWindowSnapshot struct {
	// byPlatform[platform] = 该平台基准账号的上游窗口端点
	byPlatform map[string]*referenceWindowEnds
	loadedAt   time.Time
}

type referenceWindowEnds struct {
	accountID int64
	// fiveHourEnd / weeklyEnd 为零值表示该窗口数据不可用（退回本地语义）。
	fiveHourEnd time.Time
	weeklyEnd   time.Time
}

// quotaWindowRefreshInterval 基准账号窗口的刷新周期。
// session_window_end 变化频率以小时计，15s 足够及时，且远低于热路径读取频率。
const quotaWindowRefreshInterval = 15 * time.Second

// QuotaWindowSyncService 维护基准账号窗口快照，并实现 QuotaWindowResolver。
type QuotaWindowSyncService struct {
	settingRepo SettingRepository
	accountRepo AccountRepository
	timingWheel *TimingWheelService

	snapshot atomic.Pointer[quotaWindowSnapshot]
	stopped  atomic.Bool
}

func NewQuotaWindowSyncService(
	settingRepo SettingRepository,
	accountRepo AccountRepository,
	tw *TimingWheelService,
) *QuotaWindowSyncService {
	s := &QuotaWindowSyncService{settingRepo: settingRepo, accountRepo: accountRepo, timingWheel: tw}
	s.snapshot.Store(&quotaWindowSnapshot{byPlatform: map[string]*referenceWindowEnds{}})
	return s
}

// Start 立即刷新一次并注册定时刷新。
func (s *QuotaWindowSyncService) Start() {
	if s == nil {
		return
	}
	s.Refresh(context.Background())
	if s.timingWheel != nil {
		s.timingWheel.ScheduleRecurring("quota:window_sync", quotaWindowRefreshInterval, func() {
			if s.stopped.Load() {
				return
			}
			s.Refresh(context.Background())
		})
	}
}

func (s *QuotaWindowSyncService) Stop() {
	if s == nil {
		return
	}
	s.stopped.Store(true)
	if s.timingWheel != nil {
		s.timingWheel.Cancel("quota:window_sync")
	}
}

// Refresh 重新读取设置与基准账号，替换快照。
// 任何一步失败都保留旧快照（fail-safe：宁可用略旧的边界，也不要突然退回本地语义
// 把所有用户的窗口重置一遍）。
func (s *QuotaWindowSyncService) Refresh(ctx context.Context) {
	if s == nil || s.settingRepo == nil {
		return
	}
	refs, err := s.loadReferenceAccountIDs(ctx)
	if err != nil {
		// 设置从未写过是默认状态（没有任何平台配基准账号），不是故障 ——
		// 当成 warn 会让绝大多数部署每 15s 刷一条噪音日志。
		if !errors.Is(err, ErrSettingNotFound) {
			slog.Warn("quota_window_sync_read_setting_failed", "error", err)
			return
		}
		refs = map[string]int64{}
	}

	prev := s.snapshot.Load()
	next := &quotaWindowSnapshot{byPlatform: make(map[string]*referenceWindowEnds, len(refs)), loadedAt: time.Now()}
	for platform, accountID := range refs {
		if accountID <= 0 || s.accountRepo == nil {
			continue
		}
		account, err := s.accountRepo.GetByID(ctx, accountID)
		if err != nil || account == nil {
			// 读取失败时**沿用上一轮的端点**，绝不能把该平台从快照里丢掉。
			// 丢掉会让窗口来源从 account 掉回 calendar/rolling，而两者的过期判定
			// 完全不同 —— 已存的 start 会被判为跨窗，所有用户的周/5h 用量被瞬间清零；
			// 账号读取恢复后来源再翻回 account，又清一次。一次 DB 抖动就能把全站额度抹平。
			// 宁可用略旧的边界，也不做这种破坏性回退。
			if prev != nil {
				if carried := prev.byPlatform[platform]; carried != nil && carried.accountID == accountID {
					next.byPlatform[platform] = carried
					slog.Warn("quota_window_reference_account_unavailable_kept_previous",
						"platform", platform, "account_id", accountID, "error", err)
					continue
				}
			}
			slog.Warn("quota_window_reference_account_unavailable",
				"platform", platform, "account_id", accountID, "error", err)
			continue
		}
		ends := &referenceWindowEnds{accountID: accountID}
		if account.SessionWindowEnd != nil {
			ends.fiveHourEnd = *account.SessionWindowEnd
		}
		if ts := parseExtraFloat64(account.Extra["passive_usage_7d_reset"]); ts > 0 {
			ends.weeklyEnd = time.Unix(int64(ts), 0)
		}
		// 同理：账号还在、但某个窗口端点这一轮读不到（例如 extra 被别的流程覆盖），
		// 也沿用上一轮的值，避免单个窗口来源反复翻转。
		if prev != nil {
			if carried := prev.byPlatform[platform]; carried != nil && carried.accountID == accountID {
				if ends.fiveHourEnd.IsZero() {
					ends.fiveHourEnd = carried.fiveHourEnd
				}
				if ends.weeklyEnd.IsZero() {
					ends.weeklyEnd = carried.weeklyEnd
				}
			}
		}
		next.byPlatform[platform] = ends
	}
	for platform, ends := range next.byPlatform {
		logStaleReferenceWindows(platform, ends, next.loadedAt)
	}
	s.snapshot.Store(next)
}

// loadReferenceAccountIDs 解析设置值。空值/解析失败均返回空 map（无基准账号）。
func (s *QuotaWindowSyncService) loadReferenceAccountIDs(ctx context.Context) (map[string]int64, error) {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyQuotaReferenceAccounts)
	if err != nil {
		return nil, err
	}
	return ParseQuotaReferenceAccounts(raw), nil
}

// ParseQuotaReferenceAccounts 把设置里的 JSON 解析成 platform → accountID。
// 非法 JSON、非法平台、非正数 ID 一律丢弃（fail-open 到本地语义，绝不阻断请求）。
func ParseQuotaReferenceAccounts(raw string) map[string]int64 {
	out := map[string]int64{}
	if raw == "" {
		return out
	}
	parsed := map[string]int64{}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		slog.Warn("quota_reference_accounts_unmarshal_failed", "error", err)
		return out
	}
	for platform, id := range parsed {
		if id > 0 && IsAllowedQuotaPlatform(platform) {
			out[platform] = id
		}
	}
	return out
}

// ResolveQuotaWindow 实现 QuotaWindowResolver。
func (s *QuotaWindowSyncService) ResolveQuotaWindow(platform, window string, now time.Time) ResolvedQuotaWindow {
	if s == nil || (window != QuotaWindowFiveHour && window != QuotaWindowWeekly) {
		return localQuotaWindow(window, now)
	}
	snap := s.snapshot.Load()
	if snap == nil {
		return localQuotaWindow(window, now)
	}
	ends := snap.byPlatform[platform]
	if ends == nil {
		return localQuotaWindow(window, now)
	}

	var (
		upstreamEnd time.Time
		dur         time.Duration
	)
	if window == QuotaWindowFiveHour {
		upstreamEnd, dur = ends.fiveHourEnd, QuotaWindowFiveHourDuration
	} else {
		upstreamEnd, dur = ends.weeklyEnd, QuotaWindowWeeklyDuration
	}
	if upstreamEnd.IsZero() {
		// 基准账号存在但该窗口从未产生过数据 → 本地兜底。
		return localQuotaWindow(window, now)
	}

	// 截断到秒：account 来源的过期判定是「起点必须完全相等」，而已存起点会经
	// Redis（按 Unix 秒存取）往返。若上游端点带亚秒精度（校准接口直接收 RFC3339，
	// 绕过前端就能传进来），往返后永远比不相等 —— 每次预检都判跨窗、用量被清零，
	// 5h 限额将完全失效。在边界生成处统一截断，两侧口径就永远一致。
	end := advanceStaleWindow(upstreamEnd.Truncate(time.Second), dur, now)
	start := end.Add(-dur)
	return ResolvedQuotaWindow{Start: &start, End: &end, Source: QuotaWindowSourceAccount}
}

func (s *QuotaWindowSyncService) ResolveAllQuotaWindows(platform string, now time.Time) map[string]ResolvedQuotaWindow {
	return map[string]ResolvedQuotaWindow{
		QuotaWindowFiveHour: s.ResolveQuotaWindow(platform, QuotaWindowFiveHour, now),
		QuotaWindowDaily:    localQuotaWindow(QuotaWindowDaily, now),
		QuotaWindowWeekly:   s.ResolveQuotaWindow(platform, QuotaWindowWeekly, now),
		QuotaWindowMonthly:  localQuotaWindow(QuotaWindowMonthly, now),
	}
}

// advanceStaleWindow 把已过期的上游窗口端点按窗口时长向前步进到下一个未来边界。
//
// 这一步不能省。基准账号闲置时不会有新请求 → 没有新的响应头 → session_window_end
// 会一直停在过去。若直接采用该过期端点，窗口边界永远算不到未来，用户用量永不清零，
// 所有人被永久挡在限额外（个人网关只有一两个账号，几天不用就会触发）。
// Claude 的 5h 窗口是连续的，按时长步进能落回真实网格附近，且保证限额一定会刷新。
// maxStaleWindowSteps 步进次数上限。超过说明端点陈旧到没有参考价值
// （例如账号停用数月），此时直接以 now 为界，避免长循环。
const maxStaleWindowSteps = 4096

// 纯计算、无日志：本函数在计费热路径上每个请求都会被调用（preflight + 计费写入
// 各一次），在这里打日志会让「基准账号闲置」这种完全正常的状态每秒刷上百行 warn。
// 陈旧检测的可观测性由 Refresh 承担 —— 它 15s 一次，且陈旧与否是账号的属性而非请求的属性。
func advanceStaleWindow(end time.Time, dur time.Duration, now time.Time) time.Time {
	if end.After(now) || dur <= 0 {
		return end
	}
	// 需要步进的整窗数：now 与 end 的间隔除以窗口时长，向下取整后 +1
	// 保证结果严格晚于 now。
	steps := int(now.Sub(end)/dur) + 1
	if steps > maxStaleWindowSteps {
		return now.Add(dur)
	}
	return end.Add(time.Duration(steps) * dur)
}

// logStaleReferenceWindows 在 Refresh 周期里报告陈旧的基准窗口。
// 单独成函数是为了让 advanceStaleWindow 在热路径上保持零分配、零日志。
func logStaleReferenceWindows(platform string, ends *referenceWindowEnds, now time.Time) {
	for _, w := range []struct {
		window string
		end    time.Time
		dur    time.Duration
	}{
		{QuotaWindowFiveHour, ends.fiveHourEnd, QuotaWindowFiveHourDuration},
		{QuotaWindowWeekly, ends.weeklyEnd, QuotaWindowWeeklyDuration},
	} {
		if w.end.IsZero() || w.end.After(now) {
			continue
		}
		slog.Warn("quota_window_reference_stale_advanced",
			"platform", platform, "window", w.window, "account_id", ends.accountID,
			"upstream_end", w.end, "advanced_end", advanceStaleWindow(w.end, w.dur, now))
	}
}
