// Package workitems stores tasks and decisions that are derived from a
// conversation.  It deliberately keeps the source post and channel on every
// row so authorization can be re-evaluated instead of trusting client-supplied
// display metadata.
package workitems

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
)

const (
	KindTask     = "task"
	KindDecision = "decision"

	StatusOpen        = "open"
	StatusInProgress  = "in_progress"
	StatusDone        = "done"
	StatusCancelled   = "cancelled"
	StatusRecorded    = "recorded"
	StatusSuperseded  = "superseded"
	StatusProposed    = "proposed"
	StatusUnderReview = "under_review"

	PriorityLow    = "low"
	PriorityNormal = "normal"
	PriorityHigh   = "high"
	PriorityUrgent = "urgent"

	RecurrenceNone    = "none"
	RecurrenceDaily   = "daily"
	RecurrenceWeekly  = "weekly"
	RecurrenceMonthly = "monthly"

	RelationDependsOn = "depends_on"
	RelationImpacts   = "impacts"

	DefaultPageSize = 40
	MaxPageSize     = 100
	maxTitleRunes   = 240
	maxBodyRunes    = 10_000
	maxIDRunes      = 128
	maxKeyRunes     = 200
)

var (
	ErrInvalid             = errors.New("invalid work item")
	ErrNotFound            = errors.New("work item not found")
	ErrForbidden           = errors.New("work item operation forbidden")
	ErrSourceNotAccessible = errors.New("source post is not accessible")
	ErrIdempotencyConflict = errors.New("idempotency key was used for another work item")
	ErrBlocked             = errors.New("work item is blocked by an unfinished dependency")
	ErrDependencyCycle     = errors.New("work item dependency would create a cycle")
	ErrTransition          = errors.New("work item status transition is not allowed")
)

type Item struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	Status         string `json:"status"`
	CreatedBy      string `json:"created_by"`
	AssigneeID     string `json:"assignee_id,omitempty"`
	TeamID         string `json:"team_id,omitempty"`
	ChannelID      string `json:"channel_id"`
	SourcePostID   string `json:"source_post_id,omitempty"`
	SourceThreadID string `json:"source_thread_id,omitempty"`
	// IdempotencyKey is request-control metadata, not part of the user-facing
	// work object. Keeping it out of JSON also prevents websocket broadcasts
	// from leaking a reusable key to clients.
	IdempotencyKey     string   `json:"-"`
	CreateFingerprint  string   `json:"-"`
	DueAt              int64    `json:"due_at"`
	DecidedAt          int64    `json:"decided_at"`
	Priority           string   `json:"priority"`
	CompletedAt        int64    `json:"completed_at"`
	ReviewerID         string   `json:"reviewer_id,omitempty"`
	RecurrenceUnit     string   `json:"recurrence_unit"`
	RecurrenceInterval int      `json:"recurrence_interval"`
	SeriesID           string   `json:"series_id,omitempty"`
	OccurrenceNo       int      `json:"occurrence_no"`
	SupersedesID       string   `json:"supersedes_id,omitempty"`
	SupersededByID     string   `json:"superseded_by_id,omitempty"`
	DependencyIDs      []string `json:"dependency_ids"`
	ImpactTaskIDs      []string `json:"impact_task_ids"`
	CreateAt           int64    `json:"create_at"`
	UpdateAt           int64    `json:"update_at"`
	DeleteAt           int64    `json:"delete_at"`
	// PreviousAssigneeID is populated only by Patch when reassignment removes
	// another viewer. It is websocket fan-out metadata and must never leave the
	// server in JSON responses.
	PreviousAssigneeID string `json:"-"`
	// AssigneeChanged lets adapters emit assignment notifications only for a
	// real ownership transition. It is transient fan-out metadata.
	AssigneeChanged bool  `json:"-"`
	ReviewerChanged bool  `json:"-"`
	SpawnedItem     *Item `json:"-"`
}

type CreateInput struct {
	Kind               string
	Title              string
	Description        string
	AssigneeID         string
	SourcePostID       string
	DueAt              int64
	IdempotencyKey     string
	Priority           string
	ReviewerID         string
	InitialStatus      string
	RecurrenceUnit     string
	RecurrenceInterval int
	SupersedesID       string
	DependencyIDs      []string
	ImpactTaskIDs      []string
}

type PatchInput struct {
	Title              *string
	Description        *string
	Status             *string
	AssigneeID         *string
	DueAt              *int64
	Priority           *string
	ReviewerID         *string
	RecurrenceUnit     *string
	RecurrenceInterval *int
}

type ListOptions struct {
	Kind     string
	Status   string
	Cursor   string
	PageSize int
	DueFrom  int64
	DueTo    int64
	Sort     string
}

