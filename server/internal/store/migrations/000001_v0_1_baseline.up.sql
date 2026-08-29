-- moyro:irreversible
-- Moyro v0.1 baseline schema. Mattermost-compatible field names where feasible.

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

-- Phase 21: Mattermost-shaped preferences. Each row is a (user, category, name)
-- triplet whose value is an opaque string the client interprets. Common Mattermost
-- categories: "display_settings" (theme, message_display), "sidebar_settings",
-- "favorite_channel" (name=channel_id, value="true"), "direct_channel_show",
-- "advanced_settings", "tutorial_step". Keeping value as TEXT (rather than JSONB)
-- matches the v4 OpenAPI contract so official clients deserialize without coaxing.
CREATE TABLE IF NOT EXISTS preferences (
    user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    category  TEXT NOT NULL,
    name      TEXT NOT NULL,
    value     TEXT NOT NULL DEFAULT '',
    update_at BIGINT NOT NULL,
    PRIMARY KEY (user_id, category, name)
);

CREATE INDEX IF NOT EXISTS preferences_user_category_idx ON preferences (user_id, category);

-- Phase 22: Mattermost-shaped sidebar categories. Each user has 1..N categories
-- per team that drive the channel sidebar's grouping/ordering. The four canonical
-- type values are favorites|channels|direct_messages|custom — favorites/channels/
-- direct_messages are auto-created on first list (one of each per team). `custom`
-- categories are user-defined and may have any display_name. `sort_order` is the
-- position of the category itself within the sidebar; `sorting` is the in-category
-- sort mode ("alpha"|"recent"|"manual"). Channel membership is a separate join
-- table because a channel may live in only one category at a time per user/team.
CREATE TABLE IF NOT EXISTS sidebar_categories (
    id             TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    team_id        TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    type           TEXT NOT NULL,
    display_name   TEXT NOT NULL DEFAULT '',
    sort_order     INTEGER NOT NULL DEFAULT 0,
    sorting        TEXT NOT NULL DEFAULT 'alpha',
    muted          BOOLEAN NOT NULL DEFAULT FALSE,
    collapsed      BOOLEAN NOT NULL DEFAULT FALSE,
    create_at      BIGINT NOT NULL,
    update_at      BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS sidebar_categories_user_team_idx ON sidebar_categories (user_id, team_id, sort_order);

CREATE TABLE IF NOT EXISTS sidebar_category_channels (
    category_id   TEXT NOT NULL REFERENCES sidebar_categories(id) ON DELETE CASCADE,
    channel_id    TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    sort_order    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (category_id, channel_id)
);

CREATE INDEX IF NOT EXISTS sidebar_category_channels_category_idx ON sidebar_category_channels (category_id, sort_order);

-- Phase 22: user-level notify_props (separate from per-channel notify_props on
-- channel_members). JSONB encoding mirrors Mattermost's contract: a free-form map
-- of strings (e.g. "email": "true"|"false", "desktop": "all"|"mention"|"none",
-- "push": "...", "first_name": "true"|"false"). Stored as text values for byte-
-- for-byte API compatibility.
ALTER TABLE users ADD COLUMN IF NOT EXISTS notify_props JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Phase 23: profile fields surfaced via PUT /users/{id} and PUT /users/{id}/patch.
-- Mattermost's user object has these as first-class fields. We treat all four as
-- TEXT (never NULL) so SELECT ... COALESCE goes away. Empty string ⇒ "not set".
ALTER TABLE users ADD COLUMN IF NOT EXISTS first_name TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS last_name  TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS nickname   TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS position   TEXT NOT NULL DEFAULT '';

-- Phase 23: custom slash commands. Mirrors Mattermost's `commands` table layout
-- but trimmed to the fields we can faithfully implement today. `trigger` is the
-- token after the slash (e.g. "weather"); unique per (team, trigger) so two teams
-- can register the same name independently. `token` is a regenerable random
-- string sent to the callback URL so receivers can verify. `auto_complete*` drive
-- the typeahead surface; if auto_complete=false the command stays hidden.
CREATE TABLE IF NOT EXISTS commands (
    id                  TEXT PRIMARY KEY,
    team_id             TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    creator_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    trigger_word        TEXT NOT NULL,
    method              TEXT NOT NULL DEFAULT 'P', -- 'P' POST | 'G' GET
    url                 TEXT NOT NULL DEFAULT '',
    username            TEXT NOT NULL DEFAULT '',
    icon_url            TEXT NOT NULL DEFAULT '',
    auto_complete       BOOLEAN NOT NULL DEFAULT TRUE,
    auto_complete_desc  TEXT NOT NULL DEFAULT '',
    auto_complete_hint  TEXT NOT NULL DEFAULT '',
    display_name        TEXT NOT NULL DEFAULT '',
    description         TEXT NOT NULL DEFAULT '',
    token               TEXT NOT NULL,
    create_at           BIGINT NOT NULL,
    update_at           BIGINT NOT NULL,
    delete_at           BIGINT NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS commands_team_trigger_idx
    ON commands (team_id, LOWER(trigger_word)) WHERE delete_at = 0;
CREATE INDEX IF NOT EXISTS commands_creator_idx ON commands (creator_id);

-- Phase 24: thread membership / read state. The official Mattermost API exposes
-- per-(user, team, root) following + last-read tracking. Following is implicit
-- on reply (you posted in the thread) and explicit when the user clicks
-- "Following"; read tracking lets the UI dim a thread once the user has caught
-- up. team_id is denormalised so the global "mark all team threads read" query
-- doesn't have to JOIN posts→channels→teams on every fan-out.
CREATE TABLE IF NOT EXISTS thread_memberships (
    user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    team_id          TEXT NOT NULL,
    root_id          TEXT NOT NULL,
    last_viewed_at   BIGINT NOT NULL DEFAULT 0,
    last_updated_at  BIGINT NOT NULL DEFAULT 0,
    unread_mentions  INTEGER NOT NULL DEFAULT 0,
    unread_replies   INTEGER NOT NULL DEFAULT 0,
    following        BOOLEAN NOT NULL DEFAULT TRUE,
    PRIMARY KEY (user_id, root_id)
);
CREATE INDEX IF NOT EXISTS thread_memberships_team_idx
    ON thread_memberships (user_id, team_id);

-- Phase 24: custom status. Mattermost stores `{emoji, text, expires_at, duration}`
-- as a single user-level JSONB blob. We piggyback on the existing user_statuses
-- row instead of growing a new table — there's already one row per user there
-- and the existing GetMany / Get path can hand the field out without an extra
-- read. Empty {} ⇒ no custom status set; client renders the plain Online/Away.
ALTER TABLE user_statuses ADD COLUMN IF NOT EXISTS custom_status JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Phase 24: device id for push notifications. Mattermost stores this on the
-- session row so a logged-in user can have multiple devices, each with its
-- own push token. We don't actually push (no APNS / FCM wiring yet) but the
-- column lets official mobile clients register without a 404. The PUT route
-- is fire-and-forget — write the token, never read it back.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS device_id TEXT NOT NULL DEFAULT '';

-- Phase 29: channel bookmarks. Mattermost lets users pin a small list of URL
-- or file links above the message stream so a channel-wide reference (a Jira
-- query, a runbook PDF, a Figma board) stays one click away. Bookmarks are
-- per-channel (NOT per-user — every member sees the same list) but the row
-- carries the creator id for audit. `link_url` and `file_id` are mutually
-- exclusive in practice but the schema keeps both columns optional so a
-- future "rich" bookmark variant can grow without a migration.
CREATE TABLE IF NOT EXISTS channel_bookmarks (
    id              TEXT PRIMARY KEY,
    channel_id      TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    owner_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    display_name    TEXT NOT NULL DEFAULT '',
    sort_order      INTEGER NOT NULL DEFAULT 0,
    link_url        TEXT NOT NULL DEFAULT '',
    image_url       TEXT NOT NULL DEFAULT '',
    emoji           TEXT NOT NULL DEFAULT '',
    file_id         TEXT NOT NULL DEFAULT '',
    type            TEXT NOT NULL DEFAULT 'link',
    create_at       BIGINT NOT NULL,
    update_at       BIGINT NOT NULL,
    delete_at       BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS channel_bookmarks_channel_idx
    ON channel_bookmarks (channel_id, sort_order)
    WHERE delete_at = 0;

-- Phase 32: custom profile attributes. Mattermost's "extra fields on a user
-- profile" feature — admins define a global set of fields (text/select/url/
-- date/etc.), each user fills them in. Two tables: the field definitions
-- (global, admin-managed) and the per-user value rows. We treat values as
-- raw JSONB so a future field type addition (e.g. multi-select) round-trips
-- without a migration. Field rows are soft-deleted so historical values can
-- still resolve their field name.
CREATE TABLE IF NOT EXISTS custom_profile_fields (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    type        TEXT NOT NULL DEFAULT 'text',
    target_id   TEXT NOT NULL DEFAULT '',
    target_type TEXT NOT NULL DEFAULT 'user',
    attrs       JSONB NOT NULL DEFAULT '{}'::jsonb,
    sort_order  INTEGER NOT NULL DEFAULT 0,
    create_at   BIGINT NOT NULL,
    update_at   BIGINT NOT NULL,
    delete_at   BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS custom_profile_fields_active_idx
    ON custom_profile_fields (sort_order)
    WHERE delete_at = 0;

CREATE TABLE IF NOT EXISTS custom_profile_values (
    user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    field_id  TEXT NOT NULL REFERENCES custom_profile_fields(id) ON DELETE CASCADE,
    value     JSONB NOT NULL DEFAULT 'null'::jsonb,
    update_at BIGINT NOT NULL,
    PRIMARY KEY (user_id, field_id)
);
CREATE INDEX IF NOT EXISTS custom_profile_values_user_idx
    ON custom_profile_values (user_id);

-- Phase 33: real persistence for the Phase 28 stub features. Three tables:
--   post_acknowledgements — Mattermost's "ack a post" feature; one row per
--     (post, user) once that user has explicitly acknowledged. Cascade on
--     either parent purges the ack record automatically.
--   terms_of_service — admin-defined TOS body; only the newest non-deleted
--     row is "current". Soft-delete via delete_at so historical accept rows
--     can still resolve their TOS body for audit.
--   user_terms_of_service — per-user acceptance of a specific TOS revision.
--     PK on (user_id) so a user only has one accepted-TOS pointer at a time;
--     they re-accept by overwriting the pointer.
CREATE TABLE IF NOT EXISTS post_acknowledgements (
    post_id   TEXT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    ack_at    BIGINT NOT NULL,
    PRIMARY KEY (post_id, user_id)
);
CREATE INDEX IF NOT EXISTS post_acknowledgements_user_idx
    ON post_acknowledgements (user_id);

CREATE TABLE IF NOT EXISTS terms_of_service (
    id        TEXT PRIMARY KEY,
    user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    text      TEXT NOT NULL DEFAULT '',
    create_at BIGINT NOT NULL,
    delete_at BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS terms_of_service_active_idx
    ON terms_of_service (create_at DESC)
    WHERE delete_at = 0;

CREATE TABLE IF NOT EXISTS user_terms_of_service (
    user_id          TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    terms_of_service_id TEXT NOT NULL REFERENCES terms_of_service(id) ON DELETE CASCADE,
    create_at        BIGINT NOT NULL
);

-- Moyro foundation: one-time administrator bootstrap. The singleton marker is
-- written in the same transaction as user creation/promotion. It prevents a
-- stale BOOTSTRAP_ADMIN_PASSWORD from resetting credentials on every restart.
CREATE TABLE IF NOT EXISTS bootstrap_state (
    id              SMALLINT PRIMARY KEY CHECK (id = 1),
    admin_user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    completed_at    BIGINT NOT NULL
);

-- Administrator-managed runtime settings. A row is either clear JSON or an
-- authenticated ciphertext envelope, never both. HTTP read models expose only
-- secret_configured for encrypted rows.
CREATE TABLE IF NOT EXISTS system_settings (
    section             TEXT NOT NULL,
    setting_key         TEXT NOT NULL,
    value_json          JSONB,
    secret_ciphertext   BYTEA,
    secret_nonce        BYTEA,
    key_id              TEXT,
    revision            BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_by          TEXT REFERENCES users(id) ON DELETE SET NULL,
    update_at           BIGINT NOT NULL,
    PRIMARY KEY (section, setting_key),
    CHECK (
        (value_json IS NOT NULL AND secret_ciphertext IS NULL AND secret_nonce IS NULL AND key_id IS NULL)
        OR
        (value_json IS NULL AND secret_ciphertext IS NOT NULL AND secret_nonce IS NOT NULL AND key_id IS NOT NULL)
    )
);
CREATE INDEX IF NOT EXISTS system_settings_section_idx
    ON system_settings (section, setting_key);

-- Configurable RBAC definitions. Existing users/team_members/channel_members
-- keep their Mattermost-compatible whitespace-separated role assignments;
-- these tables make the permissions attached to each role durable and mutable.
CREATE TABLE IF NOT EXISTS permissions (
    name            TEXT PRIMARY KEY,
    description     TEXT NOT NULL DEFAULT '',
    resource_type   TEXT NOT NULL DEFAULT 'system',
    built_in        BOOLEAN NOT NULL DEFAULT TRUE
);

CREATE TABLE IF NOT EXISTS roles (
    id              TEXT PRIMARY KEY,
    name            TEXT UNIQUE NOT NULL,
    display_name    TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    scope_type      TEXT NOT NULL CHECK (scope_type IN ('system','team','channel')),
    built_in        BOOLEAN NOT NULL DEFAULT FALSE,
    revision        BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_by      TEXT REFERENCES users(id) ON DELETE SET NULL,
    create_at       BIGINT NOT NULL,
    update_at       BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS role_permissions (
    role_id          TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    permission_name  TEXT NOT NULL REFERENCES permissions(name) ON DELETE RESTRICT,
    PRIMARY KEY (role_id, permission_name)
);
CREATE INDEX IF NOT EXISTS role_permissions_permission_idx
    ON role_permissions (permission_name, role_id);

INSERT INTO permissions (name, description, resource_type, built_in) VALUES
    ('manage_system', 'Full system administration.', 'system', TRUE),
    ('manage_roles', 'Change role permission definitions.', 'system', TRUE),
    ('manage_jobs', 'Manage background jobs.', 'system', TRUE),
    ('read_jobs', 'Read background job state.', 'system', TRUE),
    ('manage_oauth', 'Manage OAuth applications.', 'system', TRUE),
    ('manage_system_wide_oauth', 'Manage system-wide OAuth providers.', 'system', TRUE),
    ('manage_plugins', 'Manage installed plugins.', 'system', TRUE),
    ('manage_settings', 'Read and change service settings.', 'system', TRUE),
    ('manage_oidc', 'Configure and test OIDC providers.', 'system', TRUE),
    ('manage_ai', 'Configure and test AI providers.', 'system', TRUE),
    ('use_ai', 'Use enabled AI providers.', 'system', TRUE),
    ('manage_api_keys', 'Manage API keys for all users.', 'system', TRUE),
    ('manage_own_api_keys', 'Manage the caller''s API keys.', 'system', TRUE),
    ('manage_key_permissions', 'Change API-key scope policy.', 'system', TRUE),
    ('manage_approval_policies', 'Manage review and approval policies.', 'system', TRUE),
    ('request_approval', 'Submit an approval request.', 'team', TRUE),
    ('review_approval', 'Approve or reject team requests.', 'team', TRUE),
    ('mcp_read', 'Read permitted Moyro resources over MCP.', 'system', TRUE),
    ('mcp_write', 'Invoke permitted Moyro mutations over MCP.', 'system', TRUE),
    ('create_public_channel', 'Create a public channel.', 'team', TRUE),
    ('create_private_channel', 'Create a private channel.', 'team', TRUE),
    ('create_post', 'Create a post.', 'channel', TRUE),
    ('use_channel_mentions', 'Use channel-wide mentions.', 'channel', TRUE),
    ('manage_slash_commands', 'Manage slash commands.', 'team', TRUE),
    ('manage_team', 'Manage a team.', 'team', TRUE),
    ('manage_public_channel_properties', 'Manage public channel properties.', 'channel', TRUE),
    ('manage_private_channel_properties', 'Manage private channel properties.', 'channel', TRUE),
    ('manage_channel', 'Manage channel membership and properties.', 'channel', TRUE)
ON CONFLICT (name) DO NOTHING;

INSERT INTO roles
    (id, name, display_name, description, scope_type, built_in, revision, create_at, update_at)
VALUES
    ('system_admin', 'system_admin', 'System Admin', 'Full system administration role.', 'system', TRUE, 1,
        (EXTRACT(EPOCH FROM clock_timestamp())*1000)::BIGINT, (EXTRACT(EPOCH FROM clock_timestamp())*1000)::BIGINT),
    ('system_user', 'system_user', 'System User', 'Default authenticated user role.', 'system', TRUE, 1,
        (EXTRACT(EPOCH FROM clock_timestamp())*1000)::BIGINT, (EXTRACT(EPOCH FROM clock_timestamp())*1000)::BIGINT),
    ('system_guest', 'system_guest', 'System Guest', 'Restricted authenticated guest role.', 'system', TRUE, 1,
        (EXTRACT(EPOCH FROM clock_timestamp())*1000)::BIGINT, (EXTRACT(EPOCH FROM clock_timestamp())*1000)::BIGINT),
    ('team_admin', 'team_admin', 'Team Admin', 'Team-scoped administration role.', 'team', TRUE, 1,
        (EXTRACT(EPOCH FROM clock_timestamp())*1000)::BIGINT, (EXTRACT(EPOCH FROM clock_timestamp())*1000)::BIGINT),
    ('team_user', 'team_user', 'Team User', 'Default team membership role.', 'team', TRUE, 1,
        (EXTRACT(EPOCH FROM clock_timestamp())*1000)::BIGINT, (EXTRACT(EPOCH FROM clock_timestamp())*1000)::BIGINT),
    ('channel_admin', 'channel_admin', 'Channel Admin', 'Channel-scoped administration role.', 'channel', TRUE, 1,
        (EXTRACT(EPOCH FROM clock_timestamp())*1000)::BIGINT, (EXTRACT(EPOCH FROM clock_timestamp())*1000)::BIGINT),
    ('channel_user', 'channel_user', 'Channel User', 'Default channel membership role.', 'channel', TRUE, 1,
        (EXTRACT(EPOCH FROM clock_timestamp())*1000)::BIGINT, (EXTRACT(EPOCH FROM clock_timestamp())*1000)::BIGINT)
ON CONFLICT (id) DO NOTHING;

-- Only seed role grants while a role is at its untouched revision. Once an
-- administrator changes a built-in role (revision > 1), restart/migration must
-- not silently restore permissions that were deliberately removed.
INSERT INTO role_permissions (role_id, permission_name)
SELECT seed.role_id, seed.permission_name
FROM (VALUES
    ('system_admin','manage_system'),
    ('system_admin','manage_roles'),
    ('system_admin','manage_jobs'),
    ('system_admin','read_jobs'),
    ('system_admin','manage_oauth'),
    ('system_admin','manage_system_wide_oauth'),
    ('system_admin','manage_plugins'),
    ('system_admin','manage_settings'),
    ('system_admin','manage_oidc'),
    ('system_admin','manage_ai'),
    ('system_admin','use_ai'),
    ('system_admin','manage_api_keys'),
    ('system_admin','manage_own_api_keys'),
    ('system_admin','manage_key_permissions'),
    ('system_admin','manage_approval_policies'),
    ('system_admin','request_approval'),
    ('system_admin','review_approval'),
    ('system_admin','mcp_read'),
    ('system_admin','mcp_write'),
    ('system_admin','create_public_channel'),
    ('system_admin','create_private_channel'),
    ('system_admin','create_post'),
    ('system_admin','use_channel_mentions'),
    ('system_admin','manage_slash_commands'),
    ('system_admin','manage_team'),
    ('system_admin','manage_public_channel_properties'),
    ('system_admin','manage_private_channel_properties'),
    ('system_admin','manage_channel'),
    ('system_user','manage_own_api_keys'),
    ('system_user','use_ai'),
    ('system_user','mcp_read'),
    ('system_user','mcp_write'),
    ('system_user','create_public_channel'),
    ('system_user','create_private_channel'),
    ('system_user','create_post'),
    ('system_user','use_channel_mentions'),
    ('system_user','manage_slash_commands'),
    ('system_user','request_approval'),
    ('system_guest','create_post'),
    ('system_guest','use_channel_mentions'),
    ('team_admin','manage_team'),
    ('team_admin','manage_public_channel_properties'),
    ('team_admin','manage_private_channel_properties'),
    ('team_admin','review_approval'),
    ('team_user','request_approval'),
    ('channel_admin','manage_channel')
) AS seed(role_id, permission_name)
JOIN roles r ON r.id=seed.role_id AND r.revision=1
ON CONFLICT (role_id, permission_name) DO NOTHING;

-- Scoped personal/service/MCP keys. key_prefix is display-only; secret_hash is
-- a domain-separated HMAC. Rotation creates a new row and puts the predecessor
-- in retiring state for a bounded overlap window.
CREATE TABLE IF NOT EXISTS api_keys (
    id                  TEXT PRIMARY KEY,
    owner_user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name                TEXT NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    key_prefix          TEXT NOT NULL,
    secret_hash         BYTEA UNIQUE NOT NULL,
    kind                TEXT NOT NULL CHECK (kind IN ('user','service','mcp')),
    status              TEXT NOT NULL CHECK (status IN ('active','retiring','revoked')),
    constraints         JSONB NOT NULL DEFAULT '{}'::jsonb,
    expires_at          BIGINT NOT NULL,
    valid_until         BIGINT NOT NULL DEFAULT 0,
    rotation_group_id   TEXT NOT NULL,
    version             INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    rotated_from_id     TEXT REFERENCES api_keys(id) ON DELETE SET NULL,
    created_by          TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    create_at           BIGINT NOT NULL,
    update_at           BIGINT NOT NULL,
    last_used_at        BIGINT NOT NULL DEFAULT 0,
    revoked_at          BIGINT NOT NULL DEFAULT 0,
    revision            BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0)
);
CREATE INDEX IF NOT EXISTS api_keys_owner_idx
    ON api_keys (owner_user_id, create_at DESC);
CREATE INDEX IF NOT EXISTS api_keys_rotation_idx
    ON api_keys (rotation_group_id, version);
CREATE INDEX IF NOT EXISTS api_keys_retiring_idx
    ON api_keys (valid_until) WHERE status='retiring' AND revoked_at=0;

CREATE TABLE IF NOT EXISTS api_key_permissions (
    api_key_id       TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    permission_name  TEXT NOT NULL REFERENCES permissions(name) ON DELETE RESTRICT,
    PRIMARY KEY (api_key_id, permission_name)
);
CREATE INDEX IF NOT EXISTS api_key_permissions_permission_idx
    ON api_key_permissions (permission_name, api_key_id);

-- Keycloak OIDC authorization transactions. Only a SHA-256 digest of state is
-- indexed; nonce, PKCE verifier and return path live inside an authenticated
-- ENCRYPTION_KEY envelope and each row is consumed exactly once.
CREATE TABLE IF NOT EXISTS oidc_auth_flows (
    state_hash  BYTEA PRIMARY KEY,
    key_id      TEXT NOT NULL,
    nonce       BYTEA NOT NULL,
    ciphertext  BYTEA NOT NULL,
    expires_at  BIGINT NOT NULL,
    create_at   BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS oidc_auth_flows_expiry_idx
    ON oidc_auth_flows (expires_at);

-- Per-user AI defaults. Provider credentials and system provider settings are
-- kept in system_settings; this table contains no secrets.
CREATE TABLE IF NOT EXISTS user_ai_preferences (
    user_id            TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    enabled            BOOLEAN NOT NULL DEFAULT TRUE,
    provider_id        TEXT,
    model              TEXT,
    max_output_tokens  BIGINT NOT NULL DEFAULT 8192 CHECK (max_output_tokens BETWEEN 1 AND 262144),
    temperature        DOUBLE PRECISION NOT NULL DEFAULT 0.7 CHECK (temperature BETWEEN 0 AND 2),
    update_at          BIGINT NOT NULL
);

-- Optional review/approval workflow. With no enabled matching policy Submit
-- returns direct execution and creates no request row.
CREATE TABLE IF NOT EXISTS approval_policies (
    id                     TEXT PRIMARY KEY,
    scope_type             TEXT NOT NULL CHECK (scope_type IN ('system','team')),
    scope_id               TEXT NOT NULL DEFAULT '',
    action_type            TEXT NOT NULL,
    enabled                BOOLEAN NOT NULL DEFAULT FALSE,
    reviewer_permission    TEXT NOT NULL REFERENCES permissions(name) ON DELETE RESTRICT,
    approvals_required     INTEGER NOT NULL DEFAULT 1 CHECK (approvals_required > 0),
    forbid_self_approval   BOOLEAN NOT NULL DEFAULT TRUE,
    expires_after_seconds  BIGINT NOT NULL DEFAULT 259200 CHECK (expires_after_seconds >= 0),
    config                 JSONB NOT NULL DEFAULT '{}'::jsonb,
    revision               BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_by             TEXT REFERENCES users(id) ON DELETE SET NULL,
    update_at              BIGINT NOT NULL,
    UNIQUE (scope_type, scope_id, action_type),
    CHECK ((scope_type='system' AND scope_id='') OR (scope_type='team' AND scope_id<>''))
);
CREATE INDEX IF NOT EXISTS approval_policies_match_idx
    ON approval_policies (action_type, scope_type, scope_id) WHERE enabled;

CREATE TABLE IF NOT EXISTS approval_requests (
    id               TEXT PRIMARY KEY,
    policy_id        TEXT NOT NULL REFERENCES approval_policies(id) ON DELETE RESTRICT,
    action_type      TEXT NOT NULL,
    requester_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    team_id          TEXT REFERENCES teams(id) ON DELETE SET NULL,
    resource_type    TEXT NOT NULL DEFAULT '',
    resource_id      TEXT NOT NULL DEFAULT '',
    payload          JSONB NOT NULL DEFAULT '{}'::jsonb,
    status           TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','expired','executed')),
    idempotency_key  TEXT,
    create_at        BIGINT NOT NULL,
    update_at        BIGINT NOT NULL,
    decided_at       BIGINT NOT NULL DEFAULT 0,
    executed_at      BIGINT NOT NULL DEFAULT 0,
    expires_at       BIGINT NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS approval_requests_idempotency_idx
    ON approval_requests (requester_id, action_type, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS approval_requests_requester_idx
    ON approval_requests (requester_id, create_at DESC);
CREATE INDEX IF NOT EXISTS approval_requests_team_status_idx
    ON approval_requests (team_id, status, create_at DESC);

CREATE TABLE IF NOT EXISTS approval_decisions (
    request_id   TEXT NOT NULL REFERENCES approval_requests(id) ON DELETE CASCADE,
    reviewer_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    decision     TEXT NOT NULL CHECK (decision IN ('approve','reject')),
    reason       TEXT NOT NULL DEFAULT '',
    create_at    BIGINT NOT NULL,
    PRIMARY KEY (request_id, reviewer_id)
);

CREATE TABLE IF NOT EXISTS workflow_outbox (
    id           TEXT PRIMARY KEY,
    request_id   TEXT UNIQUE NOT NULL REFERENCES approval_requests(id) ON DELETE CASCADE,
    action_type  TEXT NOT NULL,
    payload      JSONB NOT NULL DEFAULT '{}'::jsonb,
    status       TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','succeeded','failed')),
    attempts     INTEGER NOT NULL DEFAULT 0,
    available_at BIGINT NOT NULL,
    create_at    BIGINT NOT NULL,
    update_at    BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS workflow_outbox_pending_idx
    ON workflow_outbox (available_at, create_at) WHERE status='pending';

-- An approval retry must resolve to the original post instead of duplicating
-- the protected side effect.
CREATE UNIQUE INDEX IF NOT EXISTS posts_approval_request_unique_idx
    ON posts ((props->>'approval_request_id'))
    WHERE props ? 'approval_request_id';

-- A team lead is intentionally separate from team_admin; administrators may
-- change its permission set from the RBAC management API/UI.
INSERT INTO roles
    (id, name, display_name, description, scope_type, built_in, revision, create_at, update_at)
VALUES
    ('team_lead', 'team_lead', 'Team Lead', 'Reviews team workflow requests without full team administration.', 'team', TRUE, 1,
        (EXTRACT(EPOCH FROM clock_timestamp())*1000)::BIGINT, (EXTRACT(EPOCH FROM clock_timestamp())*1000)::BIGINT)
ON CONFLICT (id) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_name)
SELECT 'team_lead', 'review_approval'
FROM roles WHERE id='team_lead' AND revision=1
ON CONFLICT (role_id, permission_name) DO NOTHING;
