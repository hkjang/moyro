-- Durable scheduled-post leases and post-level idempotency. The legacy
-- sent_at/error_text columns remain authoritative compatibility fields for
-- older API clients while new workers coordinate through explicit state.

ALTER TABLE scheduled_posts
    ADD COLUMN status TEXT NOT NULL DEFAULT 'pending',
    ADD COLUMN claimed_at BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN lease_until BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN claim_token TEXT NOT NULL DEFAULT '',
    ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN next_attempt_at BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN last_error_code TEXT NOT NULL DEFAULT '',
    ADD COLUMN last_error_text TEXT NOT NULL DEFAULT '',
    ADD COLUMN result_post_id TEXT REFERENCES posts(id) ON DELETE SET NULL;

-- A v0.1 row with sent_at=-1 may represent work interrupted immediately before
-- the coordinated upgrade. Give it a full recovery lease from migration time
-- rather than replaying it immediately. This does not make mixed old/new worker
-- execution safe. Legacy successes have no durable result post id, but remain
-- succeeded.
WITH migration_clock AS (
    SELECT (EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::BIGINT AS now_ms
)
UPDATE scheduled_posts AS sp
SET status = CASE
        WHEN sp.sent_at > 0 THEN 'succeeded'
        WHEN sp.sent_at < 0 THEN 'processing'
        WHEN sp.error_text <> '' THEN 'retry'
        ELSE 'pending'
    END,
    claimed_at = CASE WHEN sp.sent_at < 0 THEN migration_clock.now_ms ELSE 0 END,
    lease_until = CASE WHEN sp.sent_at < 0 THEN migration_clock.now_ms + 300000 ELSE 0 END,
    claim_token = CASE WHEN sp.sent_at < 0 THEN 'legacy-' || sp.id ELSE '' END,
    attempt_count = CASE
        WHEN sp.sent_at = 0 AND sp.error_text = '' THEN 0
        ELSE 1
    END,
    next_attempt_at = CASE
        WHEN sp.sent_at > 0 THEN 0
        WHEN sp.sent_at < 0 THEN migration_clock.now_ms + 300000
        WHEN sp.error_text <> '' THEN GREATEST(sp.send_at, migration_clock.now_ms)
        ELSE sp.send_at
    END,
    last_error_code = CASE WHEN sp.error_text <> '' THEN 'legacy_error' ELSE '' END,
    last_error_text = sp.error_text
FROM migration_clock;

ALTER TABLE scheduled_posts
    ADD CONSTRAINT scheduled_posts_status_check
        CHECK (status IN ('pending', 'processing', 'succeeded', 'retry', 'dead', 'cancelled')) NOT VALID,
    ADD CONSTRAINT scheduled_posts_attempt_count_check
        CHECK (attempt_count >= 0) NOT VALID;

ALTER TABLE scheduled_posts VALIDATE CONSTRAINT scheduled_posts_status_check;
ALTER TABLE scheduled_posts VALIDATE CONSTRAINT scheduled_posts_attempt_count_check;

CREATE INDEX scheduled_posts_claim_due_idx
    ON scheduled_posts (next_attempt_at, send_at, id)
    WHERE status IN ('pending', 'retry');

CREATE INDEX scheduled_posts_expired_lease_idx
    ON scheduled_posts (lease_until, id)
    WHERE status = 'processing';

ALTER TABLE posts ADD COLUMN scheduled_post_id TEXT;

CREATE UNIQUE INDEX posts_scheduled_post_unique_idx
    ON posts (scheduled_post_id)
    WHERE scheduled_post_id IS NOT NULL;
