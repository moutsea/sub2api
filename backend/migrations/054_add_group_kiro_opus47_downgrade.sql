-- Add Kiro group switch to downgrade claude-opus-4-7 requests to claude-opus-4-6.
ALTER TABLE groups
ADD COLUMN IF NOT EXISTS kiro_opus_47_downgrade BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN groups.kiro_opus_47_downgrade IS 'Kiro 分组请求 claude-opus-4-7 时降级到 claude-opus-4-6';
