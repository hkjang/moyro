-- Durable delivery queue for outbound integrations. Callback configuration is
-- snapshotted, but webhook credentials are deliberately not copied into the
-- payload. Workers resolve the current credential immediately before delivery.

CREATE TABLE integration_deliveries (
    id                  TEXT PRIMARY KEY,
    event_id            TEXT NOT NULL,
    integration_type    TEXT NOT NULL DEFAULT 'outgoing_webhook',
    integration_id      TEXT NOT NULL,
    callback_url        TEXT NOT NULL,
    content_type        TEXT NOT NULL DEFAULT 'application/json',
    payload             JSONB NOT NULL,
    status              TEXT NOT NULL DEFAULT 'pending',
    attempt_count       INTEGER NOT NULL DEFAULT 0,
    next_attempt_at     BIGINT NOT NULL,
    claimed_at          BIGINT NOT NULL DEFAULT 0,
    lease_until         BIGINT NOT NULL DEFAULT 0,
    claim_token         TEXT NOT NULL DEFAULT '',
    last_error_code     TEXT NOT NULL DEFAULT '',
    last_error_text     TEXT NOT NULL DEFAULT '',
    response_status     INTEGER NOT NULL DEFAULT 0,
    create_at           BIGINT NOT NULL,
    update_at           BIGINT NOT NULL,
    succeeded_at        BIGINT NOT NULL DEFAULT 0,
    dead_at             BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT integration_deliveries_type_check
        CHECK (integration_type IN ('outgoing_webhook')),
    CONSTRAINT integration_deliveries_status_check
        CHECK (status IN ('pending', 'processing', 'retry', 'succeeded', 'dead')),
    CONSTRAINT integration_deliveries_attempt_count_check
        CHECK (attempt_count >= 0),
    CONSTRAINT integration_deliveries_payload_size_check
        CHECK (octet_length(payload::text) <= 262144),
    CONSTRAINT integration_deliveries_event_target_unique
        UNIQUE (event_id, integration_id, callback_url)
);

CREATE INDEX integration_deliveries_claim_due_idx
    ON integration_deliveries (next_attempt_at, create_at, id)
    WHERE status IN ('pending', 'retry');

CREATE INDEX integration_deliveries_expired_lease_idx
    ON integration_deliveries (lease_until, id)
    WHERE status = 'processing';

CREATE INDEX integration_deliveries_outstanding_idx
    ON integration_deliveries (status, create_at)
    WHERE status IN ('pending', 'processing', 'retry');

CREATE TABLE integration_delivery_attempts (
    id                  BIGSERIAL PRIMARY KEY,
    delivery_id         TEXT NOT NULL REFERENCES integration_deliveries(id) ON DELETE CASCADE,
    attempt_number      INTEGER NOT NULL CHECK (attempt_number > 0),
    claim_token         TEXT NOT NULL,
    started_at          BIGINT NOT NULL,
    completed_at        BIGINT NOT NULL,
    duration_ms         BIGINT NOT NULL CHECK (duration_ms >= 0),
    response_status     INTEGER NOT NULL DEFAULT 0,
    error_code          TEXT NOT NULL DEFAULT '',
    error_text          TEXT NOT NULL DEFAULT '',
    succeeded           BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE (delivery_id, attempt_number)
);

CREATE INDEX integration_delivery_attempts_delivery_idx
    ON integration_delivery_attempts (delivery_id, attempt_number DESC);
