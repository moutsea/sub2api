-- Add per-user custom rate_multiplier override on user_allowed_groups.
-- NULL means "use the group default"; a non-NULL value overrides it.

ALTER TABLE user_allowed_groups
    ADD COLUMN IF NOT EXISTS rate_multiplier DECIMAL(10, 4) DEFAULT NULL;

COMMENT ON COLUMN user_allowed_groups.rate_multiplier IS
    'Per-user rate multiplier override. NULL = use group default.';
