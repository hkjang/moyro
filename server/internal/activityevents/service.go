// Package activityevents provides the durable, user-scoped read model behind
// Moyro's integrated activity inbox. Producers submit an intentionally small
// set of display fields; arbitrary source payloads are never persisted here.
package activityevents

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
)

const (
	DefaultPageSize = 50
	MaxPageSize     = 100
	MaxBulkReadIDs  = 200
)

var (
	ErrInvalid       = errors.New("activity event is invalid")
	ErrInvalidCursor = errors.New("activity event cursor is invalid")
	ErrNotFound      = errors.New("activity event not found")
)

type EventType string

const (
	TypeMention           EventType = "mention"
	TypeThreadReply       EventType = "thread_reply"
	TypeDirectMessage     EventType = "direct_message"
	TypeApprovalRequested EventType = "approval_requested"
	TypeDecided           EventType = "decided"
	TypeReminderFired     EventType = "reminder_fired"
	TypeTaskAssigned      EventType = "task_assigned"
	TypeSystemWarning     EventType = "system_warning"
	TypePluginEvent       EventType = "plugin_event"
)

var supportedEventTypes = map[EventType]struct{}{
	TypeMention:           {},
	TypeThreadReply:       {},
	TypeDirectMessage:     {},
	TypeApprovalRequested: {},
	TypeDecided:           {},
	TypeReminderFired:     {},
	TypeTaskAssigned:      {},
	TypeSystemWarning:     {},
	TypePluginEvent:       {},
}

// ParseEventType is the shared allow-list boundary for emitters and filters.
func ParseEventType(raw string) (EventType, error) {
	typ := EventType(strings.ToLower(strings.TrimSpace(raw)))
	if _, ok := supportedEventTypes[typ]; !ok {
		return "", fmt.Errorf("%w: unsupported type %q", ErrInvalid, raw)
	}
	return typ, nil
}

// Event contains the persisted row. UserID and DedupeKey are deliberately
// excluded from JSON as defense in depth; HTTP adapters still use an explicit
// allow-list view instead of serializing this domain value directly.
type Event struct {
	ID           string    `json:"id"`
	UserID       string    `json:"-"`
	Type         EventType `json:"type"`
	ActorID      string    `json:"actor_id,omitempty"`
	TeamID       string    `json:"team_id,omitempty"`
	ChannelID    string    `json:"channel_id,omitempty"`
	PostID       string    `json:"post_id,omitempty"`
	ResourceType string    `json:"resource_type,omitempty"`
	ResourceID   string    `json:"resource_id,omitempty"`
	Title        string    `json:"title"`
	Summary      string    `json:"summary,omitempty"`
	DedupeKey    string    `json:"-"`
	CreateAt     int64     `json:"create_at"`
	UpdateAt     int64     `json:"update_at"`
	ReadAt       int64     `json:"read_at"`
	CompletedAt  int64     `json:"completed_at"`
	SnoozedUntil int64     `json:"snoozed_until"`
}

// EmitInput is intentionally not a generic payload. Producers must select the
// safe text and identifiers that a recipient is allowed to see.
type EmitInput struct {
	UserID       string
	Type         EventType
	DedupeKey    string
	ActorID      string
	TeamID       string
	ChannelID    string
	PostID       string
	ResourceType string
	ResourceID   string
	Title        string
	Summary      string
}

// Emitter is the narrow dependency future post, approval, reminder, task and
// plugin producers should accept.
type Emitter interface {
	Emit(context.Context, EmitInput) (*Event, error)
}

type ListOptions struct {
	Cursor     string
	Limit      int
	UnreadOnly bool
	Types      []EventType
}

type Page struct {
	Events     []Event
	NextCursor string
}

type StatePatch struct {
	Read         *bool
	Completed    *bool
	SnoozedUntil *int64
}

