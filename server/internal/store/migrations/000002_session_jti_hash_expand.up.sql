-- Expand phase for keyed session lookup. The legacy token column remains
-- required so v0.1 binaries can share the database during a rolling upgrade.
-- New binaries dual-write both columns and lazily backfill legacy rows after
-- validating the signed JWT. A later contract migration may remove token only
-- after every running binary understands jti_hash.
ALTER TABLE sessions ADD COLUMN jti_hash BYTEA;

ALTER TABLE sessions
    ADD CONSTRAINT sessions_jti_hash_length
    CHECK (jti_hash IS NULL OR octet_length(jti_hash) = 32);

CREATE UNIQUE INDEX sessions_jti_hash_uidx
    ON sessions (jti_hash)
    WHERE jti_hash IS NOT NULL;
