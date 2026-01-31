-- 临时 API Key quota_only 类型改为基于美元金额
-- 1. 将 total_quota (BIGINT, 请求次数) 改为 total_quota_usd (DOUBLE PRECISION, 美元)
-- 2. 新增 total_cost_usd 字段用于记录已消费金额

-- 重命名并更改类型 (保留原值，转为 float)
ALTER TABLE temp_api_keys
  ALTER COLUMN total_quota TYPE DOUBLE PRECISION USING total_quota::DOUBLE PRECISION;

ALTER TABLE temp_api_keys
  RENAME COLUMN total_quota TO total_quota_usd;

-- 添加已消费金额字段
ALTER TABLE temp_api_keys
  ADD COLUMN IF NOT EXISTS total_cost_usd DOUBLE PRECISION DEFAULT 0;
