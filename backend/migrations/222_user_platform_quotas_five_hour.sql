-- user_platform_quotas 增加 5 小时档，与 Claude 官方的 5h 会话窗口对齐。
--
-- 语义与既有三档一致：NULL = 不限制，0 = 显式禁用，>0 = USD 上限。
--
-- 窗口边界：当系统设置 quota_reference_accounts 为该平台指定了「用量基准账号」时，
-- 5h 与周窗口跟随该账号的真实上游窗口（session_window_end / passive_usage_7d_reset）；
-- 未配置时 5h 退回「首次消费起算」的滚动窗口、周保持自然周，行为与本次改动前完全一致。
-- 详见 internal/service/quota_window_resolver.go。

ALTER TABLE user_platform_quotas
    ADD COLUMN IF NOT EXISTS five_hour_limit_usd   DECIMAL(20,10);

ALTER TABLE user_platform_quotas
    ADD COLUMN IF NOT EXISTS five_hour_usage_usd   DECIMAL(20,10) NOT NULL DEFAULT 0;

-- NULL = 窗口尚未初始化；首次预检 / 首次计费时会被写入。
ALTER TABLE user_platform_quotas
    ADD COLUMN IF NOT EXISTS five_hour_window_start TIMESTAMPTZ;
