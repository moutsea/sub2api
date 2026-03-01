-- 新增 time_quota 类型支持：限时 + 每日 USD 额度
-- 1. daily_quota_usd: 每日 USD 额度上限（time_quota 类型使用）
-- 2. current_period_cost_usd: 当前周期已消费 USD（time_quota 类型使用，周期重置时归零）

ALTER TABLE temp_api_keys
  ADD COLUMN IF NOT EXISTS daily_quota_usd DOUBLE PRECISION DEFAULT 0;

ALTER TABLE temp_api_keys
  ADD COLUMN IF NOT EXISTS current_period_cost_usd DOUBLE PRECISION DEFAULT 0;
