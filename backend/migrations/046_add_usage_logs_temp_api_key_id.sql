-- 为 usage_logs 表添加 temp_api_key_id 字段
-- 用于追踪临时 API Key 的使用日志

ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS temp_api_key_id BIGINT;

-- 添加索引以支持按 temp_api_key_id 查询
CREATE INDEX IF NOT EXISTS idx_usage_logs_temp_api_key_id ON usage_logs(temp_api_key_id) WHERE temp_api_key_id IS NOT NULL;

-- 添加外键约束（可选，如果需要强引用完整性）
-- ALTER TABLE usage_logs ADD CONSTRAINT fk_usage_logs_temp_api_key
--     FOREIGN KEY (temp_api_key_id) REFERENCES temp_api_keys(id) ON DELETE SET NULL;
