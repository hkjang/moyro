-- Guest status was previously derived by running a regular expression over the
-- Mattermost-compatible whitespace-separated `users.roles` string on every row
-- of every authorization query. That made the predicate non-indexable and put a
-- per-row regex on the WebSocket fan-out, session authentication, and channel
-- statistics paths.
--
-- Project the same role test into a stored generated column so the value is
-- computed once per write instead of once per read. The expression is a literal
-- copy of the predicate the Go services used, so the projection cannot disagree
-- with the role string it is derived from.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS is_guest BOOLEAN
        GENERATED ALWAYS AS (
            'system_guest' = ANY (regexp_split_to_array(BTRIM(roles), '\s+'))
        ) STORED;

-- Fan-out and directory queries filter to live non-guest accounts far more often
-- than they look for guests. A partial index over the small guest population
-- keeps guest-expiry checks cheap without carrying an index entry per user.
CREATE INDEX IF NOT EXISTS users_guest_expiry_idx
    ON users (guest_expires_at)
    WHERE is_guest AND delete_at = 0;

-- The inverted WebSocket audience query resolves a channel's recipients from
-- channel_members and then confirms each candidate is a live account. The
-- baseline users primary key already serves the lookup; this partial index
-- serves the far more common "is this a live, non-guest account" filter without
-- touching the heap.
CREATE INDEX IF NOT EXISTS users_active_non_guest_idx
    ON users (id)
    WHERE delete_at = 0 AND NOT is_guest;
