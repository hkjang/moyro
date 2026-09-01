-- Durable work-management lifecycle and offline-safe message automation.
-- Existing v0.2.5 rows retain their meaning through conservative defaults.

ALTER TABLE work_items
    DROP CONSTRAINT work_items_status_check,
    DROP CONSTRAINT work_items_shape_check;

ALTER TABLE work_items
    ADD COLUMN create_fingerprint TEXT NOT NULL DEFAULT '',
    ADD COLUMN priority TEXT NOT NULL DEFAULT 'normal',
    ADD COLUMN completed_at BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN reviewer_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN recurrence_unit TEXT NOT NULL DEFAULT 'none',
    ADD COLUMN recurrence_interval INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN series_id TEXT,
    ADD COLUMN occurrence_no INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN supersedes_id TEXT REFERENCES work_items(id) ON DELETE SET NULL;

ALTER TABLE work_items
    ADD CONSTRAINT work_items_status_check CHECK (
        (kind='task' AND status IN ('open', 'in_progress', 'done', 'cancelled')) OR
        (kind='decision' AND status IN ('proposed', 'under_review', 'recorded', 'superseded', 'cancelled'))
    ),
    ADD CONSTRAINT work_items_priority_check CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
    ADD CONSTRAINT work_items_create_fingerprint_check CHECK (char_length(create_fingerprint) IN (0, 64)),
    ADD CONSTRAINT work_items_completed_at_check CHECK (completed_at >= 0),
    ADD CONSTRAINT work_items_recurrence_unit_check CHECK (recurrence_unit IN ('none', 'daily', 'weekly', 'monthly')),
    ADD CONSTRAINT work_items_recurrence_interval_check CHECK (
        (recurrence_unit='none' AND recurrence_interval=0) OR
        (recurrence_unit<>'none' AND recurrence_interval BETWEEN 1 AND 365)
    ),
    ADD CONSTRAINT work_items_occurrence_no_check CHECK (occurrence_no >= 0),
    ADD CONSTRAINT work_items_supersedes_self_check CHECK (supersedes_id IS NULL OR supersedes_id<>id),
    ADD CONSTRAINT work_items_shape_check CHECK (
        (
            kind='task' AND decided_at=0 AND reviewer_id IS NULL AND supersedes_id IS NULL AND
            (recurrence_unit='none' OR due_at>0)
        ) OR
        (
            kind='decision' AND assignee_id IS NULL AND due_at=0 AND completed_at=0 AND
            recurrence_unit='none' AND recurrence_interval=0 AND series_id IS NULL AND occurrence_no=0 AND
            (
                (status IN ('proposed', 'under_review') AND decided_at=0) OR
                (status IN ('recorded', 'superseded') AND decided_at>0) OR
                status='cancelled'
            )
        )
    );

CREATE INDEX work_items_due_idx
    ON work_items (due_at, status, id)
    WHERE delete_at=0 AND kind='task' AND due_at>0;

CREATE UNIQUE INDEX work_items_series_occurrence_idx
    ON work_items (series_id, occurrence_no)
    WHERE series_id IS NOT NULL;

CREATE UNIQUE INDEX work_items_supersedes_idx
    ON work_items (supersedes_id)
    WHERE supersedes_id IS NOT NULL AND delete_at=0;

CREATE TABLE work_item_links (
    source_item_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    target_item_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    relation TEXT NOT NULL,
    created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
    create_at BIGINT NOT NULL,
    PRIMARY KEY (source_item_id, target_item_id, relation),
    CONSTRAINT work_item_links_relation_check CHECK (relation IN ('depends_on', 'impacts')),
    CONSTRAINT work_item_links_self_check CHECK (source_item_id<>target_item_id),
    CONSTRAINT work_item_links_create_at_check CHECK (create_at>0)
);

CREATE INDEX work_item_links_target_idx
    ON work_item_links (target_item_id, relation, source_item_id);

CREATE TABLE work_item_events (
    id TEXT PRIMARY KEY,
    work_item_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    actor_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    event_type TEXT NOT NULL,
    from_status TEXT NOT NULL DEFAULT '',
    to_status TEXT NOT NULL DEFAULT '',
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    create_at BIGINT NOT NULL,
    CONSTRAINT work_item_events_type_check CHECK (
        event_type IN (
            'created', 'updated', 'status_changed', 'assigned', 'reviewer_changed',
            'dependency_added', 'dependency_removed', 'impact_added', 'impact_removed',
            'superseded', 'recurrence_spawned'
        )
    ),
    CONSTRAINT work_item_events_details_check CHECK (jsonb_typeof(details)='object'),
    CONSTRAINT work_item_events_create_at_check CHECK (create_at>0)
);

