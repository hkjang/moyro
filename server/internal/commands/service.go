// Package commands implements custom (user-defined) slash commands. A command
// is a row in the `commands` table that registers a trigger word per team plus
// an external callback URL. When a user types /<trigger> in a channel that
// belongs to that team, the slashcmd router consults this service for a
// match; the callback then runs over HTTP and its JSON body is rendered like
// any other slash response.
//
// This package owns CRUD only. The HTTP fan-out to the callback URL is left
// to the caller (httpapi handlers) so policy decisions like host allowlists,
// timeout, retry, and content-type validation stay alongside the existing
// outgoing-webhook dispatcher rather than getting reimplemented here.
package commands

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/hkjang/moyro/server/internal/store"
)

var (
	// ErrNotFound is returned when a CRUD lookup misses. Handlers map to 404.
	ErrNotFound = errors.New("command not found")
	// ErrTriggerInvalid is returned when the trigger word fails the slug check.
	ErrTriggerInvalid = errors.New("invalid trigger word")
	// ErrDuplicateTrigger is returned when a (team, lower(trigger)) collision
	// is detected on Create / Update / Move. Handlers map to 409.
	ErrDuplicateTrigger = errors.New("duplicate trigger word")
)

// Method is the HTTP verb used when invoking the callback URL. We accept the
// Mattermost short codes ("P" / "G") so payloads round-trip without a
// translation layer.
type Method string

const (
	MethodPost Method = "P"
	MethodGet  Method = "G"
)

// Command is the on-the-wire shape of a custom command. Fields mirror the
// official Mattermost contract; intentionally no `team` slug because clients
// only ever pass team_id.
type Command struct {
	ID               string `json:"id"`
	TeamID           string `json:"team_id"`
	CreatorID        string `json:"creator_id"`
	Trigger          string `json:"trigger"`
	Method           string `json:"method"`
	URL              string `json:"url"`
	Username         string `json:"username"`
	IconURL          string `json:"icon_url"`
	AutoComplete     bool   `json:"auto_complete"`
	AutoCompleteDesc string `json:"auto_complete_desc"`
	AutoCompleteHint string `json:"auto_complete_hint"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description"`
	Token            string `json:"token,omitempty"`
	CreateAt         int64  `json:"create_at"`
	UpdateAt         int64  `json:"update_at"`
	DeleteAt         int64  `json:"delete_at"`
}

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

// CreateInput is the optional-fields-included shape used by Create. We don't
// expose `Token` here because it's always generated on the server.
type CreateInput struct {
	TeamID           string
	CreatorID        string
	Trigger          string
	Method           string
	URL              string
	Username         string
	IconURL          string
	AutoComplete     bool
	AutoCompleteDesc string
	AutoCompleteHint string
	DisplayName      string
	Description      string
}

// Create inserts a new custom command. The trigger word is normalised to
// lower-case (matching the partial unique index in the baseline migration) and validated
// before the INSERT so we don't burn an INSERT on a bad slug.
func (s *Service) Create(ctx context.Context, in CreateInput) (*Command, error) {
	trigger := strings.TrimPrefix(strings.TrimSpace(in.Trigger), "/")
	trigger = strings.ToLower(trigger)
	if !validTrigger(trigger) {
		return nil, ErrTriggerInvalid
	}
	method := normaliseMethod(in.Method)
	now := time.Now().UnixMilli()
	cmd := &Command{
		ID:               uuid.NewString(),
		TeamID:           in.TeamID,
		CreatorID:        in.CreatorID,
		Trigger:          trigger,
		Method:           method,
		URL:              in.URL,
		Username:         in.Username,
		IconURL:          in.IconURL,
		AutoComplete:     in.AutoComplete,
		AutoCompleteDesc: in.AutoCompleteDesc,
		AutoCompleteHint: in.AutoCompleteHint,
		DisplayName:      in.DisplayName,
		Description:      in.Description,
		Token:            randomToken(),
		CreateAt:         now,
		UpdateAt:         now,
	}
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO commands (
			id, team_id, creator_id, trigger_word, method, url, username, icon_url,
			auto_complete, auto_complete_desc, auto_complete_hint,
			display_name, description, token, create_at, update_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15)
	`, cmd.ID, cmd.TeamID, cmd.CreatorID, cmd.Trigger, cmd.Method, cmd.URL, cmd.Username, cmd.IconURL,
		cmd.AutoComplete, cmd.AutoCompleteDesc, cmd.AutoCompleteHint,
		cmd.DisplayName, cmd.Description, cmd.Token, cmd.CreateAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicateTrigger
		}
		return nil, err
	}
	return cmd, nil
}

// Get returns a single non-deleted command. Returns ErrNotFound on miss.
func (s *Service) Get(ctx context.Context, id string) (*Command, error) {
	row := s.db.Pool.QueryRow(ctx, selectCols+`
		FROM commands WHERE id=$1 AND delete_at=0
	`, id)
	c, err := scanCommand(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return c, nil
}

// ListForTeam returns every non-deleted command for the given team, ordered
// by trigger so the admin UI is stable across reloads. customOnly is the
// official-API knob that lets the official client filter out built-ins; we
// don't have that distinction here so the flag is accepted but ignored.
func (s *Service) ListForTeam(ctx context.Context, teamID string) ([]*Command, error) {
	rows, err := s.db.Pool.Query(ctx, selectCols+`
		FROM commands WHERE team_id=$1 AND delete_at=0
		ORDER BY trigger_word ASC
	`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Command{}
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// FindByTrigger resolves a trigger token (case-insensitive, no leading slash)
// to a command for the given team. Used by the slash-command dispatcher to
// route /<trigger> at execution time. Returns ErrNotFound when nothing matches.
func (s *Service) FindByTrigger(ctx context.Context, teamID, trigger string) (*Command, error) {
	t := strings.TrimPrefix(strings.TrimSpace(trigger), "/")
	t = strings.ToLower(t)
	row := s.db.Pool.QueryRow(ctx, selectCols+`
		FROM commands
		WHERE team_id=$1 AND LOWER(trigger_word)=$2 AND delete_at=0
	`, teamID, t)
	c, err := scanCommand(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return c, nil
}

// UpdateInput accepts the full new state (the official PUT contract is full
// replace, not patch). Trigger collisions surface as ErrDuplicateTrigger.
type UpdateInput struct {
	Trigger          string
	Method           string
	URL              string
	Username         string
	IconURL          string
	AutoComplete     bool
	AutoCompleteDesc string
	AutoCompleteHint string
	DisplayName      string
	Description      string
}

// Update writes a full new state for an existing command. Returns the
// post-update row so the caller can echo it. Caller verifies caller-vs-creator
// and admin-override before calling — we only enforce data invariants here.
func (s *Service) Update(ctx context.Context, id string, in UpdateInput) (*Command, error) {
	trigger := strings.TrimPrefix(strings.TrimSpace(in.Trigger), "/")
	trigger = strings.ToLower(trigger)
	if !validTrigger(trigger) {
		return nil, ErrTriggerInvalid
	}
	method := normaliseMethod(in.Method)
	now := time.Now().UnixMilli()
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE commands
		SET trigger_word=$2, method=$3, url=$4, username=$5, icon_url=$6,
		    auto_complete=$7, auto_complete_desc=$8, auto_complete_hint=$9,
		    display_name=$10, description=$11, update_at=$12
		WHERE id=$1 AND delete_at=0
	`, id, trigger, method, in.URL, in.Username, in.IconURL,
		in.AutoComplete, in.AutoCompleteDesc, in.AutoCompleteHint,
		in.DisplayName, in.Description, now)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicateTrigger
		}
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.Get(ctx, id)
}

