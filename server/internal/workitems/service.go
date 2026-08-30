// Package workitems stores tasks and decisions that are derived from a
// conversation.  It deliberately keeps the source post and channel on every
// row so authorization can be re-evaluated instead of trusting client-supplied
// display metadata.
package workitems

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

	StatusOpen       = "open"
	StatusInProgress = "in_progress"
	StatusDone       = "done"
	StatusCancelled  = "cancelled"
	StatusRecorded   = "recorded"
	StatusSuperseded = "superseded"

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
	IdempotencyKey string `json:"-"`
	DueAt          int64  `json:"due_at"`
	DecidedAt      int64  `json:"decided_at"`
	CreateAt       int64  `json:"create_at"`
	UpdateAt       int64  `json:"update_at"`
	DeleteAt       int64  `json:"delete_at"`
	// PreviousAssigneeID is populated only by Patch when reassignment removes
	// another viewer. It is websocket fan-out metadata and must never leave the
	// server in JSON responses.
	PreviousAssigneeID string `json:"-"`
	// AssigneeChanged lets adapters emit assignment notifications only for a
	// real ownership transition. It is transient fan-out metadata.
	AssigneeChanged bool `json:"-"`
}

type CreateInput struct {
	Kind           string
	Title          string
	Description    string
	AssigneeID     string
	SourcePostID   string
	DueAt          int64
	IdempotencyKey string
}

type PatchInput struct {
	Title       *string
	Description *string
	Status      *string
	AssigneeID  *string
	DueAt       *int64
}

type ListOptions struct {
	Kind     string
	Status   string
	Cursor   string
	PageSize int
}

type Page struct {
	Items      []Item `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

type cursor struct {
	CreateAt int64  `json:"create_at"`
	ID       string `json:"id"`
}

const itemColumns = `
	w.id, w.kind, w.title, w.description, w.status, w.created_by,
	COALESCE(w.assignee_id,''), COALESCE(w.team_id,''), w.channel_id,
	COALESCE(w.source_post_id,''), COALESCE(w.source_thread_id,''),
	w.idempotency_key, w.due_at, w.decided_at, w.create_at, w.update_at, w.delete_at`

type scanner interface{ Scan(...any) error }

func scanItem(row scanner) (*Item, error) {
	var item Item
	err := row.Scan(
		&item.ID, &item.Kind, &item.Title, &item.Description, &item.Status,
		&item.CreatedBy, &item.AssigneeID, &item.TeamID, &item.ChannelID,
		&item.SourcePostID, &item.SourceThreadID, &item.IdempotencyKey,
		&item.DueAt, &item.DecidedAt, &item.CreateAt, &item.UpdateAt,
		&item.DeleteAt,
	)
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
		return status == StatusRecorded || status == StatusSuperseded || status == StatusCancelled
	default:
		return false
	}
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
	if input.IdempotencyKey, err = normalizeIdentifier(input.IdempotencyKey, maxKeyRunes, true); err != nil {
		return nil, false, err
	}
	if !validKind(input.Kind) || input.DueAt < 0 {
		return nil, false, ErrInvalid
	}
	if input.Title, err = normalizeText(input.Title, maxTitleRunes); err != nil {
		return nil, false, err
	}
	if input.Description, err = normalizeOptionalText(input.Description, maxBodyRunes); err != nil {
		return nil, false, err
	}
	if input.Kind == KindDecision && (input.AssigneeID != "" || input.DueAt != 0) {
		return nil, false, ErrInvalid
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
		WHERE p.id=$2 AND p.delete_at=0
		FOR SHARE OF p, c, cm, actor
	`, actorID, input.SourcePostID).Scan(&channelID, &teamID, &rootID)
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
		var member bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1
				FROM channel_members cm
				JOIN users u ON u.id=cm.user_id AND u.delete_at=0
				WHERE cm.channel_id=$1 AND cm.user_id=$2
			)
		`, channelID, input.AssigneeID).Scan(&member); err != nil {
			return nil, false, err
		}
		if !member {
			return nil, false, ErrForbidden
		}
	}

	now := time.Now().UnixMilli()
	item := &Item{
		ID: uuid.NewString(), Kind: input.Kind, Title: input.Title,
		Description: input.Description, CreatedBy: actorID,
		AssigneeID: input.AssigneeID, TeamID: teamID, ChannelID: channelID,
		SourcePostID: input.SourcePostID, IdempotencyKey: input.IdempotencyKey,
		DueAt: input.DueAt, CreateAt: now, UpdateAt: now,
	}
	if rootID != "" {
		item.SourceThreadID = rootID
	}
	if item.Kind == KindTask {
		item.Status = StatusOpen
	} else {
		item.Status = StatusRecorded
		item.DecidedAt = now
	}
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
			idempotency_key, due_at, decided_at, create_at, update_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15)
		ON CONFLICT (created_by, idempotency_key) DO NOTHING
	`, item.ID, item.Kind, item.Title, item.Description, item.Status, item.CreatedBy,
		assignee, team, item.ChannelID, item.SourcePostID, thread,
		item.IdempotencyKey, item.DueAt, item.DecidedAt, now)
	if err != nil {
		return nil, false, err
	}
	if tag.RowsAffected() == 0 {
		existing, err := scanItem(tx.QueryRow(ctx, `SELECT `+itemColumns+` FROM work_items w WHERE w.created_by=$1 AND w.idempotency_key=$2`, actorID, input.IdempotencyKey))
		if err != nil {
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
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return item, false, nil
}

func sameCreateRequest(existing, requested *Item) bool {
	return existing != nil && requested != nil &&
		existing.Kind == requested.Kind &&
		existing.Title == requested.Title &&
		existing.Description == requested.Description &&
		existing.AssigneeID == requested.AssigneeID &&
		existing.TeamID == requested.TeamID &&
		existing.ChannelID == requested.ChannelID &&
		existing.SourcePostID == requested.SourcePostID &&
		existing.SourceThreadID == requested.SourceThreadID &&
		existing.DueAt == requested.DueAt
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
	if decoded.CreateAt <= 0 || strings.TrimSpace(decoded.ID) != decoded.ID {
		return cursor{}, ErrInvalid
	}
	if _, err := normalizeIdentifier(decoded.ID, maxIDRunes, true); err != nil {
		return cursor{}, ErrInvalid
	}
	return decoded, nil
}

func encodeCursor(item Item) string {
	raw, _ := json.Marshal(cursor{CreateAt: item.CreateAt, ID: item.ID})
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
	if options.Kind != "" && !validKind(options.Kind) {
		return nil, ErrInvalid
	}
	if options.Status != "" && options.Kind == "" {
		return nil, ErrInvalid
	}
	if options.Status != "" && !validStatus(options.Kind, options.Status) {
		return nil, ErrInvalid
	}
	position, err := decodeCursor(options.Cursor)
	if err != nil {
		return nil, err
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
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+itemColumns+`
		FROM work_items w
		JOIN channel_members cm ON cm.channel_id=w.channel_id AND cm.user_id=$1
		JOIN channels c ON c.id=w.channel_id AND c.delete_at=0
		WHERE w.delete_at=0
		  AND (w.created_by=$1 OR w.assignee_id=$1 OR w.kind='decision')
		  AND ($2='' OR w.kind=$2)
		  AND ($3='' OR w.status=$3)
		  AND ($4::bigint=0 OR (w.create_at, w.id) < ($4, $5))
		  AND (NOT $6::boolean OR cardinality($7::text[])=0 OR w.team_id=ANY($7::text[]))
		  AND (NOT $6::boolean OR cardinality($8::text[])=0 OR w.channel_id=ANY($8::text[]))
		ORDER BY w.create_at DESC, w.id DESC
		LIMIT $9
	`, userID, options.Kind, options.Status, position.CreateAt, position.ID,
		constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs, pageSize+1)
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
		page.NextCursor = encodeCursor(page.Items[pageSize-1])
		page.Items = page.Items[:pageSize]
	}
	return page, nil
}

