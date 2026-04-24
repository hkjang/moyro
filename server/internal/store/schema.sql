-- Moddle MVP1 schema. Mattermost-compatible field names where feasible.

CREATE TABLE IF NOT EXISTS users (
    id              TEXT PRIMARY KEY,
    username        TEXT UNIQUE NOT NULL,
    email           TEXT UNIQUE NOT NULL,
    password_hash   TEXT NOT NULL,
    roles           TEXT NOT NULL DEFAULT 'system_user',
    create_at       BIGINT NOT NULL,
    update_at       BIGINT NOT NULL,
    delete_at       BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS teams (
    id              TEXT PRIMARY KEY,
    name            TEXT UNIQUE NOT NULL,
    display_name    TEXT NOT NULL,
    type            TEXT NOT NULL DEFAULT 'O',
    create_at       BIGINT NOT NULL,
    update_at       BIGINT NOT NULL,
    delete_at       BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS team_members (
    team_id         TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    roles           TEXT NOT NULL DEFAULT 'team_user',
    create_at       BIGINT NOT NULL,
    PRIMARY KEY (team_id, user_id)
);

CREATE TABLE IF NOT EXISTS channels (
    id              TEXT PRIMARY KEY,
    team_id         TEXT REFERENCES teams(id) ON DELETE CASCADE,
    type            TEXT NOT NULL,
    display_name    TEXT NOT NULL,
    name            TEXT NOT NULL,
    header          TEXT NOT NULL DEFAULT '',
    purpose         TEXT NOT NULL DEFAULT '',
    create_at       BIGINT NOT NULL,
    update_at       BIGINT NOT NULL,
    delete_at       BIGINT NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS channels_team_name_idx ON channels (team_id, name);

CREATE TABLE IF NOT EXISTS channel_members (
    channel_id      TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    roles           TEXT NOT NULL DEFAULT 'channel_user',
    notify_props    JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_viewed_at  BIGINT NOT NULL DEFAULT 0,
    create_at       BIGINT NOT NULL,
    PRIMARY KEY (channel_id, user_id)
);

CREATE TABLE IF NOT EXISTS posts (
    id              TEXT PRIMARY KEY,
    channel_id      TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    root_id         TEXT NOT NULL DEFAULT '',
    message         TEXT NOT NULL,
    props           JSONB NOT NULL DEFAULT '{}'::jsonb,
    create_at       BIGINT NOT NULL,
    update_at       BIGINT NOT NULL,
    delete_at       BIGINT NOT NULL DEFAULT 0,
    tsv             tsvector
);

CREATE INDEX IF NOT EXISTS posts_channel_create_idx ON posts (channel_id, create_at DESC);
CREATE INDEX IF NOT EXISTS posts_root_idx ON posts (root_id) WHERE root_id <> '';
CREATE INDEX IF NOT EXISTS posts_tsv_idx ON posts USING GIN (tsv);

CREATE OR REPLACE FUNCTION posts_tsv_update() RETURNS trigger AS $$
BEGIN
    NEW.tsv := to_tsvector('simple', COALESCE(NEW.message, ''));
    RETURN NEW;
END
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS posts_tsv_trg ON posts;
CREATE TRIGGER posts_tsv_trg BEFORE INSERT OR UPDATE ON posts
    FOR EACH ROW EXECUTE FUNCTION posts_tsv_update();

CREATE TABLE IF NOT EXISTS sessions (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token           TEXT UNIQUE NOT NULL,
    device_id       TEXT NOT NULL DEFAULT '',
    expires_at      BIGINT NOT NULL,
    create_at       BIGINT NOT NULL
);

ALTER TABLE posts ADD COLUMN IF NOT EXISTS file_ids JSONB NOT NULL DEFAULT '[]'::jsonb;

CREATE TABLE IF NOT EXISTS file_infos (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    post_id         TEXT REFERENCES posts(id) ON DELETE SET NULL,
    channel_id      TEXT REFERENCES channels(id) ON DELETE SET NULL,
    path            TEXT NOT NULL,
    name            TEXT NOT NULL,
    extension       TEXT NOT NULL DEFAULT '',
    size            BIGINT NOT NULL,
    mime_type       TEXT NOT NULL DEFAULT '',
    create_at       BIGINT NOT NULL,
    update_at       BIGINT NOT NULL,
    delete_at       BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS file_infos_post_idx ON file_infos (post_id);
CREATE INDEX IF NOT EXISTS file_infos_creator_idx ON file_infos (user_id);

CREATE TABLE IF NOT EXISTS reactions (
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    post_id         TEXT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    emoji_name      TEXT NOT NULL,
    create_at       BIGINT NOT NULL,
    PRIMARY KEY (user_id, post_id, emoji_name)
);

CREATE INDEX IF NOT EXISTS reactions_post_idx ON reactions (post_id);

CREATE TABLE IF NOT EXISTS user_statuses (
    user_id         TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    status          TEXT NOT NULL DEFAULT 'offline',
    manual          BOOLEAN NOT NULL DEFAULT FALSE,
    last_activity_at BIGINT NOT NULL DEFAULT 0,
    update_at       BIGINT NOT NULL
);

ALTER TABLE posts ADD COLUMN IF NOT EXISTS is_pinned BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE channels ADD COLUMN IF NOT EXISTS creator_id TEXT;

CREATE INDEX IF NOT EXISTS posts_pinned_idx ON posts (channel_id) WHERE is_pinned = TRUE;

CREATE TABLE IF NOT EXISTS plugins (
    id              TEXT PRIMARY KEY,
    version         TEXT NOT NULL,
    state           TEXT NOT NULL DEFAULT 'installed',
    manifest        JSONB NOT NULL,
    create_at       BIGINT NOT NULL,
    update_at       BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_logs (
    id              BIGSERIAL PRIMARY KEY,
    actor_id        TEXT,
    action          TEXT NOT NULL,
    target          TEXT,
    payload         JSONB,
    create_at       BIGINT NOT NULL
);

-- Phase 11: server-side unread + mention counters. Keeping these on
-- channel_members avoids a per-post scan on reconnect; the counters are
-- bumped inline with post creation and zeroed on MarkViewed.
ALTER TABLE channel_members ADD COLUMN IF NOT EXISTS msg_count     BIGINT NOT NULL DEFAULT 0;
ALTER TABLE channel_members ADD COLUMN IF NOT EXISTS mention_count BIGINT NOT NULL DEFAULT 0;

-- Phase 12: bots, personal access tokens, incoming/outgoing webhooks.
-- A bot is a row in `users` with is_bot=true and an empty password_hash;
-- auth.Login refuses bot logins so the only way to act as a bot is with
-- a personal access token (sha256 hashed, shown plain once at creation).
ALTER TABLE users ADD COLUMN IF NOT EXISTS is_bot          BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE users ADD COLUMN IF NOT EXISTS bot_owner_id   TEXT REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE users ADD COLUMN IF NOT EXISTS bot_description TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS personal_access_tokens (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash      TEXT UNIQUE NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    create_at       BIGINT NOT NULL,
    last_used_at    BIGINT NOT NULL DEFAULT 0,
    revoked_at      BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS pat_user_idx ON personal_access_tokens (user_id);

-- Incoming webhook: `id` doubles as the URL slug; clients POST to
-- /hooks/{id} and a post is created in `channel_id` as `creator_id`.
CREATE TABLE IF NOT EXISTS incoming_webhooks (
    id              TEXT PRIMARY KEY,
    creator_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel_id      TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    team_id         TEXT REFERENCES teams(id) ON DELETE CASCADE,
    display_name    TEXT NOT NULL DEFAULT '',
    username        TEXT NOT NULL DEFAULT '',
    icon_url        TEXT NOT NULL DEFAULT '',
    channel_locked  BOOLEAN NOT NULL DEFAULT TRUE,
    create_at       BIGINT NOT NULL,
    update_at       BIGINT NOT NULL,
    delete_at       BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS incoming_creator_idx ON incoming_webhooks (creator_id);

-- Outgoing webhook: server matches `trigger_words` (JSONB array of strings)
-- against fresh posts in `channel_id` (or any channel in `team_id` when
-- channel_id is empty) and POSTs to each callback URL.
CREATE TABLE IF NOT EXISTS outgoing_webhooks (
    id              TEXT PRIMARY KEY,
    token           TEXT UNIQUE NOT NULL,
    creator_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    team_id         TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    channel_id      TEXT REFERENCES channels(id) ON DELETE CASCADE,
    trigger_words   JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- 0 = exact-match-on-first-word, 1 = anywhere
    trigger_when    SMALLINT NOT NULL DEFAULT 0,
    callback_urls   JSONB NOT NULL DEFAULT '[]'::jsonb,
    display_name    TEXT NOT NULL DEFAULT '',
    content_type    TEXT NOT NULL DEFAULT 'application/json',
    create_at       BIGINT NOT NULL,
    update_at       BIGINT NOT NULL,
    delete_at       BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS outgoing_team_idx     ON outgoing_webhooks (team_id) WHERE delete_at = 0;
CREATE INDEX IF NOT EXISTS outgoing_channel_idx  ON outgoing_webhooks (channel_id) WHERE delete_at = 0 AND channel_id IS NOT NULL;

-- Phase 13: custom emoji + image thumbnails.
-- A custom emoji is a (name → file_infos row) mapping: the actual image
-- lives in the existing file storage so we get the upload/download plumbing
-- for free. `name` is lowercased, [a-z0-9_-], 1..40 chars.
CREATE TABLE IF NOT EXISTS emojis (
    id              TEXT PRIMARY KEY,
    name            TEXT UNIQUE NOT NULL,
    creator_id      TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    file_id         TEXT NOT NULL REFERENCES file_infos(id) ON DELETE RESTRICT,
    create_at       BIGINT NOT NULL,
    delete_at       BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS emojis_live_idx ON emojis (name) WHERE delete_at = 0;

-- Thumbnail metadata: generated async after upload. Empty path means the
-- image hasn't been processed yet (or isn't an image); clients fall back
-- to the full-size file in that case.
ALTER TABLE file_infos ADD COLUMN IF NOT EXISTS thumbnail_path TEXT NOT NULL DEFAULT '';
ALTER TABLE file_infos ADD COLUMN IF NOT EXISTS width          INTEGER NOT NULL DEFAULT 0;
ALTER TABLE file_infos ADD COLUMN IF NOT EXISTS height         INTEGER NOT NULL DEFAULT 0;

-- Phase 14: OAuth / SSO identity linkage. A single user can hold multiple
-- provider identities (Google + GitHub pointing at the same internal user)
-- but a (provider, subject) tuple is unique. Password users simply never
-- have a row here. We don't store the access/refresh tokens: login-only.
CREATE TABLE IF NOT EXISTS user_identities (
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider   TEXT NOT NULL,
    subject    TEXT NOT NULL,
    email      TEXT NOT NULL,
    create_at  BIGINT NOT NULL,
    PRIMARY KEY (provider, subject)
);

CREATE INDEX IF NOT EXISTS user_identities_user_idx ON user_identities (user_id);

-- Phase 15: profile picture. Stores either an external URL (populated by
-- OAuth sign-up from the provider's user info) or a server-relative path
-- into our own /api/v4/users/{id}/image endpoint (populated by self
-- upload). Empty string means "use initial tile fallback in UI".
ALTER TABLE users ADD COLUMN IF NOT EXISTS picture TEXT NOT NULL DEFAULT '';

-- Phase 16: team invite tokens. Admin issues a shareable URL; the invitee
-- opens it, signs up, and auto-joins the team. `max_uses = 0` means the
-- token can be reused until expires_at fires (useful for team-wide links);
-- any positive value is decremented through use_count. `revoked_at > 0`
-- hard-disables the token regardless of remaining uses.
CREATE TABLE IF NOT EXISTS invite_tokens (
    id              TEXT PRIMARY KEY,
    team_id         TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    created_by      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    max_uses        INT NOT NULL DEFAULT 1,
    use_count       INT NOT NULL DEFAULT 0,
    expires_at      BIGINT NOT NULL,
    revoked_at      BIGINT NOT NULL DEFAULT 0,
    create_at       BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS invite_tokens_team_idx ON invite_tokens(team_id) WHERE revoked_at = 0;

-- Phase 17: email digest preferences. JSONB holds
--   { "digest_enabled": bool (default true, opt-out),
--     "last_digest_at": int64  (ms epoch of most recent send) }
-- Kept on `users` rather than a side table because it's 1:1 and the digest
-- worker scans users anyway. `?` operator checks a key exists; `->>`
-- returns text — we compare text against 'false' to honour opt-outs.
ALTER TABLE users ADD COLUMN IF NOT EXISTS email_prefs JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Phase 18: personal saved-post bookmarks. One row per (user, post); the
-- partial index on create_at keeps the "my saved posts" list paginated
-- cheaply. FK cascades so deleting a user or a post cleans up silently.
CREATE TABLE IF NOT EXISTS saved_posts (
    user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    post_id   TEXT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    create_at BIGINT NOT NULL,
    PRIMARY KEY (user_id, post_id)
);

CREATE INDEX IF NOT EXISTS saved_posts_user_idx ON saved_posts (user_id, create_at DESC);

-- Phase 18: server-side OpenGraph previews. Array of
--   { url, title, description, image_url, fetched_at }
-- Populated asynchronously after a post is created; the server re-broadcasts
-- a post_edited event once the previews are attached so clients render
-- cards without a page reload. Empty array means "no links in message".
ALTER TABLE posts ADD COLUMN IF NOT EXISTS link_metadata JSONB NOT NULL DEFAULT '[]'::jsonb;

-- Phase 19: scheduled messages. A pending row has sent_at=0; the scheduled
-- worker claims due rows by flipping sent_at to -1 (in-progress), posts via
-- posts.Service.Create, then stamps sent_at = now(). error_text captures the
-- last failure so the UI can surface a retry affordance. file_ids/props use
-- the same JSONB encoding as posts.
CREATE TABLE IF NOT EXISTS scheduled_posts (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel_id  TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    root_id     TEXT NOT NULL DEFAULT '',
    message     TEXT NOT NULL,
    file_ids    JSONB NOT NULL DEFAULT '[]'::jsonb,
    props       JSONB NOT NULL DEFAULT '{}'::jsonb,
    send_at     BIGINT NOT NULL,
    create_at   BIGINT NOT NULL,
    sent_at     BIGINT NOT NULL DEFAULT 0,
    error_text  TEXT NOT NULL DEFAULT ''
);

-- Partial index powers the worker's "due now" claim query cheaply. Only
-- pending rows (sent_at=0) are ever scanned.
CREATE INDEX IF NOT EXISTS scheduled_posts_due_idx ON scheduled_posts (send_at) WHERE sent_at = 0;
CREATE INDEX IF NOT EXISTS scheduled_posts_user_idx ON scheduled_posts (user_id, send_at);

-- Phase 19: post reminders. "Remind me in 1h about this post". The reminders
-- worker claims due rows, broadcasts a reminder_fired WS event scoped to the
-- owning user, then stamps delivered_at. post_id cascades so if the source
-- post disappears the reminder goes with it.
CREATE TABLE IF NOT EXISTS post_reminders (
    id             TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    post_id        TEXT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    remind_at      BIGINT NOT NULL,
    create_at      BIGINT NOT NULL,
    delivered_at   BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS post_reminders_due_idx ON post_reminders (remind_at) WHERE delivered_at = 0;
CREATE INDEX IF NOT EXISTS post_reminders_user_idx ON post_reminders (user_id, remind_at);
