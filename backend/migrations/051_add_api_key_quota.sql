-- Per-API-key usage quota: allow users to set a USD spending cap on each key.
-- NULL quota_limit_usd = no limit (default, backward compatible).

ALTER TABLE api_keys
  ADD COLUMN IF NOT EXISTS quota_limit_usd DOUBLE PRECISION DEFAULT NULL;

ALTER TABLE api_keys
  ADD COLUMN IF NOT EXISTS quota_used_usd DOUBLE PRECISION NOT NULL DEFAULT 0;
