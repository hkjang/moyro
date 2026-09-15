-- Event notification mail sent through the company SMTP relay. One row per
-- attempt to reach one address: when, for which event, to whom, with what
-- subject, and whether it worked. The body is deliberately absent — the log
-- exists so an administrator can answer "did it leave the building?", and a
-- log that also carried every message would itself be a leak.
CREATE TABLE IF NOT EXISTS mail_deliveries (
    id            TEXT   PRIMARY KEY,
    event         TEXT   NOT NULL,
    recipient_id  TEXT   NOT NULL DEFAULT '',
    recipient     TEXT   NOT NULL,
    subject       TEXT   NOT NULL,
    actor_id      TEXT   NOT NULL DEFAULT '',
    resource_type TEXT   NOT NULL DEFAULT '',
    resource_id   TEXT   NOT NULL DEFAULT '',
    status        TEXT   NOT NULL CHECK (status IN ('queued', 'sent', 'failed')),
    attempts      INT    NOT NULL DEFAULT 0,
    error_message TEXT   NOT NULL DEFAULT '',
    create_at     BIGINT NOT NULL,
    update_at     BIGINT NOT NULL
);

-- The administrator's view is newest-first, optionally by status.
CREATE INDEX IF NOT EXISTS mail_deliveries_create_at_idx ON mail_deliveries (create_at DESC, id);
CREATE INDEX IF NOT EXISTS mail_deliveries_status_create_at_idx ON mail_deliveries (status, create_at DESC);
