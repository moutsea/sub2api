-- Add display_platform field to groups table for platform disguise
-- When set, frontend shows this value instead of actual platform
-- Example: platform="kiro", display_platform="anthropic" -> user sees "anthropic"
ALTER TABLE groups ADD COLUMN display_platform VARCHAR(50);
