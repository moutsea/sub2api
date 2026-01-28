-- 临时 API Key 表
-- 特性：
-- 1. 有有效期限制（从首次使用开始计算）
-- 2. 每 24 小时最多 1000 次请求
-- 3. 只能使用指定分组的模型
-- 4. 由管理员批量创建

CREATE TABLE temp_api_keys (
    id              BIGSERIAL PRIMARY KEY,
    key             VARCHAR(128) NOT NULL UNIQUE,  -- sk-temp-xxx 格式
    name            VARCHAR(100) NOT NULL,         -- 名称/备注
    group_id        BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,  -- 关联分组（必须）

    -- 有效期设置
    valid_days      INTEGER NOT NULL DEFAULT 7,    -- 有效天数（创建时设置）
    activated_at    TIMESTAMPTZ,                   -- 首次激活时间（首次使用时设置）
    expires_at      TIMESTAMPTZ,                   -- 过期时间（首次使用时计算：activated_at + valid_days * 24h）

    -- 请求限制
    daily_limit     INTEGER NOT NULL DEFAULT 1000, -- 每日请求限制

    -- 当前周期使用统计
    current_period_start TIMESTAMPTZ,             -- 当前 24 小时周期开始时间
    current_period_count INTEGER NOT NULL DEFAULT 0, -- 当前周期请求次数

    -- 总计统计
    total_requests  BIGINT NOT NULL DEFAULT 0,    -- 总请求次数

    -- 状态
    status          VARCHAR(20) NOT NULL DEFAULT 'active',  -- active, inactive, expired

    -- 创建信息
    created_by      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,  -- 创建者（管理员）
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ  -- 软删除
);

-- 索引
CREATE UNIQUE INDEX idx_temp_api_keys_key ON temp_api_keys(key) WHERE deleted_at IS NULL;
CREATE INDEX idx_temp_api_keys_group_id ON temp_api_keys(group_id);
CREATE INDEX idx_temp_api_keys_status ON temp_api_keys(status);
CREATE INDEX idx_temp_api_keys_created_by ON temp_api_keys(created_by);
CREATE INDEX idx_temp_api_keys_expires_at ON temp_api_keys(expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX idx_temp_api_keys_deleted_at ON temp_api_keys(deleted_at);

-- 注释
COMMENT ON TABLE temp_api_keys IS '临时 API Key 表，支持有效期和每日请求限制';
COMMENT ON COLUMN temp_api_keys.valid_days IS '有效天数，从首次使用开始计算';
COMMENT ON COLUMN temp_api_keys.activated_at IS '首次激活时间，即首次被使用的时间';
COMMENT ON COLUMN temp_api_keys.expires_at IS '过期时间，activated_at + valid_days * 24h';
COMMENT ON COLUMN temp_api_keys.daily_limit IS '每 24 小时请求次数限制';
COMMENT ON COLUMN temp_api_keys.current_period_start IS '当前 24 小时周期开始时间';
COMMENT ON COLUMN temp_api_keys.current_period_count IS '当前周期已使用的请求次数';