func patchHasOwnerFields(input PatchInput) bool {
	return input.Title != nil || input.Description != nil || input.AssigneeID != nil || input.DueAt != nil
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
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM channel_members WHERE channel_id=$1 AND user_id=$2)`, current.ChannelID, actorID).Scan(&stillMember); err != nil {
		return nil, err
	}
	if !stillMember {
		return nil, ErrNotFound
	}
	owner := current.CreatedBy == actorID
	assignee := current.Kind == KindTask && current.AssigneeID == actorID
	if !owner && !assignee {
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
		current.Status = value
	}
	if input.DueAt != nil {
		if current.Kind != KindTask || *input.DueAt < 0 {
			return nil, ErrInvalid
		}
		current.DueAt = *input.DueAt
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
			var member bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS(
					SELECT 1
					FROM channel_members cm
					JOIN users u ON u.id=cm.user_id AND u.delete_at=0
					WHERE cm.channel_id=$1 AND cm.user_id=$2
				)
			`, current.ChannelID, candidate).Scan(&member); err != nil {
				return nil, err
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
	var assigned any
	if current.AssigneeID != "" {
		assigned = current.AssigneeID
	}
	now := time.Now().UnixMilli()
	tag, err := tx.Exec(ctx, `
		UPDATE work_items AS w
		SET title=$1, description=$2, status=$3, assignee_id=$4, due_at=$5, update_at=$6
		WHERE w.id=$7
		  AND EXISTS (
			SELECT 1 FROM channels c
			WHERE c.id=w.channel_id AND c.delete_at=0
		  )
	`, current.Title, current.Description, current.Status, assigned, current.DueAt, now, current.ID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	current.UpdateAt = now
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
		UPDATE work_items SET delete_at=$1, update_at=$1
		WHERE id=$2 AND created_by=$3 AND delete_at=0
		  AND (NOT $4::boolean OR cardinality($5::text[])=0 OR work_items.team_id=ANY($5::text[]))
		  AND (NOT $4::boolean OR cardinality($6::text[])=0 OR work_items.channel_id=ANY($6::text[]))
		  AND EXISTS (
			SELECT 1 FROM channels c
			WHERE c.id=work_items.channel_id AND c.delete_at=0
		  )
		  AND EXISTS (
			SELECT 1 FROM channel_members cm
			WHERE cm.channel_id=work_items.channel_id AND cm.user_id=$3
		  )
		RETURNING id, kind, title, description, status, created_by,
			COALESCE(assignee_id,''), COALESCE(team_id,''), channel_id,
			COALESCE(source_post_id,''), COALESCE(source_thread_id,''),
			idempotency_key, due_at, decided_at, create_at, update_at, delete_at
	`, now, itemID, actorID, constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("delete work item: %w", err)
	}
	return item, nil
}
