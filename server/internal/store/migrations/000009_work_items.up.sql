-- Conversation-derived work objects.  The source post remains the authority
-- for channel scope; nullable foreign keys preserve the work history when a
-- source message or assignee is removed.
CREATE TABLE work_items (
    id                  TEXT PRIMARY KEY,
    kind                TEXT NOT NULL,
    title               TEXT NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL,
    created_by          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    assignee_id         TEXT REFERENCES users(id) ON DELETE SET NULL,
    team_id             TEXT REFERENCES teams(id) ON DELETE CASCADE,
    channel_id          TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    source_post_id      TEXT REFERENCES posts(id) ON DELETE SET NULL,
    source_thread_id    TEXT REFERENCES posts(id) ON DELETE SET NULL,
    idempotency_key     TEXT NOT NULL,
    due_at              BIGINT NOT NULL DEFAULT 0,
    decided_at          BIGINT NOT NULL DEFAULT 0,
    create_at           BIGINT NOT NULL,
    update_at           BIGINT NOT NULL,
    delete_at           BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT work_items_kind_check CHECK (kind IN ('task', 'decision')),
    CONSTRAINT work_items_status_check CHECK (
        (kind='task' AND status IN ('open', 'in_progress', 'done', 'cancelled')) OR
        (kind='decision' AND status IN ('recorded', 'superseded', 'cancelled'))
    ),
    CONSTRAINT work_items_title_length_check CHECK (char_length(title) BETWEEN 1 AND 240),
    CONSTRAINT work_items_description_length_check CHECK (char_length(description) <= 10000),
    CONSTRAINT work_items_id_length_check CHECK (char_length(id) BETWEEN 1 AND 128),
    CONSTRAINT work_items_idempotency_length_check CHECK (char_length(idempotency_key) BETWEEN 1 AND 200),
    CONSTRAINT work_items_due_at_check CHECK (due_at >= 0),
    CONSTRAINT work_items_decided_at_check CHECK (decided_at >= 0),
    CONSTRAINT work_items_shape_check CHECK (
        (kind='task' AND decided_at=0) OR
        (kind='decision' AND assignee_id IS NULL AND due_at=0 AND decided_at>0)
    ),
    CONSTRAINT work_items_timestamps_check CHECK (
        create_at>0 AND update_at>=create_at AND delete_at>=0
    )
);

CREATE INDEX work_items_owner_idx
    ON work_items (created_by, kind, create_at DESC, id DESC)
    WHERE delete_at=0;

CREATE INDEX work_items_assignee_idx
    ON work_items (assignee_id, status, due_at, id)
    WHERE delete_at=0 AND assignee_id IS NOT NULL;

CREATE INDEX work_items_channel_idx
    ON work_items (channel_id, kind, create_at DESC, id DESC)
    WHERE delete_at=0;

CREATE INDEX work_items_source_idx
    ON work_items (source_post_id, kind, create_at DESC)
    WHERE delete_at=0 AND source_post_id IS NOT NULL;

CREATE UNIQUE INDEX work_items_idempotency_idx
    ON work_items (created_by, idempotency_key);
