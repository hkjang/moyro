-- Durable, user-scoped activity inbox. Events contain only the structured
-- display fields needed by clients; source payloads and credentials must stay
-- in their owning subsystems instead of being copied into this table.

CREATE TABLE activity_events (
    id               TEXT PRIMARY KEY,
    user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    event_type       TEXT NOT NULL,
    actor_id         TEXT NOT NULL DEFAULT '',
    team_id          TEXT NOT NULL DEFAULT '',
    channel_id       TEXT NOT NULL DEFAULT '',
    post_id          TEXT NOT NULL DEFAULT '',
    resource_type    TEXT NOT NULL DEFAULT '',
    resource_id      TEXT NOT NULL DEFAULT '',
    title            TEXT NOT NULL,
    summary          TEXT NOT NULL DEFAULT '',
    dedupe_key       TEXT NOT NULL,
    create_at        BIGINT NOT NULL,
    update_at        BIGINT NOT NULL,
    read_at          BIGINT NOT NULL DEFAULT 0,
    completed_at     BIGINT NOT NULL DEFAULT 0,
    snoozed_until    BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT activity_events_type_check CHECK (event_type IN (
        'mention',
        'thread_reply',
        'direct_message',
        'approval_requested',
        'decided',
        'reminder_fired',
        'task_assigned',
        'system_warning',
        'plugin_event'
    )),
    CONSTRAINT activity_events_id_size_check CHECK (char_length(id) BETWEEN 1 AND 128),
    CONSTRAINT activity_events_actor_size_check CHECK (char_length(actor_id) <= 128),
    CONSTRAINT activity_events_team_size_check CHECK (char_length(team_id) <= 128),
    CONSTRAINT activity_events_channel_size_check CHECK (char_length(channel_id) <= 128),
    CONSTRAINT activity_events_post_size_check CHECK (char_length(post_id) <= 128),
    CONSTRAINT activity_events_resource_type_size_check CHECK (char_length(resource_type) <= 64),
    CONSTRAINT activity_events_resource_id_size_check CHECK (char_length(resource_id) <= 128),
    CONSTRAINT activity_events_title_size_check CHECK (char_length(title) BETWEEN 1 AND 256),
    CONSTRAINT activity_events_summary_size_check CHECK (char_length(summary) <= 4096),
    CONSTRAINT activity_events_dedupe_size_check CHECK (char_length(dedupe_key) BETWEEN 1 AND 256),
    CONSTRAINT activity_events_timestamps_check CHECK (
        create_at > 0 AND update_at >= create_at AND
        read_at >= 0 AND completed_at >= 0 AND snoozed_until >= 0
    ),
    CONSTRAINT activity_events_user_type_dedupe_unique UNIQUE (user_id, event_type, dedupe_key)
);

CREATE INDEX activity_events_user_cursor_idx
    ON activity_events (user_id, create_at DESC, id DESC);

CREATE INDEX activity_events_user_type_cursor_idx
    ON activity_events (user_id, event_type, create_at DESC, id DESC);

CREATE INDEX activity_events_user_unread_cursor_idx
    ON activity_events (user_id, create_at DESC, id DESC)
    WHERE read_at = 0;

CREATE INDEX activity_events_user_snoozed_idx
    ON activity_events (user_id, snoozed_until)
    WHERE snoozed_until > 0;