// ApprovalReviewAuthorizer revalidates whether a user may still review the
// approval request referenced by an approval_review activity. The approval
// package owns the canonical policy/RBAC decision; activityevents only uses
// the result to keep a stale inbox row from becoming an authorization bypass.
type ApprovalReviewAuthorizer func(context.Context, string, string) (bool, error)

type Service struct {
	db        *store.DB
	nowMS     func() int64
	canReview ApprovalReviewAuthorizer
}

func New(db *store.DB) *Service {
	return &Service{db: db, nowMS: func() int64 { return time.Now().UnixMilli() }}
}

// SetApprovalReviewAuthorizer wires the approval service's canonical review
// decision during application startup. An unwired authorizer fails closed for
// approval_review rows while all other activity types remain available.
func (s *Service) SetApprovalReviewAuthorizer(authorizer ApprovalReviewAuthorizer) {
	s.canReview = authorizer
}

var _ Emitter = (*Service)(nil)

const eventColumns = `
	id, user_id, event_type, actor_id, team_id, channel_id, post_id,
	resource_type, resource_id, title, summary, dedupe_key,
	create_at, update_at, read_at, completed_at, snoozed_until`

// activityLiveAccessPredicate is applied at every read and state-mutation
// boundary. Channel-scoped rows require an active channel plus current channel
// membership. Team-backed channels additionally require an active team and
// current team membership, even for older events whose team_id was omitted.
// Team-only rows require the equivalent live team access.
const activityLiveAccessPredicate = `
	(
		(
			activity_events.channel_id<>''
			AND EXISTS (
				SELECT 1
				FROM channel_members cm
				JOIN channels c ON c.id=cm.channel_id AND c.delete_at=0
				WHERE cm.channel_id=activity_events.channel_id
				  AND cm.user_id=$1
				  AND (activity_events.team_id='' OR COALESCE(c.team_id,'')=activity_events.team_id)
				  AND (
					COALESCE(c.team_id,'')=''
					OR EXISTS (
						SELECT 1
						FROM team_members tm
						JOIN teams t ON t.id=tm.team_id AND t.delete_at=0
						WHERE tm.team_id=c.team_id AND tm.user_id=$1
					)
				  )
			)
		)
		OR (
			activity_events.channel_id=''
			AND (
				activity_events.team_id=''
				OR EXISTS (
					SELECT 1
					FROM team_members tm
					JOIN teams t ON t.id=tm.team_id AND t.delete_at=0
					WHERE tm.team_id=activity_events.team_id AND tm.user_id=$1
				)
			)
		)
	)`

func (s *Service) Emit(ctx context.Context, input EmitInput) (*Event, error) {
	if err := normalizeEmitInput(&input); err != nil {
		return nil, err
	}
	now := s.nowMS()
	if now <= 0 {
		return nil, fmt.Errorf("%w: invalid server clock", ErrInvalid)
	}
	event := Event{
		ID: uuid.NewString(), UserID: input.UserID, Type: input.Type,
		ActorID: input.ActorID, TeamID: input.TeamID, ChannelID: input.ChannelID,
		PostID: input.PostID, ResourceType: input.ResourceType, ResourceID: input.ResourceID,
		Title: input.Title, Summary: input.Summary, DedupeKey: input.DedupeKey,
		CreateAt: now, UpdateAt: now,
	}

	// The no-op conflict update gives concurrent/replayed emitters the original
	// immutable row in one statement without replacing its first-seen content.
	row := s.db.Pool.QueryRow(ctx, `
		INSERT INTO activity_events (
			id, user_id, event_type, actor_id, team_id, channel_id, post_id,
			resource_type, resource_id, title, summary, dedupe_key,
			create_at, update_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13)
		ON CONFLICT (user_id, event_type, dedupe_key) DO UPDATE
		SET dedupe_key=EXCLUDED.dedupe_key
		RETURNING `+eventColumns,
		event.ID, event.UserID, event.Type, event.ActorID, event.TeamID,
		event.ChannelID, event.PostID, event.ResourceType, event.ResourceID,
		event.Title, event.Summary, event.DedupeKey, event.CreateAt,
	)
	return scanEvent(row)
}

