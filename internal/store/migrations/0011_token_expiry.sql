-- 0011_token_expiry.sql: optional expiry for mmp_ agent tokens.
-- NULL = no expiry (every token minted before this migration keeps working and is
-- reported as "no expiry" so the operator can rotate it).
ALTER TABLE api_tokens ADD COLUMN expires_at TEXT;