CREATE INDEX work_item_events_item_idx
    ON work_item_events (work_item_id, create_at DESC, id DESC);

INSERT INTO work_item_events (id, work_item_id, actor_id, event_type, to_status, details, create_at)
SELECT gen_random_uuid()::text, id, created_by, 'created', status, '{}'::jsonb, create_at
FROM work_items
WHERE delete_at=0;

CREATE TABLE automation_rules (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    created_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    team_id TEXT REFERENCES teams(id) ON DELETE CASCADE,
    channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    match_type TEXT NOT NULL,
    match_value TEXT NOT NULL,
    revision BIGINT NOT NULL DEFAULT 1,
    create_at BIGINT NOT NULL,
    update_at BIGINT NOT NULL,
    delete_at BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT automation_rules_name_length_check CHECK (char_length(name) BETWEEN 1 AND 120),
    CONSTRAINT automation_rules_match_type_check CHECK (match_type IN ('contains', 'starts_with')),
    CONSTRAINT automation_rules_match_value_length_check CHECK (char_length(match_value) BETWEEN 1 AND 200),
    CONSTRAINT automation_rules_revision_check CHECK (revision>0),
    CONSTRAINT automation_rules_timestamps_check CHECK (create_at>0 AND update_at>=create_at AND delete_at>=0)
);

CREATE INDEX automation_rules_owner_idx
    ON automation_rules (created_by, update_at DESC, id)
    WHERE delete_at=0;

CREATE INDEX automation_rules_channel_idx
    ON automation_rules (channel_id, enabled, create_at, id)
    WHERE delete_at=0;

CREATE TABLE automation_rule_actions (
    id TEXT PRIMARY KEY,
    rule_id TEXT NOT NULL REFERENCES automation_rules(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    action_type TEXT NOT NULL,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    create_at BIGINT NOT NULL,
    CONSTRAINT automation_rule_actions_position_check CHECK (position BETWEEN 0 AND 4),
    CONSTRAINT automation_rule_actions_type_check CHECK (action_type IN ('task', 'decision', 'reminder')),
    CONSTRAINT automation_rule_actions_config_check CHECK (jsonb_typeof(config)='object'),
    CONSTRAINT automation_rule_actions_create_at_check CHECK (create_at>0),
    UNIQUE (rule_id, position)
);

CREATE TABLE automation_runs (
    id TEXT PRIMARY KEY,
    rule_id TEXT NOT NULL REFERENCES automation_rules(id) ON DELETE CASCADE,
    -- Runs retain their immutable action snapshot when a rule revision replaces
    -- its editable action rows, so this is deliberately not a foreign key.
    action_id TEXT NOT NULL,
    post_id TEXT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    actor_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    action_type TEXT NOT NULL,
    action_config JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempt_count INTEGER NOT NULL DEFAULT 0,
    next_attempt_at BIGINT NOT NULL,
    claimed_at BIGINT NOT NULL DEFAULT 0,
    lease_until BIGINT NOT NULL DEFAULT 0,
    claim_token TEXT NOT NULL DEFAULT '',
    result_type TEXT NOT NULL DEFAULT '',
    result_id TEXT NOT NULL DEFAULT '',
    last_error_code TEXT NOT NULL DEFAULT '',
    last_error_text TEXT NOT NULL DEFAULT '',
    create_at BIGINT NOT NULL,
    update_at BIGINT NOT NULL,
    completed_at BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT automation_runs_action_type_check CHECK (action_type IN ('task', 'decision', 'reminder')),
    CONSTRAINT automation_runs_config_check CHECK (jsonb_typeof(action_config)='object'),
    CONSTRAINT automation_runs_status_check CHECK (status IN ('pending', 'processing', 'retry', 'succeeded', 'dead', 'cancelled')),
    CONSTRAINT automation_runs_attempt_count_check CHECK (attempt_count>=0),
    CONSTRAINT automation_runs_timestamps_check CHECK (
        create_at>0 AND update_at>=create_at AND next_attempt_at>=0 AND
        claimed_at>=0 AND lease_until>=0 AND completed_at>=0
    ),
    UNIQUE (rule_id, action_id, post_id)
);

CREATE INDEX automation_runs_due_idx
    ON automation_runs (next_attempt_at, create_at, id)
    WHERE status IN ('pending', 'retry', 'processing');

CREATE INDEX automation_runs_rule_idx
    ON automation_runs (rule_id, create_at DESC, id DESC);
