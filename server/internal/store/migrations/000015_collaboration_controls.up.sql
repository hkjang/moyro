-- User-scoped inbox rules, bounded guest access, and OIDC-driven
-- collaboration controls.  OIDC group mappings themselves live in the
-- revisioned system_settings JSON document so administrators can update the
-- provider and its mappings atomically.

CREATE TABLE user_inbox_preferences (
    user_id                    TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    vip_user_ids               TEXT[] NOT NULL DEFAULT '{}',
    priority_event_types       TEXT[] NOT NULL DEFAULT ARRAY[
        'mention', 'direct_message', 'approval_requested', 'system_warning'
    ]::TEXT[],
    bundle_by                  TEXT NOT NULL DEFAULT 'channel'
        CHECK (bundle_by IN ('none', 'channel', 'type')),
    snooze_presets_minutes     INTEGER[] NOT NULL DEFAULT ARRAY[60, 240, 1440],
    work_hours_enabled         BOOLEAN NOT NULL DEFAULT FALSE,
    work_hours_timezone        TEXT NOT NULL DEFAULT 'UTC',
    work_hours_weekdays        SMALLINT[] NOT NULL DEFAULT ARRAY[1,2,3,4,5]::SMALLINT[],
    work_hours_start_minute    INTEGER NOT NULL DEFAULT 540
        CHECK (work_hours_start_minute BETWEEN 0 AND 1439),
    work_hours_end_minute      INTEGER NOT NULL DEFAULT 1080
        CHECK (work_hours_end_minute BETWEEN 0 AND 1439),
    priority_override          BOOLEAN NOT NULL DEFAULT TRUE,
    update_at                  BIGINT NOT NULL,
    CHECK (cardinality(vip_user_ids) <= 200),
    CHECK (cardinality(priority_event_types) <= 16),
    CHECK (cardinality(snooze_presets_minutes) BETWEEN 1 AND 8),
    CHECK (cardinality(work_hours_weekdays) BETWEEN 1 AND 7)
);

ALTER TABLE users
    ADD COLUMN guest_expires_at BIGINT NOT NULL DEFAULT 0
        CHECK (guest_expires_at >= 0),
    ADD COLUMN guest_file_download BOOLEAN NOT NULL DEFAULT TRUE;

-- Preserve pre-existing Mattermost-style guest accounts during upgrade while
-- converting them to the new bounded model. Deleted guests remain closed; an
-- administrator must explicitly renew them if they are later reactivated.
UPDATE users
SET guest_expires_at = (EXTRACT(EPOCH FROM clock_timestamp() + INTERVAL '30 days') * 1000)::BIGINT,
    guest_file_download = TRUE,
    update_at = GREATEST(update_at, (EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::BIGINT)
WHERE delete_at = 0
  AND guest_expires_at = 0
  AND roles ~ '(^|[[:space:]])system_guest([[:space:]]|$)';

ALTER TABLE invite_tokens
    ADD COLUMN invite_kind TEXT NOT NULL DEFAULT 'member'
        CHECK (invite_kind IN ('member', 'guest')),
    ADD COLUMN channel_ids TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN guest_expires_after_seconds BIGINT NOT NULL DEFAULT 0
        CHECK (guest_expires_after_seconds >= 0),
    ADD COLUMN guest_file_download BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE invite_tokens
    ADD CONSTRAINT invite_tokens_guest_scope_check CHECK (
        invite_kind = 'member'
        OR (
            cardinality(channel_ids) BETWEEN 1 AND 100
            AND guest_expires_after_seconds BETWEEN 3600 AND 31536000
        )
    );