// Delete soft-deletes by stamping delete_at. We don't physically remove the
// row so audit logs that reference command_id stay resolvable.
func (s *Service) Delete(ctx context.Context, id string) error {
	now := time.Now().UnixMilli()
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE commands SET delete_at=$2 WHERE id=$1 AND delete_at=0
	`, id, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RegenerateToken mints a new token for an existing command. Returns the new
// token and an updated Command so the UI can show the value once. Each
// regeneration invalidates any out-of-band copies the integrator stored.
func (s *Service) RegenerateToken(ctx context.Context, id string) (*Command, error) {
	now := time.Now().UnixMilli()
	tok := randomToken()
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE commands SET token=$2, update_at=$3 WHERE id=$1 AND delete_at=0
	`, id, tok, now)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.Get(ctx, id)
}

// Move re-homes a command to another team. Trigger collisions in the target
// team return ErrDuplicateTrigger. Mattermost's official endpoint is
// PUT /commands/{id}/move with body {team_id}.
func (s *Service) Move(ctx context.Context, id, targetTeamID string) error {
	now := time.Now().UnixMilli()
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE commands SET team_id=$2, update_at=$3 WHERE id=$1 AND delete_at=0
	`, id, targetTeamID, now)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicateTrigger
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AutocompleteForTeam returns the auto_complete=true commands for the team.
// Used to populate the slash-command palette popup.
func (s *Service) AutocompleteForTeam(ctx context.Context, teamID string) ([]*Command, error) {
	rows, err := s.db.Pool.Query(ctx, selectCols+`
		FROM commands
		WHERE team_id=$1 AND delete_at=0 AND auto_complete=TRUE
		ORDER BY trigger_word ASC
	`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Command{}
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- helpers ----

const selectCols = `SELECT id, team_id, creator_id, trigger_word, method, url, username, icon_url,
       auto_complete, auto_complete_desc, auto_complete_hint,
       display_name, description, token, create_at, update_at, delete_at`

type scannable interface{ Scan(...any) error }

func scanCommand(row scannable) (*Command, error) {
	var c Command
	err := row.Scan(&c.ID, &c.TeamID, &c.CreatorID, &c.Trigger, &c.Method, &c.URL, &c.Username, &c.IconURL,
		&c.AutoComplete, &c.AutoCompleteDesc, &c.AutoCompleteHint,
		&c.DisplayName, &c.Description, &c.Token, &c.CreateAt, &c.UpdateAt, &c.DeleteAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// validTrigger enforces a conservative slug shape so triggers can be safely
// embedded in URL paths and chat messages without escaping. 1-32 chars, ascii
// letters / digits / -_, no leading hyphen / underscore.
func validTrigger(t string) bool {
	if len(t) == 0 || len(t) > 32 {
		return false
	}
	first := t[0]
	if !(first >= 'a' && first <= 'z') && !(first >= '0' && first <= '9') {
		return false
	}
	for i := 0; i < len(t); i++ {
		ch := t[i]
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= '0' && ch <= '9':
		case ch == '-' || ch == '_':
		default:
			return false
		}
	}
	return true
}

func normaliseMethod(m string) string {
	switch strings.ToUpper(strings.TrimSpace(m)) {
	case "G", "GET":
		return string(MethodGet)
	default:
		return string(MethodPost)
	}
}

func randomToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to a uuid-derived token; shouldn't happen but better
		// than panicking the request handler.
		return strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	return hex.EncodeToString(b[:])
}

// isUniqueViolation peeks at a pgx error to detect the unique-trigger collision.
// We avoid a hard import on pgconn so the package stays light; matching the
// SQLSTATE prefix on the error string is enough — only the trigger index
// can fire here.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") ||
		strings.Contains(msg, "duplicate key value")
}
