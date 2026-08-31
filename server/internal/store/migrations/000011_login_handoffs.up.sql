-- Browser SSO callbacks hand a short-lived opaque code to the web client.
-- Only its digest is persisted; exchanging it atomically deletes the row and
-- creates the ordinary Moyro session used by every authenticated API.
CREATE TABLE login_handoffs (
    code_hash     BYTEA PRIMARY KEY,
    binding_hash  BYTEA NOT NULL,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at    BIGINT NOT NULL,
    create_at     BIGINT NOT NULL,
    CONSTRAINT login_handoffs_code_hash_length CHECK (octet_length(code_hash) = 32),
    CONSTRAINT login_handoffs_binding_hash_length CHECK (octet_length(binding_hash) = 32)
);

CREATE INDEX login_handoffs_expiry_idx
    ON login_handoffs (expires_at);

-- PostgreSQL does not automatically index the referencing side of a foreign
-- key. Keep user deactivation/deletion from scanning every live handoff.
CREATE INDEX login_handoffs_user_idx
    ON login_handoffs (user_id);
