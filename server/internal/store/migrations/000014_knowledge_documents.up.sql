-- Channel-scoped, conversation-derived documents. Document bodies remain
-- searchable with PostgreSQL's offline-safe full-text index; authorization is
-- always re-evaluated against live team/channel memberships by the service.

CREATE TABLE documents (
    id                  TEXT PRIMARY KEY,
    title               TEXT NOT NULL,
    body                TEXT NOT NULL,
    created_by          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    team_id             TEXT REFERENCES teams(id) ON DELETE CASCADE,
    channel_id          TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    source_thread_id    TEXT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    source_cursor_at    BIGINT NOT NULL,
    idempotency_key     TEXT NOT NULL,
    -- Service-created rows always store a SHA-256 request fingerprint. The
    -- zero digest default keeps offline imports/fixtures compatible while
    -- making their replay behavior fail closed instead of consulting mutable
    -- title/body fields.
    create_fingerprint  TEXT NOT NULL DEFAULT repeat('0', 64),
    revision            BIGINT NOT NULL DEFAULT 1,
    create_at           BIGINT NOT NULL,
    update_at           BIGINT NOT NULL,
    delete_at           BIGINT NOT NULL DEFAULT 0,
    tsv                 TSVECTOR,
    CONSTRAINT documents_title_size_check CHECK (char_length(title) BETWEEN 1 AND 240),
    CONSTRAINT documents_body_size_check CHECK (char_length(body) BETWEEN 1 AND 100000),
    CONSTRAINT documents_idempotency_size_check CHECK (char_length(idempotency_key) BETWEEN 1 AND 200),
    CONSTRAINT documents_create_fingerprint_check CHECK (char_length(create_fingerprint) = 64),
    CONSTRAINT documents_revision_check CHECK (revision > 0),
    CONSTRAINT documents_source_cursor_check CHECK (source_cursor_at > 0),
    CONSTRAINT documents_timestamps_check CHECK (
        create_at > 0 AND update_at >= create_at AND delete_at >= 0
    ),
    CONSTRAINT documents_creator_idempotency_unique UNIQUE (created_by, idempotency_key)
);

CREATE INDEX documents_channel_cursor_idx
    ON documents (channel_id, update_at DESC, id DESC)
    WHERE delete_at = 0;

CREATE INDEX documents_creator_cursor_idx
    ON documents (created_by, update_at DESC, id DESC)
    WHERE delete_at = 0;

CREATE INDEX documents_tsv_idx ON documents USING GIN (tsv);

CREATE OR REPLACE FUNCTION documents_tsv_update() RETURNS trigger AS $$
BEGIN
    NEW.tsv :=
        setweight(to_tsvector('simple', COALESCE(NEW.title, '')), 'A') ||
        setweight(to_tsvector('simple', COALESCE(NEW.body, '')), 'B');
    RETURN NEW;
END
$$ LANGUAGE plpgsql;

CREATE TRIGGER documents_tsv_trg BEFORE INSERT OR UPDATE OF title, body ON documents
    FOR EACH ROW EXECUTE FUNCTION documents_tsv_update();

-- Sources are server-resolved snapshots of one live thread.
-- documents.source_cursor_at is an opaque, positive content revision (not a
-- timestamp). The digest detects edits that share the same millisecond.
-- captured_update_at is retained as snapshot diagnostics, but stale checks do
-- not use it because attachment, pin, link-preview, and props-only metadata
-- updates must not invalidate message-derived content.
CREATE TABLE document_sources (
    document_id         TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    post_id             TEXT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    position            INTEGER NOT NULL,
    captured_update_at  BIGINT NOT NULL,
    captured_content_digest TEXT NOT NULL,
    PRIMARY KEY (document_id, post_id),
    CONSTRAINT document_sources_position_unique UNIQUE (document_id, position),
    CONSTRAINT document_sources_position_check CHECK (position >= 0),
    CONSTRAINT document_sources_update_at_check CHECK (captured_update_at > 0),
    CONSTRAINT document_sources_digest_check CHECK (length(captured_content_digest) = 32)
);

CREATE INDEX document_sources_post_idx ON document_sources (post_id, document_id);
