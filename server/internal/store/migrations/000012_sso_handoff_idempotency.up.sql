-- A completed browser handoff remains recoverable for a very short window so
-- a lost HTTP response does not strand the user after the session transaction
-- committed. The browser binding is still required and only the server-side
-- session row is referenced; no additional credential is persisted here.
ALTER TABLE login_handoffs
    ADD COLUMN session_id TEXT REFERENCES sessions(id) ON DELETE CASCADE,
    ADD COLUMN exchanged_at BIGINT;

ALTER TABLE login_handoffs
    ADD CONSTRAINT login_handoffs_completion_pair
    CHECK ((session_id IS NULL) = (exchanged_at IS NULL));

CREATE INDEX login_handoffs_session_idx
    ON login_handoffs (session_id)
    WHERE session_id IS NOT NULL;
