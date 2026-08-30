-- Flow summary reads every active channel membership for one user. The
-- baseline primary key starts with channel_id, so it cannot efficiently serve
-- that user-scoped access path in large installations.
CREATE INDEX IF NOT EXISTS channel_members_user_channel_idx
    ON channel_members (user_id, channel_id);
