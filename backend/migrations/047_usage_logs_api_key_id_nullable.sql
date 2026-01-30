-- 允许 usage_logs.api_key_id 为 NULL
-- 当使用临时 API Key 时，没有真实的 api_key_id，只有 temp_api_key_id

-- 1. 删除外键约束
ALTER TABLE usage_logs DROP CONSTRAINT IF EXISTS usage_logs_api_key_id_fkey;

-- 2. 修改列为可空
ALTER TABLE usage_logs ALTER COLUMN api_key_id DROP NOT NULL;

-- 3. 重新添加外键约束（允许 NULL）
ALTER TABLE usage_logs ADD CONSTRAINT usage_logs_api_key_id_fkey
    FOREIGN KEY (api_key_id) REFERENCES api_keys(id) ON DELETE CASCADE;

-- 4. 添加检查约束：api_key_id 和 temp_api_key_id 至少有一个非空
ALTER TABLE usage_logs ADD CONSTRAINT usage_logs_key_id_check
    CHECK (api_key_id IS NOT NULL OR temp_api_key_id IS NOT NULL);
