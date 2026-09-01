package workitems

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/jackc/pgx/v5"
)

type Event struct {
	ID         string         `json:"id"`
	WorkItemID string         `json:"work_item_id"`
	ActorID    string         `json:"actor_id,omitempty"`
	EventType  string         `json:"event_type"`
	FromStatus string         `json:"from_status,omitempty"`
	ToStatus   string         `json:"to_status,omitempty"`
	Details    map[string]any `json:"details"`
	CreateAt   int64          `json:"create_at"`
}

type rowsQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// Dependency changes are rare and correctness matters more than graph-write
// throughput. A transaction advisory lock closes the READ COMMITTED write-skew
// window where concurrent reverse edges could both pass the recursive check.
const dependencyGraphAdvisoryLockID int64 = 0x4d4f59524f574b

func lockDependencyGraphTx(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, dependencyGraphAdvisoryLockID)
	return err
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func activeMemberTx(ctx context.Context, tx pgx.Tx, channelID, userID string) (bool, error) {
	var one int
	err := tx.QueryRow(ctx, `
		SELECT 1 FROM channel_members cm
		JOIN users u ON u.id=cm.user_id AND u.delete_at=0
		  AND (
			NOT ('system_guest'=ANY(regexp_split_to_array(BTRIM(u.roles), E'\\s+')))
			OR u.guest_expires_at>$3
		  )
		WHERE cm.channel_id=$1 AND cm.user_id=$2
		FOR SHARE OF cm, u
	`, channelID, userID, time.Now().UnixMilli()).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func insertEventTx(ctx context.Context, tx pgx.Tx, itemID, actorID, eventType, fromStatus, toStatus string, details map[string]any, at int64) error {
	if details == nil {
		details = map[string]any{}
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO work_item_events
			(id, work_item_id, actor_id, event_type, from_status, to_status, details, create_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`, uuid.NewString(), itemID, nullableString(actorID), eventType, fromStatus, toStatus, raw, at)
	return err
}

func normalizeRelationIDs(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		id, err := normalizeIdentifier(value, maxIDRunes, true)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	return normalized, nil
}

func addInitialLinksTx(ctx context.Context, tx pgx.Tx, principal rbac.Principal, source *Item, dependencyIDs, impactIDs []string, at int64) error {
	dependencies, err := normalizeRelationIDs(dependencyIDs)
	if err != nil {
		return err
	}
	impacts, err := normalizeRelationIDs(impactIDs)
	if err != nil {
		return err
	}
	if len(dependencies) > 0 {
		if err := lockDependencyGraphTx(ctx, tx); err != nil {
			return err
		}
	}
	for _, targetID := range dependencies {
		if err := addLinkTx(ctx, tx, principal, source, targetID, RelationDependsOn, at); err != nil {
			return err
		}
	}
	for _, targetID := range impacts {
		if err := addLinkTx(ctx, tx, principal, source, targetID, RelationImpacts, at); err != nil {
			return err
		}
	}
	source.DependencyIDs = dependencies
	source.ImpactTaskIDs = impacts
	return nil
}

func linkTargetForPrincipalTx(ctx context.Context, tx pgx.Tx, principal rbac.Principal, targetID string) (*Item, error) {
	constraints := rbac.ResourceConstraintsFor(principal)
	target, err := scanItem(tx.QueryRow(ctx, `
		SELECT `+itemColumns+`
		FROM work_items w
		JOIN channels c ON c.id=w.channel_id AND c.delete_at=0
		JOIN channel_members cm ON cm.channel_id=w.channel_id AND cm.user_id=$2
		JOIN users actor ON actor.id=cm.user_id AND actor.delete_at=0
		  AND (
			NOT ('system_guest'=ANY(regexp_split_to_array(BTRIM(actor.roles), E'\\s+')))
			OR actor.guest_expires_at>$6
		  )
		WHERE w.id=$1 AND w.delete_at=0
		  AND (w.kind='decision' OR w.created_by=$2 OR w.assignee_id=$2)
		  AND (NOT $3::boolean OR cardinality($4::text[])=0 OR w.team_id=ANY($4::text[]))
		  AND (NOT $3::boolean OR cardinality($5::text[])=0 OR w.channel_id=ANY($5::text[]))
		FOR SHARE OF w, c, cm
	`, targetID, principal.UserID, constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs, time.Now().UnixMilli()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return target, err
}

func dependencyWouldCycleTx(ctx context.Context, tx pgx.Tx, sourceID, targetID string) (bool, error) {
	if sourceID == targetID {
		return true, nil
	}
	var cycles bool
	err := tx.QueryRow(ctx, `
		WITH RECURSIVE reachable(id) AS (
			SELECT target_item_id FROM work_item_links
			WHERE source_item_id=$1 AND relation='depends_on'
			UNION
			SELECT link.target_item_id
			FROM work_item_links link
			JOIN reachable parent ON parent.id=link.source_item_id
			WHERE link.relation='depends_on'
		)
		SELECT EXISTS(SELECT 1 FROM reachable WHERE id=$2)
	`, targetID, sourceID).Scan(&cycles)
	return cycles, err
}

func addLinkTx(ctx context.Context, tx pgx.Tx, principal rbac.Principal, source *Item, targetID, relation string, at int64) error {
	if source == nil || (relation != RelationDependsOn && relation != RelationImpacts) {
		return ErrInvalid
	}
	target, err := linkTargetForPrincipalTx(ctx, tx, principal, targetID)
	if err != nil {
		return err
	}
	// Relation IDs are returned with the source item. Keeping both ends in one
	// channel prevents a source viewer from learning IDs from another private
	// channel they cannot access.
	if target.ChannelID != source.ChannelID {
		return ErrForbidden
	}
	switch relation {
	case RelationDependsOn:
		if source.Kind != KindTask || target.Kind != KindTask {
			return ErrInvalid
		}
		if (source.Status == StatusInProgress || source.Status == StatusDone) &&
			target.Status != StatusDone && target.Status != StatusCancelled {
			return ErrBlocked
		}
		cycles, cycleErr := dependencyWouldCycleTx(ctx, tx, source.ID, target.ID)
		if cycleErr != nil {
			return cycleErr
		}
		if cycles {
			return ErrDependencyCycle
		}
	case RelationImpacts:
		if source.Kind != KindDecision || target.Kind != KindTask {
			return ErrInvalid
		}
	}
	result, err := tx.Exec(ctx, `
		INSERT INTO work_item_links (source_item_id, target_item_id, relation, created_by, create_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT DO NOTHING
	`, source.ID, target.ID, relation, nullableString(principal.UserID), at)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return nil
	}
	eventType := "dependency_added"
	detailKey := "dependency_id"
	if relation == RelationImpacts {
		eventType = "impact_added"
		detailKey = "task_id"
	}
	return insertEventTx(ctx, tx, source.ID, principal.UserID, eventType, "", "", map[string]any{detailKey: target.ID}, at)
}

func hasIncompleteDependenciesTx(ctx context.Context, tx pgx.Tx, itemID string) (bool, error) {
	var blocked bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM work_item_links link
			JOIN work_items dependency ON dependency.id=link.target_item_id AND dependency.delete_at=0
			WHERE link.source_item_id=$1 AND link.relation='depends_on'
			  AND dependency.status NOT IN ('done','cancelled')
		)
	`, itemID).Scan(&blocked)
	return blocked, err
}

func hasStartedDependentsTx(ctx context.Context, tx pgx.Tx, itemID string) (bool, error) {
	var blocked bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM work_item_links link
			JOIN work_items dependent ON dependent.id=link.source_item_id AND dependent.delete_at=0
			WHERE link.target_item_id=$1 AND link.relation='depends_on'
			  AND dependent.status IN ('in_progress','done')
		)
	`, itemID).Scan(&blocked)
	return blocked, err
}

func hydrateRelations(ctx context.Context, query rowsQuerier, items []*Item) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]string, 0, len(items))
	byID := make(map[string]*Item, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		item.DependencyIDs = []string{}
		item.ImpactTaskIDs = []string{}
		ids = append(ids, item.ID)
		byID[item.ID] = item
	}
	rows, err := query.Query(ctx, `
		SELECT source_item_id, target_item_id, relation
		FROM work_item_links
		WHERE source_item_id=ANY($1::text[])
		ORDER BY create_at ASC, target_item_id ASC
	`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var sourceID, targetID, relation string
		if err := rows.Scan(&sourceID, &targetID, &relation); err != nil {
			rows.Close()
			return err
		}
		item := byID[sourceID]
		if item == nil {
			continue
		}
		if relation == RelationDependsOn {
			item.DependencyIDs = append(item.DependencyIDs, targetID)
		} else if relation == RelationImpacts {
			item.ImpactTaskIDs = append(item.ImpactTaskIDs, targetID)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	replacements, err := query.Query(ctx, `
		SELECT supersedes_id, id FROM work_items
		WHERE supersedes_id=ANY($1::text[]) AND delete_at=0
	`, ids)
	if err != nil {
		return err
	}
	defer replacements.Close()
	for replacements.Next() {
		var predecessorID, successorID string
		if err := replacements.Scan(&predecessorID, &successorID); err != nil {
			return err
		}
		if item := byID[predecessorID]; item != nil {
			item.SupersededByID = successorID
		}
	}
	return replacements.Err()
}

func hydrateRelationsTx(ctx context.Context, tx pgx.Tx, items []*Item) error {
	return hydrateRelations(ctx, tx, items)
}

func spawnNextRecurrenceTx(ctx context.Context, tx pgx.Tx, current *Item, actorID string, now int64) (*Item, error) {
	dueAt := nextDueAt(current.DueAt, current.RecurrenceUnit, current.RecurrenceInterval, now)
	if dueAt == 0 {
		return nil, ErrInvalid
	}
	next := &Item{
		ID: uuid.NewString(), Kind: KindTask, Title: current.Title, Description: current.Description,
		Status: StatusOpen, CreatedBy: current.CreatedBy, AssigneeID: current.AssigneeID,
		TeamID: current.TeamID, ChannelID: current.ChannelID, SourcePostID: current.SourcePostID,
		SourceThreadID: current.SourceThreadID, DueAt: dueAt, Priority: current.Priority,
		RecurrenceUnit: current.RecurrenceUnit, RecurrenceInterval: current.RecurrenceInterval,
		SeriesID: current.SeriesID, OccurrenceNo: current.OccurrenceNo + 1,
		DependencyIDs: []string{}, ImpactTaskIDs: []string{}, CreateAt: now, UpdateAt: now,
	}
	if next.SeriesID == "" {
		next.SeriesID = current.ID
	}
	assigneeCleared := false
	if next.AssigneeID != "" {
		active, activeErr := activeMemberTx(ctx, tx, next.ChannelID, next.AssigneeID)
		if activeErr != nil {
			return nil, activeErr
		}
		if !active {
			// Recurrence must not fail forever because an assignee later left the
			// channel, was deleted, or is an expired guest. Keep the original
			// creator as owner and create the next occurrence unassigned.
			next.AssigneeID = ""
			assigneeCleared = true
		}
	}
	next.IdempotencyKey = fmt.Sprintf("recurrence:%s:%d", next.SeriesID, next.OccurrenceNo)
	next.CreateFingerprint = createRequestFingerprint(next)
	result, err := tx.Exec(ctx, `
		INSERT INTO work_items (
			id, kind, title, description, status, created_by, assignee_id, team_id,
			channel_id, source_post_id, source_thread_id, idempotency_key, create_fingerprint, due_at,
			decided_at, priority, completed_at, recurrence_unit, recurrence_interval,
			series_id, occurrence_no, create_at, update_at)
		VALUES ($1,'task',$2,$3,'open',$4,$5,$6,$7,$8,$9,$10,$11,$12,0,$13,0,$14,$15,$16,$17,$18,$18)
		ON CONFLICT (created_by, idempotency_key) DO NOTHING
	`, next.ID, next.Title, next.Description, next.CreatedBy, nullableString(next.AssigneeID),
		nullableString(next.TeamID), next.ChannelID, nullableString(next.SourcePostID),
		nullableString(next.SourceThreadID), next.IdempotencyKey, next.CreateFingerprint, next.DueAt, next.Priority,
		next.RecurrenceUnit, next.RecurrenceInterval, next.SeriesID, next.OccurrenceNo, now)
	if err != nil {
		return nil, err
	}
	if result.RowsAffected() == 0 {
		existing, lookupErr := scanItem(tx.QueryRow(ctx, `SELECT `+itemColumns+` FROM work_items w WHERE w.created_by=$1 AND w.idempotency_key=$2`, next.CreatedBy, next.IdempotencyKey))
		if lookupErr == nil && existing.DeleteAt != 0 {
			return nil, nil
		}
		return existing, lookupErr
	}
	createdDetails := map[string]any{"series_id": next.SeriesID, "occurrence_no": next.OccurrenceNo}
	if assigneeCleared {
		createdDetails["assignee_cleared"] = true
	}
	if err := insertEventTx(ctx, tx, next.ID, actorID, "created", "", StatusOpen, createdDetails, now); err != nil {
		return nil, err
	}
	if err := insertEventTx(ctx, tx, current.ID, actorID, "recurrence_spawned", "", "", map[string]any{"next_work_item_id": next.ID}, now); err != nil {
		return nil, err
	}
	return next, nil
}

func (s *Service) AddLinkForPrincipal(ctx context.Context, principal rbac.Principal, sourceID, targetID, relation string) (*Item, error) {
	sourceID, err := normalizeIdentifier(sourceID, maxIDRunes, true)
	if err != nil {
		return nil, err
	}
	targetID, err = normalizeIdentifier(targetID, maxIDRunes, true)
	if err != nil {
		return nil, err
	}
	if relation != RelationDependsOn && relation != RelationImpacts {
		return nil, ErrInvalid
	}
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if relation == RelationDependsOn {
		if err := lockDependencyGraphTx(ctx, tx); err != nil {
			return nil, err
		}
	}
	constraints := rbac.ResourceConstraintsFor(principal)
	source, err := scanItem(tx.QueryRow(ctx, `
		SELECT `+itemColumns+` FROM work_items w
		JOIN channels c ON c.id=w.channel_id AND c.delete_at=0
		JOIN channel_members cm ON cm.channel_id=w.channel_id AND cm.user_id=$2
		JOIN users actor ON actor.id=cm.user_id AND actor.delete_at=0
		  AND (
			NOT ('system_guest'=ANY(regexp_split_to_array(BTRIM(actor.roles), E'\\s+')))
			OR actor.guest_expires_at>$6
		  )
		WHERE w.id=$1 AND w.created_by=$2 AND w.delete_at=0
		  AND (NOT $3::boolean OR cardinality($4::text[])=0 OR w.team_id=ANY($4::text[]))
		  AND (NOT $3::boolean OR cardinality($5::text[])=0 OR w.channel_id=ANY($5::text[]))
		FOR UPDATE OF w, c, cm
	`, sourceID, principal.UserID, constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs, time.Now().UnixMilli()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := addLinkTx(ctx, tx, principal, source, targetID, relation, time.Now().UnixMilli()); err != nil {
		return nil, err
	}
	if err := hydrateRelationsTx(ctx, tx, []*Item{source}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return source, nil
}

func (s *Service) RemoveLinkForPrincipal(ctx context.Context, principal rbac.Principal, sourceID, targetID, relation string) (*Item, error) {
	sourceID, err := normalizeIdentifier(sourceID, maxIDRunes, true)
	if err != nil {
		return nil, err
	}
	targetID, err = normalizeIdentifier(targetID, maxIDRunes, true)
	if err != nil {
		return nil, err
	}
	if relation != RelationDependsOn && relation != RelationImpacts {
		return nil, ErrInvalid
	}
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	constraints := rbac.ResourceConstraintsFor(principal)
	source, err := scanItem(tx.QueryRow(ctx, `
		SELECT `+itemColumns+` FROM work_items w
		JOIN channels c ON c.id=w.channel_id AND c.delete_at=0
		JOIN channel_members cm ON cm.channel_id=w.channel_id AND cm.user_id=$2
		JOIN users actor ON actor.id=cm.user_id AND actor.delete_at=0
		  AND (
			NOT ('system_guest'=ANY(regexp_split_to_array(BTRIM(actor.roles), E'\\s+')))
			OR actor.guest_expires_at>$6
		  )
		WHERE w.id=$1 AND w.created_by=$2 AND w.delete_at=0
		  AND (NOT $3::boolean OR cardinality($4::text[])=0 OR w.team_id=ANY($4::text[]))
		  AND (NOT $3::boolean OR cardinality($5::text[])=0 OR w.channel_id=ANY($5::text[]))
		FOR UPDATE OF w, c, cm
	`, sourceID, principal.UserID, constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs, time.Now().UnixMilli()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	result, err := tx.Exec(ctx, `DELETE FROM work_item_links WHERE source_item_id=$1 AND target_item_id=$2 AND relation=$3`, sourceID, targetID, relation)
	if err != nil {
		return nil, err
	}
	if result.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	eventType := "dependency_removed"
	detailKey := "dependency_id"
	if relation == RelationImpacts {
		eventType = "impact_removed"
		detailKey = "task_id"
	}
	if err := insertEventTx(ctx, tx, source.ID, principal.UserID, eventType, "", "", map[string]any{detailKey: targetID}, time.Now().UnixMilli()); err != nil {
		return nil, err
	}
	if err := hydrateRelationsTx(ctx, tx, []*Item{source}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return source, nil
}

func (s *Service) ListEventsForPrincipal(ctx context.Context, principal rbac.Principal, itemID string) ([]Event, error) {
	itemID, err := normalizeIdentifier(itemID, maxIDRunes, true)
	if err != nil {
		return nil, err
	}
	constraints := rbac.ResourceConstraintsFor(principal)
	rows, err := s.db.Pool.Query(ctx, `
		SELECT event.id, event.work_item_id, COALESCE(event.actor_id,''), event.event_type,
		       event.from_status, event.to_status, event.details, event.create_at
		FROM work_item_events event
		JOIN work_items w ON w.id=event.work_item_id AND w.delete_at=0
		JOIN channels c ON c.id=w.channel_id AND c.delete_at=0
		JOIN channel_members cm ON cm.channel_id=w.channel_id AND cm.user_id=$2
		JOIN users actor ON actor.id=cm.user_id AND actor.delete_at=0
		  AND (
			NOT ('system_guest'=ANY(regexp_split_to_array(BTRIM(actor.roles), E'\\s+')))
			OR actor.guest_expires_at>$6
		  )
		WHERE event.work_item_id=$1
		  AND (w.kind='decision' OR w.created_by=$2 OR w.assignee_id=$2)
		  AND (NOT $3::boolean OR cardinality($4::text[])=0 OR w.team_id=ANY($4::text[]))
		  AND (NOT $3::boolean OR cardinality($5::text[])=0 OR w.channel_id=ANY($5::text[]))
		ORDER BY event.create_at DESC, event.id DESC
		LIMIT 500
	`, itemID, principal.UserID, constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var event Event
		var raw []byte
		if err := rows.Scan(&event.ID, &event.WorkItemID, &event.ActorID, &event.EventType, &event.FromStatus, &event.ToStatus, &raw, &event.CreateAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &event.Details); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(events) == 0 {
		var visible bool
		err := s.db.Pool.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM work_items w
				JOIN channels c ON c.id=w.channel_id AND c.delete_at=0
				JOIN channel_members cm ON cm.channel_id=w.channel_id AND cm.user_id=$2
				JOIN users actor ON actor.id=cm.user_id AND actor.delete_at=0
				  AND (
					NOT ('system_guest'=ANY(regexp_split_to_array(BTRIM(actor.roles), E'\\s+')))
					OR actor.guest_expires_at>$6
				  )
				WHERE w.id=$1 AND w.delete_at=0 AND (w.kind='decision' OR w.created_by=$2 OR w.assignee_id=$2)
				  AND (NOT $3::boolean OR cardinality($4::text[])=0 OR w.team_id=ANY($4::text[]))
				  AND (NOT $3::boolean OR cardinality($5::text[])=0 OR w.channel_id=ANY($5::text[]))
			)
		`, itemID, principal.UserID, constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs, time.Now().UnixMilli()).Scan(&visible)
		if err != nil {
			return nil, err
		}
		if !visible {
			return nil, ErrNotFound
		}
	}
	return events, nil
}