func (s *Service) List(ctx context.Context, userID string, options ListOptions) (Page, error) {
	return s.ListForPrincipal(ctx, rbac.UserPrincipal(userID), options)
}

func (s *Service) ListForPrincipal(ctx context.Context, principal rbac.Principal, options ListOptions) (Page, error) {
	userID := principal.UserID
	userID = strings.TrimSpace(userID)
	if err := validateSized("user_id", userID, 128, true); err != nil {
		return Page{}, err
	}
	limit := options.Limit
	if limit <= 0 {
		limit = DefaultPageSize
	}
	if limit > MaxPageSize {
		limit = MaxPageSize
	}
	types := make([]string, 0, len(options.Types))
	seenTypes := make(map[EventType]struct{}, len(options.Types))
	for _, typ := range options.Types {
		parsed, err := ParseEventType(string(typ))
		if err != nil {
			return Page{}, err
		}
		if _, duplicate := seenTypes[parsed]; duplicate {
			continue
		}
		seenTypes[parsed] = struct{}{}
		types = append(types, string(parsed))
	}

	cursor, hasCursor, err := decodeCursor(options.Cursor)
	if err != nil {
		return Page{}, err
	}
	constraints := rbac.ResourceConstraintsFor(principal)

	// Reviewer authorization is application-owned and cannot be folded into
	// this package's SQL without duplicating approval policy semantics. Fetch in
	// bounded batches so revoked review rows are filtered without leaving an
	// otherwise full page artificially short.
	batchLimit := limit + 1
	if batchLimit < DefaultPageSize+1 {
		batchLimit = DefaultPageSize + 1
	}
	if batchLimit > MaxPageSize+1 {
		batchLimit = MaxPageSize + 1
	}
	events := make([]Event, 0, limit+1)
	reviewCache := map[string]reviewAuthorizationResult{}
	position, positioned := cursor, hasCursor
	for len(events) < limit+1 {
		batch, queryErr := s.listBatch(ctx, userID, types, options.UnreadOnly, position, positioned, constraints, batchLimit)
		if queryErr != nil {
			return Page{}, queryErr
		}
		if len(batch) == 0 {
			break
		}
		for i := range batch {
			allowed, accessErr := s.reviewAccessAllowed(ctx, userID, &batch[i], reviewCache)
			if accessErr != nil {
				return Page{}, accessErr
			}
			if allowed {
				events = append(events, batch[i])
				if len(events) == limit+1 {
					break
				}
			}
		}
		if len(events) == limit+1 || len(batch) < batchLimit {
			break
		}
		last := batch[len(batch)-1]
		position = activityCursor{CreateAt: last.CreateAt, ID: last.ID}
		positioned = true
	}
	page := Page{Events: events}
	if len(events) > limit {
		page.Events = events[:limit]
		page.NextCursor, err = encodeCursor(page.Events[len(page.Events)-1])
		if err != nil {
			return Page{}, err
		}
	}
	return page, nil
}

func (s *Service) listBatch(
	ctx context.Context,
	userID string,
	types []string,
	unreadOnly bool,
	cursor activityCursor,
	hasCursor bool,
	constraints rbac.ResourceConstraints,
	limit int,
) ([]Event, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+eventColumns+`
		FROM activity_events
		WHERE user_id=$1
		  AND `+activityLiveAccessPredicate+`
		  AND (cardinality($2::text[]) = 0 OR event_type = ANY($2::text[]))
		  AND (NOT $3::boolean OR read_at=0)
		  AND (NOT $4::boolean OR (create_at, id) < ($5::bigint, $6::text))
		  AND (NOT $7::boolean OR cardinality($8::text[])=0 OR team_id=ANY($8::text[]))
		  AND (NOT $7::boolean OR cardinality($9::text[])=0 OR channel_id=ANY($9::text[]))
		ORDER BY create_at DESC, id DESC
		LIMIT $10
	`, userID, types, unreadOnly, hasCursor, cursor.CreateAt, cursor.ID,
		constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]Event, 0, limit)
	for rows.Next() {
		event, scanErr := scanEvent(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		events = append(events, *event)
	}
	return events, rows.Err()
}

