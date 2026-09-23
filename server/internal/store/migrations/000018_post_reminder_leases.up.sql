-- Bounded leases for reminder dispatch. delivered_at=-1 marked an in-flight
-- claim that had no release path: every reader of post_reminders matches
-- delivered_at=0, so a worker that stopped between the claim and the delivery
-- stamp left the row unreachable forever. The reminder never fired again and
-- its owner could neither list nor cancel it. claimed_at bounds the claim so an
-- abandoned one returns to the due queue.

ALTER TABLE post_reminders
    ADD COLUMN claimed_at BIGINT NOT NULL DEFAULT 0;

-- A row already in flight at upgrade time gets a full lease measured from
-- migration time rather than an immediate replay, matching the recovery rule
-- 000003 applied to interrupted scheduled posts.
UPDATE post_reminders
SET claimed_at = (EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::BIGINT
WHERE delivered_at = -1;

ALTER TABLE post_reminders
    ADD CONSTRAINT post_reminders_claimed_at_check
        CHECK (claimed_at >= 0) NOT VALID;

ALTER TABLE post_reminders VALIDATE CONSTRAINT post_reminders_claimed_at_check;

CREATE INDEX post_reminders_expired_lease_idx
    ON post_reminders (claimed_at, id)
    WHERE delivered_at = -1;
