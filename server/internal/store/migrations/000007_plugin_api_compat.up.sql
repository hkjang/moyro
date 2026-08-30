-- Mattermost plugin bots are owned by a plugin manifest id, not by a user.
-- Keep this separate from bot_owner_id (the human administrator owner used by
-- Moyro's native bot APIs and protected by a users(id) foreign key).
ALTER TABLE users ADD COLUMN IF NOT EXISTS plugin_owner_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS users_plugin_owner_idx
    ON users (plugin_owner_id, username)
    WHERE plugin_owner_id <> '' AND COALESCE(is_bot, FALSE) = TRUE;