type reviewAuthorizationResult struct {
	allowed bool
	err     error
}

func (s *Service) reviewAccessAllowed(
	ctx context.Context,
	userID string,
	event *Event,
	cache map[string]reviewAuthorizationResult,
) (bool, error) {
	if event == nil || event.ResourceType != "approval_review" {
		return event != nil, nil
	}
	requestID := strings.TrimSpace(event.ResourceID)
	if requestID == "" || s.canReview == nil {
		return false, nil
	}
	if cached, ok := cache[requestID]; ok {
		return cached.allowed, cached.err
	}
	allowed, err := s.canReview(ctx, userID, requestID)
	cache[requestID] = reviewAuthorizationResult{allowed: allowed, err: err}
	return allowed, err
}

func (s *Service) UpdateState(ctx context.Context, userID, eventID string, patch StatePatch) (*Event, error) {
	return s.UpdateStateForPrincipal(ctx, rbac.UserPrincipal(userID), eventID, patch)
}

func (s *Service) UpdateStateForPrincipal(ctx context.Context, principal rbac.Principal, eventID string, patch StatePatch) (*Event, error) {
	userID := principal.UserID
	userID = strings.TrimSpace(userID)
	eventID = strings.TrimSpace(eventID)
	if err := validateSized("user_id", userID, 128, true); err != nil {
		return nil, err
	}
	if err := validateSized("event_id", eventID, 128, true); err != nil {
		return nil, err
	}
	if patch.Read == nil && patch.Completed == nil && patch.SnoozedUntil == nil {
		return nil, fmt.Errorf("%w: at least one state field is required", ErrInvalid)
	}
	if patch.SnoozedUntil != nil && *patch.SnoozedUntil < 0 {
		return nil, fmt.Errorf("%w: snoozed_until cannot be negative", ErrInvalid)
	}
	now := s.nowMS()
	readValue := false
	if patch.Read != nil {
		readValue = *patch.Read
	}
	completedValue := false
	if patch.Completed != nil {
		completedValue = *patch.Completed
	}
	snoozedUntil := int64(0)
	if patch.SnoozedUntil != nil {
		snoozedUntil = *patch.SnoozedUntil
	}
	constraints := rbac.ResourceConstraintsFor(principal)
	existing, err := s.eventForPrincipal(ctx, userID, eventID, constraints)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	allowed, err := s.reviewAccessAllowed(ctx, userID, existing, map[string]reviewAuthorizationResult{})
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrNotFound
	}
	event, err := scanEvent(s.db.Pool.QueryRow(ctx, `
		UPDATE activity_events
		SET read_at = CASE WHEN $4::boolean THEN CASE WHEN $5::boolean THEN $3 ELSE 0 END ELSE read_at END,
		    completed_at = CASE WHEN $6::boolean THEN CASE WHEN $7::boolean THEN $3 ELSE 0 END ELSE completed_at END,
		    snoozed_until = CASE WHEN $8::boolean THEN $9 ELSE snoozed_until END,
		    update_at = $3
		WHERE user_id=$1 AND id=$2
		  AND `+activityLiveAccessPredicate+`
		  AND (NOT $10::boolean OR cardinality($11::text[])=0 OR team_id=ANY($11::text[]))
		  AND (NOT $10::boolean OR cardinality($12::text[])=0 OR channel_id=ANY($12::text[]))
		RETURNING `+eventColumns,
		userID, eventID, now,
		patch.Read != nil, readValue,
		patch.Completed != nil, completedValue,
		patch.SnoozedUntil != nil, snoozedUntil,
		constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return event, err
}

// MarkRead updates up to MaxBulkReadIDs events, always constrained to the
// authenticated owner. Foreign and missing IDs are indistinguishable no-ops.
func (s *Service) MarkRead(ctx context.Context, userID string, eventIDs []string) (int64, error) {
	return s.MarkReadForPrincipal(ctx, rbac.UserPrincipal(userID), eventIDs)
}

func (s *Service) MarkReadForPrincipal(ctx context.Context, principal rbac.Principal, eventIDs []string) (int64, error) {
	userID := principal.UserID
	userID = strings.TrimSpace(userID)
	if err := validateSized("user_id", userID, 128, true); err != nil {
		return 0, err
	}
	ids, err := normalizeEventIDs(eventIDs)
	if err != nil {
		return 0, err
	}
	now := s.nowMS()
	constraints := rbac.ResourceConstraintsFor(principal)
	events, err := s.eventsForPrincipal(ctx, userID, ids, constraints)
	if err != nil {
		return 0, err
	}
	allowedIDs := make([]string, 0, len(events))
	reviewCache := map[string]reviewAuthorizationResult{}
	for i := range events {
		allowed, accessErr := s.reviewAccessAllowed(ctx, userID, &events[i], reviewCache)
		if accessErr != nil {
			return 0, accessErr
		}
		if allowed {
			allowedIDs = append(allowedIDs, events[i].ID)
		}
	}
	if len(allowedIDs) == 0 {
		return 0, nil
	}
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE activity_events
		SET read_at=$3, update_at=$3
		WHERE user_id=$1 AND id = ANY($2::text[]) AND read_at=0
		  AND `+activityLiveAccessPredicate+`
		  AND (NOT $4::boolean OR cardinality($5::text[])=0 OR team_id=ANY($5::text[]))
		  AND (NOT $4::boolean OR cardinality($6::text[])=0 OR channel_id=ANY($6::text[]))
	`, userID, allowedIDs, now, constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *Service) eventForPrincipal(
	ctx context.Context,
	userID string,
	eventID string,
	constraints rbac.ResourceConstraints,
) (*Event, error) {
	return scanEvent(s.db.Pool.QueryRow(ctx, `
		SELECT `+eventColumns+`
		FROM activity_events
		WHERE user_id=$1 AND id=$2
		  AND `+activityLiveAccessPredicate+`
		  AND (NOT $3::boolean OR cardinality($4::text[])=0 OR team_id=ANY($4::text[]))
		  AND (NOT $3::boolean OR cardinality($5::text[])=0 OR channel_id=ANY($5::text[]))
	`, userID, eventID, constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs))
}

func (s *Service) eventsForPrincipal(
	ctx context.Context,
	userID string,
	eventIDs []string,
	constraints rbac.ResourceConstraints,
) ([]Event, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+eventColumns+`
		FROM activity_events
		WHERE user_id=$1 AND id=ANY($2::text[])
		  AND `+activityLiveAccessPredicate+`
		  AND (NOT $3::boolean OR cardinality($4::text[])=0 OR team_id=ANY($4::text[]))
		  AND (NOT $3::boolean OR cardinality($5::text[])=0 OR channel_id=ANY($5::text[]))
	`, userID, eventIDs, constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]Event, 0, len(eventIDs))
	for rows.Next() {
		event, scanErr := scanEvent(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		events = append(events, *event)
	}
	return events, rows.Err()
}

func normalizeEmitInput(input *EmitInput) error {
	if input == nil {
		return fmt.Errorf("%w: nil input", ErrInvalid)
	}
	parsed, err := ParseEventType(string(input.Type))
	if err != nil {
		return err
	}
	input.Type = parsed
	input.UserID = strings.TrimSpace(input.UserID)
	input.DedupeKey = strings.TrimSpace(input.DedupeKey)
	input.ActorID = strings.TrimSpace(input.ActorID)
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.ChannelID = strings.TrimSpace(input.ChannelID)
	input.PostID = strings.TrimSpace(input.PostID)
	input.ResourceType = strings.TrimSpace(input.ResourceType)
	input.ResourceID = strings.TrimSpace(input.ResourceID)
	input.Title = strings.TrimSpace(input.Title)

	for _, field := range []struct {
		name     string
		value    string
		max      int
		required bool
	}{
		{name: "user_id", value: input.UserID, max: 128, required: true},
		{name: "dedupe_key", value: input.DedupeKey, max: 256, required: true},
		{name: "actor_id", value: input.ActorID, max: 128},
		{name: "team_id", value: input.TeamID, max: 128},
		{name: "channel_id", value: input.ChannelID, max: 128},
		{name: "post_id", value: input.PostID, max: 128},
		{name: "resource_type", value: input.ResourceType, max: 64},
		{name: "resource_id", value: input.ResourceID, max: 128},
		{name: "title", value: input.Title, max: 256, required: true},
		{name: "summary", value: input.Summary, max: 4096},
	} {
		if err := validateSized(field.name, field.value, field.max, field.required); err != nil {
			return err
		}
	}
	return nil
}

func validateSized(name, value string, max int, required bool) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s is not valid UTF-8", ErrInvalid, name)
	}
	length := utf8.RuneCountInString(value)
	if required && length == 0 {
		return fmt.Errorf("%w: %s is required", ErrInvalid, name)
	}
	if length > max {
		return fmt.Errorf("%w: %s is too long", ErrInvalid, name)
	}
	return nil
}

func normalizeEventIDs(raw []string) ([]string, error) {
	if len(raw) == 0 || len(raw) > MaxBulkReadIDs {
		return nil, fmt.Errorf("%w: ids must contain between 1 and %d values", ErrInvalid, MaxBulkReadIDs)
	}
	ids := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, value := range raw {
		id := strings.TrimSpace(value)
		if err := validateSized("event_id", id, 128, true); err != nil {
			return nil, err
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

type activityCursor struct {
	CreateAt int64  `json:"create_at"`
	ID       string `json:"id"`
}

func encodeCursor(event Event) (string, error) {
	if event.CreateAt <= 0 || strings.TrimSpace(event.ID) == "" {
		return "", fmt.Errorf("%w: incomplete event position", ErrInvalidCursor)
	}
	raw, err := json.Marshal(activityCursor{CreateAt: event.CreateAt, ID: event.ID})
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeCursor(raw string) (activityCursor, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return activityCursor{}, false, nil
	}
	if len(raw) > 1024 {
		return activityCursor{}, false, ErrInvalidCursor
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return activityCursor{}, false, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	var cursor activityCursor
	if err := decoder.Decode(&cursor); err != nil {
		return activityCursor{}, false, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return activityCursor{}, false, ErrInvalidCursor
	}
	if cursor.CreateAt <= 0 || strings.TrimSpace(cursor.ID) != cursor.ID {
		return activityCursor{}, false, ErrInvalidCursor
	}
	if err := validateSized("cursor id", cursor.ID, 128, true); err != nil {
		return activityCursor{}, false, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	return cursor, true, nil
}

type scannable interface {
	Scan(...any) error
}

func scanEvent(row scannable) (*Event, error) {
	var event Event
	err := row.Scan(
		&event.ID, &event.UserID, &event.Type, &event.ActorID, &event.TeamID,
		&event.ChannelID, &event.PostID, &event.ResourceType, &event.ResourceID,
		&event.Title, &event.Summary, &event.DedupeKey, &event.CreateAt,
		&event.UpdateAt, &event.ReadAt, &event.CompletedAt, &event.SnoozedUntil,
	)
	return &event, err
}
