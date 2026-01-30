-- 临时 API Key 新增类型字段
-- key_type: time_limited（限时限额，默认）、quota_only（仅限额不限时）
-- total_quota: 总额度限制（请求次数），仅 quota_only 类型使用

ALTER TABLE temp_api_keys ADD COLUMN IF NOT EXISTS key_type VARCHAR(20) DEFAULT 'time_limited';
ALTER TABLE temp_api_keys ADD COLUMN IF NOT EXISTS total_quota BIGINT DEFAULT 0;
