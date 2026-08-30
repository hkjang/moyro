-- Durable plugin lifecycle and the namespaced key/value contract used by
-- Mattermost server plugins. Configuration is encrypted by the application;
-- only the envelope is stored here.
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS enabled BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS runtime_kind TEXT NOT NULL DEFAULT 'moyro_v1';
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS bundle_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS last_error TEXT NOT NULL DEFAULT '';
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS installed_by TEXT NOT NULL DEFAULT '';
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS installed_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS activated_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS config_key_id TEXT NOT NULL DEFAULT '';
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS config_nonce BYTEA;
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS config_ciphertext BYTEA;

CREATE TABLE IF NOT EXISTS plugin_key_values (
    plugin_id  TEXT NOT NULL REFERENCES plugins(id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value      BYTEA NOT NULL,
    expire_at  BIGINT NOT NULL DEFAULT 0,
    create_at  BIGINT NOT NULL,
    update_at  BIGINT NOT NULL,
    PRIMARY KEY (plugin_id, key),
    CHECK (length(key) BETWEEN 1 AND 150)
);

CREATE INDEX IF NOT EXISTS plugin_key_values_expiry_idx
    ON plugin_key_values (expire_at)
    WHERE expire_at > 0;
