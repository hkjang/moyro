-- Crash-recovery journal for plugin installs and replacements. The snapshot
-- tables preserve the previous plugin row and namespaced KV data until the
-- new bundle is activated, persisted, and durably committed.
CREATE TABLE IF NOT EXISTS plugin_install_transactions (
    id              TEXT PRIMARY KEY,
    plugin_id       TEXT NOT NULL UNIQUE,
    had_target      BOOLEAN NOT NULL,
    backup_name     TEXT NOT NULL DEFAULT '',
    phase           TEXT NOT NULL DEFAULT 'prepared',
    create_at       BIGINT NOT NULL,
    CHECK (length(id) BETWEEN 1 AND 128),
    CHECK (length(plugin_id) BETWEEN 1 AND 190),
    CHECK (phase IN ('prepared', 'moving_old', 'old_backed_up', 'promoting_new', 'restoring_old', 'removing_new')),
    CHECK (backup_name = '' OR (
        backup_name LIKE '.moyro-plugin-backup-%'
        AND backup_name NOT LIKE '%/%'
        AND backup_name NOT LIKE '%\\%'
    ))
);

CREATE TABLE IF NOT EXISTS plugin_install_plugin_snapshots (
    transaction_id      TEXT PRIMARY KEY REFERENCES plugin_install_transactions(id) ON DELETE CASCADE,
    plugin_id           TEXT NOT NULL,
    version             TEXT NOT NULL,
    state               TEXT NOT NULL,
    manifest            JSONB NOT NULL,
    create_at           BIGINT NOT NULL,
    update_at           BIGINT NOT NULL,
    enabled             BOOLEAN NOT NULL,
    runtime_kind        TEXT NOT NULL,
    bundle_sha256       TEXT NOT NULL,
    last_error          TEXT NOT NULL,
    installed_by        TEXT NOT NULL,
    installed_at        BIGINT NOT NULL,
    activated_at        BIGINT NOT NULL,
    config_key_id       TEXT NOT NULL,
    config_nonce        BYTEA,
    config_ciphertext   BYTEA
);

CREATE TABLE IF NOT EXISTS plugin_install_kv_snapshots (
    transaction_id  TEXT NOT NULL REFERENCES plugin_install_transactions(id) ON DELETE CASCADE,
    key             TEXT NOT NULL,
    value           BYTEA NOT NULL,
    expire_at       BIGINT NOT NULL,
    create_at       BIGINT NOT NULL,
    update_at       BIGINT NOT NULL,
    PRIMARY KEY (transaction_id, key)
);

CREATE INDEX IF NOT EXISTS plugin_install_transactions_created_idx
    ON plugin_install_transactions (create_at, id);