type Page struct {
	Items      []Item `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

type cursor struct {
	CreateAt int64  `json:"create_at"`
	DueAt    int64  `json:"due_at,omitempty"`
	Sort     string `json:"sort,omitempty"`
	ID       string `json:"id"`
}

const itemColumns = `
	w.id, w.kind, w.title, w.description, w.status, w.created_by,
	COALESCE(w.assignee_id,''), COALESCE(w.team_id,''), w.channel_id,
	COALESCE(w.source_post_id,''), COALESCE(w.source_thread_id,''),
	w.idempotency_key, w.create_fingerprint, w.due_at, w.decided_at, w.priority, w.completed_at,
	COALESCE(w.reviewer_id,''), w.recurrence_unit, w.recurrence_interval,
	COALESCE(w.series_id,''), w.occurrence_no, COALESCE(w.supersedes_id,''),
	w.create_at, w.update_at, w.delete_at`

type scanner interface{ Scan(...any) error }

func scanItem(row scanner) (*Item, error) {
	var item Item
	err := row.Scan(
		&item.ID, &item.Kind, &item.Title, &item.Description, &item.Status,
		&item.CreatedBy, &item.AssigneeID, &item.TeamID, &item.ChannelID,
		&item.SourcePostID, &item.SourceThreadID, &item.IdempotencyKey,
		&item.CreateFingerprint,
		&item.DueAt, &item.DecidedAt, &item.Priority, &item.CompletedAt,
		&item.ReviewerID, &item.RecurrenceUnit, &item.RecurrenceInterval,
		&item.SeriesID, &item.OccurrenceNo, &item.SupersedesID,
		&item.CreateAt, &item.UpdateAt,
		&item.DeleteAt,
	)
	item.DependencyIDs = []string{}
	item.ImpactTaskIDs = []string{}
	return &item, err
}

func normalizeText(value string, maxRunes int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !validUserText(value, false) || utf8.RuneCountInString(value) > maxRunes {
		return "", ErrInvalid
	}
	return value, nil
}

func normalizeOptionalText(value string, maxRunes int) (string, error) {
	value = strings.TrimSpace(value)
	if !validUserText(value, true) || utf8.RuneCountInString(value) > maxRunes {
		return "", ErrInvalid
	}
	return value, nil
}

func validUserText(value string, allowLayout bool) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if !unicode.IsControl(char) {
			continue
		}
		if allowLayout && (char == '\n' || char == '\r' || char == '\t') {
			continue
		}
		return false
	}
	return true
}

func normalizeIdentifier(value string, maxRunes int, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if !validUserText(value, false) {
		return "", ErrInvalid
	}
	length := utf8.RuneCountInString(value)
	if (required && length == 0) || length > maxRunes {
		return "", ErrInvalid
	}
	return value, nil
}

func validKind(kind string) bool { return kind == KindTask || kind == KindDecision }

func validStatus(kind, status string) bool {
	switch kind {
	case KindTask:
		return status == StatusOpen || status == StatusInProgress || status == StatusDone || status == StatusCancelled
	case KindDecision:
		return status == StatusProposed || status == StatusUnderReview || status == StatusRecorded || status == StatusSuperseded || status == StatusCancelled
	default:
		return false
	}
}

func validPriority(value string) bool {
	return value == PriorityLow || value == PriorityNormal || value == PriorityHigh || value == PriorityUrgent
}

func validRecurrence(unit string, interval int) bool {
	switch unit {
	case RecurrenceNone:
		return interval == 0
	case RecurrenceDaily, RecurrenceWeekly, RecurrenceMonthly:
		return interval >= 1 && interval <= 365
	default:
		return false
	}
}

func validTransition(kind, from, to string) bool {
	if from == to {
		return true
	}
	if kind == KindTask {
		switch from {
		case StatusOpen:
			return to == StatusInProgress || to == StatusDone || to == StatusCancelled
		case StatusInProgress:
			return to == StatusOpen || to == StatusDone || to == StatusCancelled
		case StatusDone:
			return to == StatusOpen
		case StatusCancelled:
			return to == StatusOpen
		}
		return false
	}
	switch from {
	case StatusProposed:
		return to == StatusUnderReview || to == StatusRecorded || to == StatusCancelled
	case StatusUnderReview:
		return to == StatusProposed || to == StatusRecorded || to == StatusCancelled
	case StatusRecorded:
		// A recorded decision is superseded only by atomically creating its
		// recorded replacement. A standalone status patch would otherwise
		// leave the lifecycle without a successor.
		return to == StatusCancelled
	case StatusSuperseded:
		return false
	case StatusCancelled:
		return to == StatusProposed || to == StatusRecorded
	default:
		return false
	}
}

func nextDueAt(current int64, unit string, interval int, now int64) int64 {
	if current <= 0 || !validRecurrence(unit, interval) || unit == RecurrenceNone {
		return 0
	}
	next := time.UnixMilli(current).UTC()
	advance := func() {
		switch unit {
		case RecurrenceDaily:
			next = next.AddDate(0, 0, interval)
		case RecurrenceWeekly:
			next = next.AddDate(0, 0, 7*interval)
		case RecurrenceMonthly:
			next = addMonthsClamped(next, interval)
		}
	}
	// A recurrence is always after the occurrence being completed, even when
	// that occurrence is completed before its due time.
	advance()
	for next.UnixMilli() <= now {
		advance()
	}
	return next.UnixMilli()
}

func addMonthsClamped(value time.Time, months int) time.Time {
	targetMonthStart := time.Date(value.Year(), value.Month()+time.Month(months), 1,
		value.Hour(), value.Minute(), value.Second(), value.Nanosecond(), value.Location())
	lastDay := targetMonthStart.AddDate(0, 1, -1).Day()
	day := value.Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(targetMonthStart.Year(), targetMonthStart.Month(), day,
		value.Hour(), value.Minute(), value.Second(), value.Nanosecond(), value.Location())
}

// Create derives team, channel, and thread scope from a live source post.
// The actor and optional assignee must both be current channel members.
func (s *Service) Create(ctx context.Context, actorID string, input CreateInput) (*Item, bool, error) {
	return s.CreateForPrincipal(ctx, rbac.UserPrincipal(actorID), input)
}

// CreateForPrincipal additionally intersects the live source-post membership
// with the authenticated credential's optional team/channel allow-lists.
func (s *Service) CreateForPrincipal(ctx context.Context, principal rbac.Principal, input CreateInput) (*Item, bool, error) {
	actorID := principal.UserID
	var err error
	if actorID, err = normalizeIdentifier(actorID, maxIDRunes, true); err != nil {
		return nil, false, err
	}
	input.Kind = strings.TrimSpace(input.Kind)
	if input.SourcePostID, err = normalizeIdentifier(input.SourcePostID, maxIDRunes, true); err != nil {
		return nil, false, err
	}
	if input.AssigneeID, err = normalizeIdentifier(input.AssigneeID, maxIDRunes, false); err != nil {
		return nil, false, err
	}
	if input.ReviewerID, err = normalizeIdentifier(input.ReviewerID, maxIDRunes, false); err != nil {
		return nil, false, err
	}
	if input.SupersedesID, err = normalizeIdentifier(input.SupersedesID, maxIDRunes, false); err != nil {
		return nil, false, err
	}
	if input.IdempotencyKey, err = normalizeIdentifier(input.IdempotencyKey, maxKeyRunes, true); err != nil {
		return nil, false, err
	}
	input.Priority = strings.TrimSpace(input.Priority)
	if input.Priority == "" {
		input.Priority = PriorityNormal
	}
	input.RecurrenceUnit = strings.TrimSpace(input.RecurrenceUnit)
	if input.RecurrenceUnit == "" {
		input.RecurrenceUnit = RecurrenceNone
	}
	input.InitialStatus = strings.TrimSpace(input.InitialStatus)
	if !validKind(input.Kind) || input.DueAt < 0 {
		return nil, false, ErrInvalid
	}
	if input.Title, err = normalizeText(input.Title, maxTitleRunes); err != nil {
		return nil, false, err
	}
	if input.Description, err = normalizeOptionalText(input.Description, maxBodyRunes); err != nil {
		return nil, false, err
	}
	if !validPriority(input.Priority) || !validRecurrence(input.RecurrenceUnit, input.RecurrenceInterval) {
		return nil, false, ErrInvalid
	}
	if input.Kind == KindTask && (input.ReviewerID != "" || input.SupersedesID != "" || input.InitialStatus != "" || (input.RecurrenceUnit != RecurrenceNone && input.DueAt == 0)) {
		return nil, false, ErrInvalid
	}
	if input.Kind == KindDecision && (input.AssigneeID != "" || input.DueAt != 0 || input.Priority != PriorityNormal || input.RecurrenceUnit != RecurrenceNone || input.RecurrenceInterval != 0 || len(input.DependencyIDs) > 0) {
		return nil, false, ErrInvalid
	}
	if input.Kind == KindDecision && input.InitialStatus == "" {
		input.InitialStatus = StatusRecorded
	}
	if input.Kind == KindDecision && input.InitialStatus != StatusProposed && input.InitialStatus != StatusRecorded {
		return nil, false, ErrInvalid
	}
	if input.Kind == KindDecision && input.SupersedesID != "" && input.InitialStatus != StatusRecorded {
		return nil, false, ErrInvalid
	}
	input.DependencyIDs, err = normalizeRelationIDs(input.DependencyIDs)
	if err != nil {
		return nil, false, err
	}
	input.ImpactTaskIDs, err = normalizeRelationIDs(input.ImpactTaskIDs)
	if err != nil {
		return nil, false, err
	}

	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)

	var channelID, teamID, rootID string
	err = tx.QueryRow(ctx, `
		SELECT p.channel_id, COALESCE(c.team_id,''), COALESCE(p.root_id,'')
		FROM posts p
		JOIN channels c ON c.id=p.channel_id AND c.delete_at=0
		JOIN channel_members cm ON cm.channel_id=p.channel_id AND cm.user_id=$1
		JOIN users actor ON actor.id=cm.user_id AND actor.delete_at=0
		  AND (
			NOT ('system_guest'=ANY(regexp_split_to_array(BTRIM(actor.roles), E'\\s+')))
			OR actor.guest_expires_at>$3
		  )
		WHERE p.id=$2 AND p.delete_at=0
		FOR SHARE OF p, c, cm, actor
	`, actorID, input.SourcePostID, time.Now().UnixMilli()).Scan(&channelID, &teamID, &rootID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, ErrSourceNotAccessible
	}
	if err != nil {
		return nil, false, err
	}
	if !rbac.ResourceConstraintsFor(principal).Allows(teamID, channelID) {
		return nil, false, ErrSourceNotAccessible
	}
	if input.Kind == KindTask && input.AssigneeID == "" {
		input.AssigneeID = actorID
	}
	if input.AssigneeID != "" {
		member, memberErr := activeMemberTx(ctx, tx, channelID, input.AssigneeID)
		if memberErr != nil {
			return nil, false, memberErr
		}
		if !member {
			return nil, false, ErrForbidden
		}
	}
	if input.ReviewerID != "" {
		member, memberErr := activeMemberTx(ctx, tx, channelID, input.ReviewerID)
		if memberErr != nil {
			return nil, false, memberErr
		}
		if !member {
			return nil, false, ErrForbidden
		}
	}
	var predecessor *Item
	if input.SupersedesID != "" {
		predecessor, err = scanItem(tx.QueryRow(ctx, `
			SELECT `+itemColumns+` FROM work_items w
			WHERE w.id=$1 AND w.kind='decision' AND w.created_by=$2
			  AND w.channel_id=$3 AND w.delete_at=0 AND w.status IN ('recorded','superseded')
			FOR UPDATE
		`, input.SupersedesID, actorID, channelID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, ErrForbidden
		}
		if err != nil {
			return nil, false, err
		}
		if predecessor.Status == StatusSuperseded {
			var replayExists bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS(
					SELECT 1 FROM work_items
					WHERE created_by=$1 AND idempotency_key=$2 AND supersedes_id=$3
				)
			`, actorID, input.IdempotencyKey, predecessor.ID).Scan(&replayExists); err != nil {
				return nil, false, err
			}
			if !replayExists {
				return nil, false, ErrForbidden
			}
			predecessor = nil
		}
	}

	now := time.Now().UnixMilli()
	item := &Item{
		ID: uuid.NewString(), Kind: input.Kind, Title: input.Title,
		Description: input.Description, CreatedBy: actorID,
		AssigneeID: input.AssigneeID, TeamID: teamID, ChannelID: channelID,
		SourcePostID: input.SourcePostID, IdempotencyKey: input.IdempotencyKey,
		DueAt: input.DueAt, Priority: input.Priority, ReviewerID: input.ReviewerID,
		RecurrenceUnit: input.RecurrenceUnit, RecurrenceInterval: input.RecurrenceInterval,
		SupersedesID: input.SupersedesID, DependencyIDs: input.DependencyIDs, ImpactTaskIDs: input.ImpactTaskIDs,
		CreateAt: now, UpdateAt: now,
	}
	if rootID != "" {
		item.SourceThreadID = rootID
	}
	if item.Kind == KindTask {
		item.Status = StatusOpen
		if item.RecurrenceUnit != RecurrenceNone {
			item.SeriesID = item.ID
		}
	} else {
		item.Status = input.InitialStatus
		if item.Status == StatusRecorded {
			item.DecidedAt = now
		}
	}
	item.CreateFingerprint = createRequestFingerprint(item)
	var assignee any
	if item.AssigneeID != "" {
		assignee = item.AssigneeID
	}
	var team any
	if item.TeamID != "" {
		team = item.TeamID
	}
	var thread any
	if item.SourceThreadID != "" {
		thread = item.SourceThreadID
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO work_items (
			id, kind, title, description, status, created_by, assignee_id,
			team_id, channel_id, source_post_id, source_thread_id,
			idempotency_key, create_fingerprint, due_at, decided_at, priority, completed_at,
			reviewer_id, recurrence_unit, recurrence_interval, series_id,
			occurrence_no, supersedes_id, create_at, update_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,0,$17,$18,$19,$20,0,$21,$22,$22)
		ON CONFLICT (created_by, idempotency_key) DO NOTHING
	`, item.ID, item.Kind, item.Title, item.Description, item.Status, item.CreatedBy,
		assignee, team, item.ChannelID, item.SourcePostID, thread,
		item.IdempotencyKey, item.CreateFingerprint, item.DueAt, item.DecidedAt, item.Priority,
		nullableString(item.ReviewerID), item.RecurrenceUnit, item.RecurrenceInterval,
		nullableString(item.SeriesID), nullableString(item.SupersedesID), now)
	if err != nil {
		return nil, false, err
	}
	if tag.RowsAffected() == 0 {
		existing, err := scanItem(tx.QueryRow(ctx, `SELECT `+itemColumns+` FROM work_items w WHERE w.created_by=$1 AND w.idempotency_key=$2`, actorID, input.IdempotencyKey))
		if err != nil {
			return nil, false, err
		}
		if err := hydrateRelationsTx(ctx, tx, []*Item{existing}); err != nil {
			return nil, false, err
		}
		if existing.DeleteAt != 0 || !sameCreateRequest(existing, item) {
			return nil, false, ErrIdempotencyConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		return existing, true, nil
	}
	if predecessor != nil {
		if _, err = tx.Exec(ctx, `UPDATE work_items SET status='superseded', update_at=$2 WHERE id=$1`, predecessor.ID, now); err != nil {
			return nil, false, err
		}
		if err = insertEventTx(ctx, tx, predecessor.ID, actorID, "superseded", predecessor.Status, StatusSuperseded, map[string]any{"replacement_id": item.ID}, now); err != nil {
			return nil, false, err
		}
	}
	if err := addInitialLinksTx(ctx, tx, principal, item, input.DependencyIDs, input.ImpactTaskIDs, now); err != nil {
		return nil, false, err
	}
	if err := insertEventTx(ctx, tx, item.ID, actorID, "created", "", item.Status, nil, now); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return item, false, nil
}

func sameCreateRequest(existing, requested *Item) bool {
	if existing != nil && requested != nil && existing.CreateFingerprint != "" && requested.CreateFingerprint != "" {
		return existing.CreateFingerprint == requested.CreateFingerprint
	}
	return existing != nil && requested != nil &&
		existing.Kind == requested.Kind &&
		existing.Status == requested.Status &&
		existing.Title == requested.Title &&
		existing.Description == requested.Description &&
		existing.AssigneeID == requested.AssigneeID &&
		existing.TeamID == requested.TeamID &&
		existing.ChannelID == requested.ChannelID &&
		existing.SourcePostID == requested.SourcePostID &&
		existing.SourceThreadID == requested.SourceThreadID &&
		existing.DueAt == requested.DueAt &&
		existing.Priority == requested.Priority &&
		existing.ReviewerID == requested.ReviewerID &&
		existing.RecurrenceUnit == requested.RecurrenceUnit &&
		existing.RecurrenceInterval == requested.RecurrenceInterval &&
		existing.SupersedesID == requested.SupersedesID &&
		sameIDSet(existing.DependencyIDs, requested.DependencyIDs) &&
		sameIDSet(existing.ImpactTaskIDs, requested.ImpactTaskIDs)
}

func createRequestFingerprint(item *Item) string {
	if item == nil {
		return ""
	}
	dependencies := append([]string(nil), item.DependencyIDs...)
	impacts := append([]string(nil), item.ImpactTaskIDs...)
	sort.Strings(dependencies)
	sort.Strings(impacts)
	payload := struct {
		Kind, Title, Description, Status, AssigneeID, TeamID, ChannelID string
		SourcePostID, SourceThreadID, Priority, ReviewerID              string
		RecurrenceUnit, SupersedesID                                    string
		DueAt                                                           int64
		RecurrenceInterval                                              int
		DependencyIDs, ImpactTaskIDs                                    []string
	}{
		Kind: item.Kind, Title: item.Title, Description: item.Description, Status: item.Status,
		AssigneeID: item.AssigneeID, TeamID: item.TeamID, ChannelID: item.ChannelID,
		SourcePostID: item.SourcePostID, SourceThreadID: item.SourceThreadID,
		Priority: item.Priority, ReviewerID: item.ReviewerID,
		RecurrenceUnit: item.RecurrenceUnit, RecurrenceInterval: item.RecurrenceInterval,
		SupersedesID: item.SupersedesID, DueAt: item.DueAt,
		DependencyIDs: dependencies, ImpactTaskIDs: impacts,
	}
	raw, _ := json.Marshal(payload)
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("%x", digest)
}

func sameIDSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	values := make(map[string]int, len(left))
	for _, value := range left {
		values[value]++
	}
	for _, value := range right {
		values[value]--
	}
	for _, count := range values {
		if count != 0 {
			return false
		}
	}
	return true
}

func decodeCursor(value string) (cursor, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return cursor{}, nil
	}
	if len(value) > 1024 {
		return cursor{}, ErrInvalid
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil {
		return cursor{}, ErrInvalid
	}
	var decoded cursor
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return cursor{}, ErrInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return cursor{}, ErrInvalid
	}
	if decoded.Sort == "" {
		decoded.Sort = "created"
	}
	if (decoded.Sort != "created" && decoded.Sort != "due") || strings.TrimSpace(decoded.ID) != decoded.ID {
		return cursor{}, ErrInvalid
	}
	if (decoded.Sort == "created" && decoded.CreateAt <= 0) || (decoded.Sort == "due" && decoded.DueAt <= 0) {
		return cursor{}, ErrInvalid
	}
	if _, err := normalizeIdentifier(decoded.ID, maxIDRunes, true); err != nil {
		return cursor{}, ErrInvalid
	}
	return decoded, nil
}

func encodeCursor(item Item) string {
	raw, _ := json.Marshal(cursor{CreateAt: item.CreateAt, Sort: "created", ID: item.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func encodeDueCursor(item Item) string {
	raw, _ := json.Marshal(cursor{DueAt: item.DueAt, Sort: "due", ID: item.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// ListForUser returns the caller's assigned/created tasks and decisions from
// channels they can still access.  A removed channel membership therefore
// removes the item from subsequent reads without deleting its audit history.
func (s *Service) ListForUser(ctx context.Context, userID string, options ListOptions) (*Page, error) {
	return s.ListForPrincipal(ctx, rbac.UserPrincipal(userID), options)
}

// ListForPrincipal preserves the user's membership/visibility rules and also
// applies any resource restrictions carried by a Moyro API-key principal.
func (s *Service) ListForPrincipal(ctx context.Context, principal rbac.Principal, options ListOptions) (*Page, error) {
	userID := principal.UserID
	var err error
	if userID, err = normalizeIdentifier(userID, maxIDRunes, true); err != nil {
		return nil, err
	}
	options.Kind = strings.TrimSpace(options.Kind)
	options.Status = strings.TrimSpace(options.Status)
	options.Sort = strings.TrimSpace(options.Sort)
	if options.Sort == "" {
		options.Sort = "created"
	}
	if options.Kind != "" && !validKind(options.Kind) {
		return nil, ErrInvalid
	}
	if options.Status != "" && options.Kind == "" {
		return nil, ErrInvalid
	}
	if options.Status != "" && !validStatus(options.Kind, options.Status) {
		return nil, ErrInvalid
	}
	if options.DueFrom < 0 || options.DueTo < 0 || (options.DueFrom > 0 && options.DueTo > 0 && options.DueTo <= options.DueFrom) {
		return nil, ErrInvalid
	}
	if options.Sort != "created" && options.Sort != "due" {
		return nil, ErrInvalid
	}
	if (options.Sort == "due" || options.DueFrom > 0 || options.DueTo > 0) && options.Kind != KindTask {
		return nil, ErrInvalid
	}
	position, err := decodeCursor(options.Cursor)
	if err != nil {
		return nil, err
	}
	if options.Cursor != "" && position.Sort != options.Sort {
		return nil, ErrInvalid
	}
	pageSize := options.PageSize
	if pageSize < 0 {
		return nil, ErrInvalid
	}
	if pageSize == 0 {
		pageSize = DefaultPageSize
	}
	if pageSize > MaxPageSize {
		pageSize = MaxPageSize
	}

	constraints := rbac.ResourceConstraintsFor(principal)
	var rows pgx.Rows
	if options.Sort == "due" {
		rows, err = s.db.Pool.Query(ctx, `
			SELECT `+itemColumns+`
			FROM work_items w
			JOIN channel_members cm ON cm.channel_id=w.channel_id AND cm.user_id=$1
			JOIN channels c ON c.id=w.channel_id AND c.delete_at=0
			JOIN users actor ON actor.id=cm.user_id AND actor.delete_at=0
			  AND (
				NOT ('system_guest'=ANY(regexp_split_to_array(BTRIM(actor.roles), E'\\s+')))
				OR actor.guest_expires_at>$11
			  )
			WHERE w.delete_at=0 AND w.kind='task' AND w.due_at>0
			  AND (w.created_by=$1 OR w.assignee_id=$1)
			  AND ($2='' OR w.status=$2)
			  AND ($3::bigint=0 OR w.due_at >= $3)
			  AND ($4::bigint=0 OR w.due_at < $4)
			  AND ($5::bigint=0 OR (w.due_at, w.id) > ($5, $6))
			  AND (NOT $7::boolean OR cardinality($8::text[])=0 OR w.team_id=ANY($8::text[]))
			  AND (NOT $7::boolean OR cardinality($9::text[])=0 OR w.channel_id=ANY($9::text[]))
			ORDER BY w.due_at ASC, w.id ASC
			LIMIT $10
		`, userID, options.Status, options.DueFrom, options.DueTo, position.DueAt, position.ID,
			constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs, pageSize+1, time.Now().UnixMilli())
	} else {
		rows, err = s.db.Pool.Query(ctx, `
			SELECT `+itemColumns+`
			FROM work_items w
			JOIN channel_members cm ON cm.channel_id=w.channel_id AND cm.user_id=$1
			JOIN channels c ON c.id=w.channel_id AND c.delete_at=0
			JOIN users actor ON actor.id=cm.user_id AND actor.delete_at=0
			  AND (
				NOT ('system_guest'=ANY(regexp_split_to_array(BTRIM(actor.roles), E'\\s+')))
				OR actor.guest_expires_at>$12
			  )
			WHERE w.delete_at=0
			  AND (w.created_by=$1 OR w.assignee_id=$1 OR w.kind='decision')
			  AND ($2='' OR w.kind=$2)
			  AND ($3='' OR w.status=$3)
			  AND ($4::bigint=0 OR w.due_at >= $4)
			  AND ($5::bigint=0 OR w.due_at < $5)
			  AND ($6::bigint=0 OR (w.create_at, w.id) < ($6, $7))
			  AND (NOT $8::boolean OR cardinality($9::text[])=0 OR w.team_id=ANY($9::text[]))
			  AND (NOT $8::boolean OR cardinality($10::text[])=0 OR w.channel_id=ANY($10::text[]))
			ORDER BY w.create_at DESC, w.id DESC
			LIMIT $11
		`, userID, options.Kind, options.Status, options.DueFrom, options.DueTo,
			position.CreateAt, position.ID, constraints.Restricted, constraints.TeamIDs,
			constraints.ChannelIDs, pageSize+1, time.Now().UnixMilli())
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Item, 0, pageSize+1)
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	page := &Page{Items: items}
	if len(page.Items) > pageSize {
		if options.Sort == "due" {
			page.NextCursor = encodeDueCursor(page.Items[pageSize-1])
		} else {
			page.NextCursor = encodeCursor(page.Items[pageSize-1])
		}
		page.Items = page.Items[:pageSize]
	}
	itemPointers := make([]*Item, 0, len(page.Items))
	for index := range page.Items {
		itemPointers = append(itemPointers, &page.Items[index])
	}
	if err := hydrateRelations(ctx, s.db.Pool, itemPointers); err != nil {
		return nil, err
	}
	return page, nil
}

func patchHasOwnerFields(input PatchInput) bool {
	return input.Title != nil || input.Description != nil || input.AssigneeID != nil || input.DueAt != nil ||
		input.Priority != nil || input.ReviewerID != nil || input.RecurrenceUnit != nil || input.RecurrenceInterval != nil
}

// Patch enforces ownership at the persistence boundary.  An assignee may
// change only task status; title, assignment, dates, and every decision remain
// controlled by the creator.
func (s *Service) Patch(ctx context.Context, actorID, itemID string, input PatchInput) (*Item, error) {
	return s.PatchForPrincipal(ctx, rbac.UserPrincipal(actorID), itemID, input)
}

// PatchForPrincipal makes a guessed out-of-scope item ID indistinguishable
// from a missing row before applying the existing owner/assignee checks.
func (s *Service) PatchForPrincipal(ctx context.Context, principal rbac.Principal, itemID string, input PatchInput) (*Item, error) {
	actorID := principal.UserID
	var err error
	if actorID, err = normalizeIdentifier(actorID, maxIDRunes, true); err != nil {
		return nil, err
	}
	if itemID, err = normalizeIdentifier(itemID, maxIDRunes, true); err != nil {
		return nil, err
	}
	if input.Status == nil && !patchHasOwnerFields(input) {
		return nil, ErrInvalid
	}
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Status changes participate in the same dependency-graph serialization
	// order as edge creation. Taking the graph lock before any work-item row
	// lock prevents both write skew and lock-order deadlocks.
	if input.Status != nil {
		if err := lockDependencyGraphTx(ctx, tx); err != nil {
			return nil, err
		}
	}
	constraints := rbac.ResourceConstraintsFor(principal)
	current, err := scanItem(tx.QueryRow(ctx, `
		SELECT `+itemColumns+`
		FROM work_items w
		JOIN channels c ON c.id=w.channel_id AND c.delete_at=0
		WHERE w.id=$1 AND w.delete_at=0
		  AND (NOT $2::boolean OR cardinality($3::text[])=0 OR w.team_id=ANY($3::text[]))
		  AND (NOT $2::boolean OR cardinality($4::text[])=0 OR w.channel_id=ANY($4::text[]))
		FOR UPDATE OF w, c
	`, itemID, constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var stillMember bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM channel_members cm
			JOIN users actor ON actor.id=cm.user_id AND actor.delete_at=0
			  AND (
				NOT ('system_guest'=ANY(regexp_split_to_array(BTRIM(actor.roles), E'\\s+')))
				OR actor.guest_expires_at>$3
			  )
			WHERE cm.channel_id=$1 AND cm.user_id=$2
		)
	`, current.ChannelID, actorID, time.Now().UnixMilli()).Scan(&stillMember); err != nil {
		return nil, err
	}
	if !stillMember {
		return nil, ErrNotFound
	}
	owner := current.CreatedBy == actorID
	assignee := current.Kind == KindTask && current.AssigneeID == actorID
	reviewer := current.Kind == KindDecision && current.ReviewerID == actorID
	if !owner && !assignee && !reviewer {
		if current.Kind == KindTask {
			// Personal tasks are listed only to their creator and current
			// assignee. Make guessed IDs indistinguishable from missing rows.
			return nil, ErrNotFound
		}
		return nil, ErrForbidden
	}
	if !owner && patchHasOwnerFields(input) {
		return nil, ErrForbidden
	}
	previousStatus := current.Status
	previousAssignee := current.AssigneeID
	previousReviewer := current.ReviewerID
	if input.Title != nil {
		value, err := normalizeText(*input.Title, maxTitleRunes)
		if err != nil {
			return nil, err
		}
		current.Title = value
	}
	if input.Description != nil {
		value, err := normalizeOptionalText(*input.Description, maxBodyRunes)
		if err != nil {
			return nil, err
		}
		current.Description = value
	}
	if input.Status != nil {
		value := strings.TrimSpace(*input.Status)
		if !validStatus(current.Kind, value) {
			return nil, ErrInvalid
		}
		if !validTransition(current.Kind, current.Status, value) {
			return nil, ErrTransition
		}
		if current.Kind == KindTask && current.Status != value &&
			(current.Status == StatusDone || current.Status == StatusCancelled) &&
			value != StatusDone && value != StatusCancelled {
			blocked, blockErr := hasStartedDependentsTx(ctx, tx, current.ID)
			if blockErr != nil {
				return nil, blockErr
			}
			if blocked {
				return nil, ErrBlocked
			}
		}
		if current.Kind == KindTask && current.Status != value && (value == StatusInProgress || value == StatusDone) {
			blocked, blockErr := hasIncompleteDependenciesTx(ctx, tx, current.ID)
			if blockErr != nil {
				return nil, blockErr
			}
			if blocked {
				return nil, ErrBlocked
			}
		}
		current.Status = value
	}
	if input.DueAt != nil {
		if current.Kind != KindTask || *input.DueAt < 0 {
			return nil, ErrInvalid
		}
		current.DueAt = *input.DueAt
	}
	if input.Priority != nil {
		if current.Kind != KindTask {
			return nil, ErrInvalid
		}
		value := strings.TrimSpace(*input.Priority)
		if !validPriority(value) {
			return nil, ErrInvalid
		}
		current.Priority = value
	}
	if input.AssigneeID != nil {
		if current.Kind != KindTask {
			return nil, ErrInvalid
		}
		previousAssigneeID := current.AssigneeID
		candidate, err := normalizeIdentifier(*input.AssigneeID, maxIDRunes, false)
		if err != nil {
			return nil, err
		}
		if candidate == "" {
			if current.AssigneeID != "" {
				current.PreviousAssigneeID = current.AssigneeID
			}
			current.AssigneeID = ""
		} else {
			member, memberErr := activeMemberTx(ctx, tx, current.ChannelID, candidate)
			if memberErr != nil {
				return nil, memberErr
			}
			if !member {
				return nil, ErrForbidden
			}
			if current.AssigneeID != "" && current.AssigneeID != candidate {
				current.PreviousAssigneeID = current.AssigneeID
			}
			current.AssigneeID = candidate
		}
		current.AssigneeChanged = previousAssigneeID != current.AssigneeID
	}
	if input.ReviewerID != nil {
		if current.Kind != KindDecision {
			return nil, ErrInvalid
		}
		candidate, normalizeErr := normalizeIdentifier(*input.ReviewerID, maxIDRunes, false)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		if candidate != "" {
			member, memberErr := activeMemberTx(ctx, tx, current.ChannelID, candidate)
			if memberErr != nil {
				return nil, memberErr
			}
			if !member {
				return nil, ErrForbidden
			}
		}
		current.ReviewerID = candidate
		current.ReviewerChanged = previousReviewer != current.ReviewerID
	}
	if input.RecurrenceUnit != nil {
		if current.Kind != KindTask {
			return nil, ErrInvalid
		}
		current.RecurrenceUnit = strings.TrimSpace(*input.RecurrenceUnit)
	}
	if input.RecurrenceInterval != nil {
		if current.Kind != KindTask {
			return nil, ErrInvalid
		}
		current.RecurrenceInterval = *input.RecurrenceInterval
	}
	if !validRecurrence(current.RecurrenceUnit, current.RecurrenceInterval) || (current.RecurrenceUnit != RecurrenceNone && current.DueAt == 0) {
		return nil, ErrInvalid
	}
	if current.RecurrenceUnit != RecurrenceNone && current.SeriesID == "" {
		current.SeriesID = current.ID
	}
	if current.RecurrenceUnit == RecurrenceNone {
		current.SeriesID = ""
		current.OccurrenceNo = 0
	}
	now := time.Now().UnixMilli()
	if current.Kind == KindTask {
		if previousStatus != StatusDone && current.Status == StatusDone {
			current.CompletedAt = now
		} else if previousStatus == StatusDone && current.Status != StatusDone {
			current.CompletedAt = 0
		}
	} else {
		if previousStatus != StatusRecorded && current.Status == StatusRecorded {
			current.DecidedAt = now
		} else if current.Status == StatusProposed || current.Status == StatusUnderReview {
			current.DecidedAt = 0
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE work_items AS w
		SET title=$1, description=$2, status=$3, assignee_id=$4, due_at=$5,
		    priority=$6, completed_at=$7, reviewer_id=$8, decided_at=$9,
		    recurrence_unit=$10, recurrence_interval=$11, series_id=$12,
		    occurrence_no=$13, update_at=$14
		WHERE w.id=$15
		  AND EXISTS (
			SELECT 1 FROM channels c
			WHERE c.id=w.channel_id AND c.delete_at=0
		  )
	`, current.Title, current.Description, current.Status, nullableString(current.AssigneeID), current.DueAt,
		current.Priority, current.CompletedAt, nullableString(current.ReviewerID), current.DecidedAt,
		current.RecurrenceUnit, current.RecurrenceInterval, nullableString(current.SeriesID),
		current.OccurrenceNo, now, current.ID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	current.UpdateAt = now
	if previousStatus != current.Status {
		if err := insertEventTx(ctx, tx, current.ID, actorID, "status_changed", previousStatus, current.Status, nil, now); err != nil {
			return nil, err
		}
	}
	if previousAssignee != current.AssigneeID {
		if err := insertEventTx(ctx, tx, current.ID, actorID, "assigned", "", "", map[string]any{"assignee_id": current.AssigneeID}, now); err != nil {
			return nil, err
		}
	}
	if previousReviewer != current.ReviewerID {
		if err := insertEventTx(ctx, tx, current.ID, actorID, "reviewer_changed", "", "", map[string]any{"reviewer_id": current.ReviewerID}, now); err != nil {
			return nil, err
		}
	}
	if previousStatus == current.Status && previousAssignee == current.AssigneeID && previousReviewer == current.ReviewerID {
		if err := insertEventTx(ctx, tx, current.ID, actorID, "updated", "", "", nil, now); err != nil {
			return nil, err
		}
	}
	if current.Kind == KindTask && previousStatus != StatusDone && current.Status == StatusDone && current.RecurrenceUnit != RecurrenceNone {
		spawned, spawnErr := spawnNextRecurrenceTx(ctx, tx, current, actorID, now)
		if spawnErr != nil {
			return nil, spawnErr
		}
		current.SpawnedItem = spawned
	}
	if err := hydrateRelationsTx(ctx, tx, []*Item{current}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return current, nil
}

func (s *Service) Delete(ctx context.Context, actorID, itemID string) (*Item, error) {
	return s.DeleteForPrincipal(ctx, rbac.UserPrincipal(actorID), itemID)
}

// DeleteForPrincipal applies credential constraints in the same atomic update
// that enforces creator ownership and current channel membership.
func (s *Service) DeleteForPrincipal(ctx context.Context, principal rbac.Principal, itemID string) (*Item, error) {
	actorID := principal.UserID
	var err error
	if actorID, err = normalizeIdentifier(actorID, maxIDRunes, true); err != nil {
		return nil, err
	}
	if itemID, err = normalizeIdentifier(itemID, maxIDRunes, true); err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	constraints := rbac.ResourceConstraintsFor(principal)
	item, err := scanItem(s.db.Pool.QueryRow(ctx, `
		UPDATE work_items AS w SET delete_at=$1, update_at=$1
		WHERE w.id=$2 AND w.created_by=$3 AND w.delete_at=0
		  AND (NOT $4::boolean OR cardinality($5::text[])=0 OR w.team_id=ANY($5::text[]))
		  AND (NOT $4::boolean OR cardinality($6::text[])=0 OR w.channel_id=ANY($6::text[]))
		  AND EXISTS (
			SELECT 1 FROM channels c
			WHERE c.id=w.channel_id AND c.delete_at=0
		  )
		  AND EXISTS (
			SELECT 1 FROM channel_members cm
			JOIN users actor ON actor.id=cm.user_id AND actor.delete_at=0
			  AND (
				NOT ('system_guest'=ANY(regexp_split_to_array(BTRIM(actor.roles), E'\\s+')))
				OR actor.guest_expires_at>$7
			  )
			WHERE cm.channel_id=w.channel_id AND cm.user_id=$3
		  )
		RETURNING `+itemColumns+`
	`, now, itemID, actorID, constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs, time.Now().UnixMilli()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("delete work item: %w", err)
	}
	return item, nil
}
