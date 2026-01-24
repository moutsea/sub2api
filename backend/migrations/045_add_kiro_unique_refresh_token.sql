-- Migration: Add unique index for Kiro refresh_token to prevent duplicates
--
-- This migration adds a partial unique index on the refresh_token field
-- for Kiro accounts to prevent importing duplicate accounts.
--
-- The index only applies to:
-- - platform = 'kiro'
-- - deleted_at IS NULL (ignore soft-deleted accounts)
-- - refresh_token is not null and not empty
--
-- This ensures each Kiro refresh_token can only be used once in active accounts.

-- Create unique index for Kiro refresh_token
CREATE UNIQUE INDEX IF NOT EXISTS idx_kiro_unique_refresh_token
ON accounts ((credentials->>'refresh_token'))
WHERE platform = 'kiro'
    AND deleted_at IS NULL
    AND credentials->>'refresh_token' IS NOT NULL
    AND credentials->>'refresh_token' != '';

-- Add comment for documentation
COMMENT ON INDEX idx_kiro_unique_refresh_token IS
'Ensures each Kiro refresh_token is unique among active accounts. Prevents duplicate account imports.';
