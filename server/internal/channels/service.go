package channels

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
)

type Channel struct {
	ID          string `json:"id"`
	TeamID      string `json:"team_id"`
	Type        string `json:"type"` // O, P, D, G
	DisplayName string `json:"display_name"`
	Name        string `json:"name"`
	Header      string `json:"header"`
	Purpose     string `json:"purpose"`
	CreateAt    int64  `json:"create_at"`
	UpdateAt    int64  `json:"update_at"`
	DeleteAt    int64  `json:"delete_at"`
}

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

func (s *Service) Create(ctx context.Context, teamID, name, display, channelType, creatorID string) (*Channel, error) {
	now := time.Now().UnixMilli()
	c := &Channel{
		ID:          uuid.NewString(),
		TeamID:      teamID,
		Type:        channelType,
		DisplayName: display,
		Name:        name,
		CreateAt:    now,
		UpdateAt:    now,
	}
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES ($1,$2,$3,$4,$5,$6,$6)
	`, c.ID, c.TeamID, c.Type, c.DisplayName, c.Name, now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO channel_members (channel_id, user_id, roles, create_at)
		VALUES ($1,$2,'channel_admin channel_user',$3)
	`, c.ID, creatorID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Service) ListForUser(ctx context.Context, userID, teamID string) ([]Channel, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT c.id, COALESCE(c.team_id,''), c.type, c.display_name, c.name, c.header, c.purpose, c.create_at, c.update_at, c.delete_at
		FROM channels c
		JOIN channel_members m ON m.channel_id = c.id
		WHERE m.user_id = $1 AND c.team_id = $2 AND c.delete_at = 0
		ORDER BY c.create_at ASC`, userID, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.TeamID, &c.Type, &c.DisplayName, &c.Name, &c.Header, &c.Purpose, &c.CreateAt, &c.UpdateAt, &c.DeleteAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListAllForUserIncludingDeleted returns every channel a user belongs to
// across all teams. It backs GET /api/v4/users/{user_id}/channels.
func (s *Service) ListAllForUserIncludingDeleted(ctx context.Context, userID string, includeDeleted bool) ([]Channel, error) {
	q := `
		SELECT c.id, COALESCE(c.team_id,''), c.type, c.display_name, c.name, c.header, c.purpose, c.create_at, c.update_at, c.delete_at
		FROM channels c
		JOIN channel_members m ON m.channel_id = c.id
		WHERE m.user_id = $1`
	if !includeDeleted {
		q += ` AND c.delete_at = 0`
	}
	q += ` ORDER BY COALESCE(c.team_id,''), c.create_at ASC`
	rows, err := s.db.Pool.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.TeamID, &c.Type, &c.DisplayName, &c.Name, &c.Header, &c.Purpose, &c.CreateAt, &c.UpdateAt, &c.DeleteAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// EnsureDefault creates the "general" channel in the given team if missing.
func (s *Service) EnsureDefault(ctx context.Context, teamID string) (*Channel, error) {
	var c Channel
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, team_id, type, display_name, name, header, purpose, create_at, update_at, delete_at
		FROM channels WHERE team_id=$1 AND name='general' AND delete_at=0
	`, teamID).Scan(&c.ID, &c.TeamID, &c.Type, &c.DisplayName, &c.Name, &c.Header, &c.Purpose, &c.CreateAt, &c.UpdateAt, &c.DeleteAt)
	if err == nil {
		return &c, nil
	}
	now := time.Now().UnixMilli()
	c = Channel{
		ID:          uuid.NewString(),
		TeamID:      teamID,
		Type:        "O",
		DisplayName: "General",
		Name:        "general",
		CreateAt:    now,
		UpdateAt:    now,
	}
	if _, err := s.db.Pool.Exec(ctx, `
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES ($1,$2,$3,$4,$5,$6,$6)
	`, c.ID, c.TeamID, c.Type, c.DisplayName, c.Name, now); err != nil {
		return nil, err
	}
	return &c, nil
}

// Join adds a user as a regular channel member, ignoring duplicates.
func (s *Service) Join(ctx context.Context, channelID, userID string) error {
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO channel_members (channel_id, user_id, roles, create_at)
		VALUES ($1,$2,'channel_user',$3)
		ON CONFLICT (channel_id, user_id) DO NOTHING
	`, channelID, userID, time.Now().UnixMilli())
	return err
}

type Member struct {
	ChannelID    string `json:"channel_id"`
	UserID       string `json:"user_id"`
	Roles        string `json:"roles"`
	LastViewedAt int64  `json:"last_viewed_at"`
	CreateAt     int64  `json:"create_at"`
}

// ListMembers returns everyone in the channel ordered by join time.
func (s *Service) ListMembers(ctx context.Context, channelID string) ([]Member, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT channel_id, user_id, roles, last_viewed_at, create_at
		FROM channel_members WHERE channel_id=$1
		ORDER BY create_at ASC
	`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.ChannelID, &m.UserID, &m.Roles, &m.LastViewedAt, &m.CreateAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListMembershipsForUser returns every channel_members row for the given user.
// Mirrors `GET /api/v4/users/{user_id}/channel_members` so official clients can
// hydrate channel-scoped state in one round-trip on launch instead of fanning
// out per-channel calls. Includes ALL teams' channels because the official
// client groups by team locally.
func (s *Service) ListMembershipsForUser(ctx context.Context, userID string) ([]Member, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT cm.channel_id, cm.user_id, cm.roles, cm.last_viewed_at, cm.create_at
		FROM channel_members cm
		JOIN channels c ON c.id = cm.channel_id
		WHERE cm.user_id = $1 AND c.delete_at = 0
		ORDER BY cm.create_at ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.ChannelID, &m.UserID, &m.Roles, &m.LastViewedAt, &m.CreateAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MembersByIDs hydrates a batch of channel memberships by channel id for a
// single user. Used by Mattermost's `POST /channels/members/ids` family for
// efficient bulk reads. Caps input slice client-side at 200; SQL caps don't
// matter because ANY($1) is unbounded.
func (s *Service) MembersByIDs(ctx context.Context, userID string, channelIDs []string) ([]Member, error) {
	out := []Member{}
	if len(channelIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT channel_id, user_id, roles, last_viewed_at, create_at
		FROM channel_members
		WHERE user_id = $1 AND channel_id = ANY($2)
	`, userID, channelIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.ChannelID, &m.UserID, &m.Roles, &m.LastViewedAt, &m.CreateAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ChannelsByIDsInTeam hydrates a bounded set of channels from a team-scoped
// id list, returning only channels the caller can see (member of the
// channel OR public ('O') channel in a team they belong to). Mirrors the
// official Mattermost endpoint shape — clients use it on launch to
// hydrate sidebars without fanning per-channel. Cap input at 200 ids.
func (s *Service) ChannelsByIDsInTeam(ctx context.Context, teamID, userID string, channelIDs []string) ([]Channel, error) {
	out := []Channel{}
	if len(channelIDs) == 0 {
		return out, nil
	}
	if len(channelIDs) > 200 {
		channelIDs = channelIDs[:200]
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT c.id, c.team_id, c.name, c.display_name, c.type, c.header, c.purpose,
		       c.create_at, c.update_at, c.delete_at
		  FROM channels c
		 WHERE c.team_id = $1
		   AND c.id = ANY($3)
		   AND c.delete_at = 0
		   AND (c.type = 'O'
		        OR EXISTS (SELECT 1 FROM channel_members m
		                   WHERE m.channel_id = c.id AND m.user_id = $2))
		 ORDER BY c.display_name
	`, teamID, userID, channelIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.TeamID, &c.Name, &c.DisplayName, &c.Type,
			&c.Header, &c.Purpose, &c.CreateAt, &c.UpdateAt, &c.DeleteAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListGroupChannelsForUser returns every group ('G') channel the user is
// a member of. Mattermost's official API exposes this as a body-less POST
// (legacy quirk) that returns the user's group DM list; we expose the
// same shape so official desktop clients can hydrate group DM rosters
// without walking every channel_members row themselves.
func (s *Service) ListGroupChannelsForUser(ctx context.Context, userID string) ([]Channel, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT c.id, c.team_id, c.name, c.display_name, c.type, c.header, c.purpose,
		       c.create_at, c.update_at, c.delete_at
		  FROM channels c
		  JOIN channel_members m ON m.channel_id = c.id
		 WHERE m.user_id = $1 AND c.type = 'G' AND c.delete_at = 0
		 ORDER BY c.update_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.TeamID, &c.Name, &c.DisplayName, &c.Type,
			&c.Header, &c.Purpose, &c.CreateAt, &c.UpdateAt, &c.DeleteAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Remove deletes a channel membership row.
func (s *Service) Remove(ctx context.Context, channelID, userID string) error {
	_, err := s.db.Pool.Exec(ctx, `
		DELETE FROM channel_members WHERE channel_id=$1 AND user_id=$2
	`, channelID, userID)
	return err
}

// GetMember returns one channel_members row, or nil with no error when
// the user isn't a member of the channel. Used by handlers that need to
// inspect role tokens before deciding whether to authorise an action.
func (s *Service) GetMember(ctx context.Context, channelID, userID string) (*Member, error) {
	var m Member
	err := s.db.Pool.QueryRow(ctx, `
		SELECT channel_id, user_id, roles, last_viewed_at, create_at
		FROM channel_members WHERE channel_id=$1 AND user_id=$2
	`, channelID, userID).Scan(&m.ChannelID, &m.UserID, &m.Roles, &m.LastViewedAt, &m.CreateAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &m, nil
}

// SetMemberRoles overwrites the space-delimited roles string on the
// channel_members row. Mirrors `PUT /channels/{id}/members/{uid}/roles`
// (and the schemeRoles alias). Whitespace is normalised; duplicate tokens
// collapsed; empty input rejected so we never strip a member out of the
// channel_user baseline by accident. Returns false when no row exists so
// the handler can 404 cleanly.
func (s *Service) SetMemberRoles(ctx context.Context, channelID, userID, roles string) (bool, error) {
	canon := canonicalRoles(roles)
	if canon == "" {
		return false, nil
	}
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE channel_members SET roles=$1
		WHERE channel_id=$2 AND user_id=$3
	`, canon, channelID, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// canonicalRoles splits on whitespace and dedups while preserving order.
// Empty input yields an empty string so callers can short-circuit.
func canonicalRoles(in string) string {
	parts := strings.Fields(in)
	if len(parts) == 0 {
		return ""
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return strings.Join(out, " ")
}

// Get fetches a single channel by id (ignores delete_at so clients can
// still render tombstones of removed channels).
func (s *Service) Get(ctx context.Context, channelID string) (*Channel, error) {
	var c Channel
	var teamID *string
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, team_id, type, display_name, name, header, purpose, create_at, update_at, delete_at
		FROM channels WHERE id=$1
	`, channelID).Scan(&c.ID, &teamID, &c.Type, &c.DisplayName, &c.Name, &c.Header, &c.Purpose, &c.CreateAt, &c.UpdateAt, &c.DeleteAt)
	if err != nil {
		return nil, err
	}
	if teamID != nil {
		c.TeamID = *teamID
	}
	return &c, nil
}

// Patch applies partial updates. Empty strings mean "leave alone" to match
// the common REST PATCH semantic.
func (s *Service) Patch(ctx context.Context, channelID, displayName, header, purpose string) (*Channel, error) {
	_, err := s.db.Pool.Exec(ctx, `
		UPDATE channels SET
			display_name = COALESCE(NULLIF($2,''), display_name),
			header       = CASE WHEN $3::text = '__unchanged__' THEN header  ELSE $3 END,
			purpose      = CASE WHEN $4::text = '__unchanged__' THEN purpose ELSE $4 END,
			update_at    = $5
		WHERE id=$1 AND delete_at=0
	`, channelID, displayName, header, purpose, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, channelID)
}

// PatchExtended is the Phase 23 superset of Patch that also lets the caller
// rename the URL slug. nil pointers leave the field alone; non-nil pointers
// (including empty strings for header/purpose) overwrite. `name` is gated:
// empty string => skip; non-empty => overwrite. We don't validate slug
// charset here — the handler does the [a-z0-9_-] check before calling.
func (s *Service) PatchExtended(ctx context.Context, channelID string, name *string, displayName *string, header *string, purpose *string) (*Channel, error) {
	now := time.Now().UnixMilli()
	nameVal, nameSet := derefStringPtr(name)
	dnVal, dnSet := derefStringPtr(displayName)
	headerVal, headerSet := derefStringPtr(header)
	purposeVal, purposeSet := derefStringPtr(purpose)
	_, err := s.db.Pool.Exec(ctx, `
		UPDATE channels SET
			name         = CASE WHEN $2  THEN $3  ELSE name END,
			display_name = CASE WHEN $4  THEN $5  ELSE display_name END,
			header       = CASE WHEN $6  THEN $7  ELSE header END,
			purpose      = CASE WHEN $8  THEN $9  ELSE purpose END,
			update_at    = $10
		WHERE id=$1 AND delete_at=0
	`, channelID,
		nameSet, nameVal,
		dnSet, dnVal,
		headerSet, headerVal,
		purposeSet, purposeVal,
		now)
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, channelID)
}

func derefStringPtr(p *string) (string, bool) {
	if p == nil {
		return "", false
	}
	return *p, true
}

// SetPrivacy flips the channel between public ('O') and private ('P'). DM
// and group channels are rejected at the handler layer because changing
// type for D/G doesn't make sense — there's no existing membership semantic
// to fall back on. Returns the refreshed channel so the caller can echo it
// over WS.
func (s *Service) SetPrivacy(ctx context.Context, channelID, privacy string) (*Channel, error) {
	if privacy != "O" && privacy != "P" {
		return nil, errInvalidPrivacy
	}
	_, err := s.db.Pool.Exec(ctx, `
		UPDATE channels SET type=$2, update_at=$3
		WHERE id=$1 AND delete_at=0 AND type IN ('O','P')
	`, channelID, privacy, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, channelID)
}

// errInvalidPrivacy is exported via SetPrivacy's return value so handlers
// can surface a 400 with a well-known error id.
var errInvalidPrivacy = newConstError("api.channel.privacy.invalid")

// SearchAll matches channels across the entire instance by name + display_name.
// Mirrors `POST /channels/search`. Membership is NOT enforced — this endpoint
// is admin-scoped at the handler layer. Excludes archived. Cap 100/page.
func (s *Service) SearchAll(ctx context.Context, term string, limit int) ([]Channel, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	like := "%" + strings.TrimSpace(term) + "%"
	rows, err := s.db.Pool.Query(ctx, `
		SELECT c.id, COALESCE(c.team_id,''), c.type, c.display_name, c.name, c.header, c.purpose, c.create_at, c.update_at, c.delete_at
		FROM channels c
		WHERE c.delete_at = 0
		  AND (c.display_name ILIKE $1 OR c.name ILIKE $1)
		ORDER BY c.display_name ASC
		LIMIT $2
	`, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.TeamID, &c.Type, &c.DisplayName, &c.Name, &c.Header, &c.Purpose, &c.CreateAt, &c.UpdateAt, &c.DeleteAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MembersByChannel returns the channel_members rows for the given channel
// filtered by the supplied user_ids. Mirrors Mattermost's
// `POST /channels/{id}/members/ids` for bulk hydrate. Caller-supplied id
// list is capped at 200 client-side; SQL doesn't enforce.
func (s *Service) MembersByChannel(ctx context.Context, channelID string, userIDs []string) ([]Member, error) {
	out := []Member{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT channel_id, user_id, roles, last_viewed_at, create_at
		FROM channel_members
		WHERE channel_id = $1 AND user_id = ANY($2)
	`, channelID, userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.ChannelID, &m.UserID, &m.Roles, &m.LastViewedAt, &m.CreateAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// constErr is an unexported sentinel error type for service-level error
// constants (see errInvalidPrivacy). Lets us use errors.Is without
// allocating a new error per call.
type constErr struct{ s string }

func (e *constErr) Error() string      { return e.s }
func newConstError(s string) *constErr { return &constErr{s: s} }

// EnsureDirect returns the D-type channel between two users, creating it
// on first call. Direct channels carry no team and use a canonical name of
// the two user ids joined with "__" in sorted order — the same scheme
// Mattermost uses for stable lookup.
func (s *Service) EnsureDirect(ctx context.Context, userA, userB string) (*Channel, error) {
	ids := []string{userA, userB}
	sort.Strings(ids)
	name := ids[0] + "__" + ids[1]

	var c Channel
	var teamID *string
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, team_id, type, display_name, name, header, purpose, create_at, update_at, delete_at
		FROM channels WHERE type='D' AND name=$1 AND delete_at=0
	`, name).Scan(&c.ID, &teamID, &c.Type, &c.DisplayName, &c.Name, &c.Header, &c.Purpose, &c.CreateAt, &c.UpdateAt, &c.DeleteAt)
	if err == nil {
		if teamID != nil {
			c.TeamID = *teamID
		}
		return &c, nil
	}
	if err != pgx.ErrNoRows {
		return nil, err
	}

	now := time.Now().UnixMilli()
	c = Channel{
		ID:          uuid.NewString(),
		Type:        "D",
		Name:        name,
		DisplayName: "",
		CreateAt:    now,
		UpdateAt:    now,
	}
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES ($1, NULL, $2, $3, $4, $5, $5)
	`, c.ID, c.Type, c.DisplayName, c.Name, now); err != nil {
		return nil, err
	}
	// Both participants are channel_users. Self-DMs still work (one row).
	for _, uid := range uniqueStrings(ids) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO channel_members (channel_id, user_id, roles, create_at)
			VALUES ($1,$2,'channel_user',$3)
			ON CONFLICT (channel_id, user_id) DO NOTHING
		`, c.ID, uid, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &c, nil
}

// EnsureGroup returns the G-type channel for the given user-id set,
// creating it on first call. The canonical name is the sha-style 40-char
// concatenation of the sorted user-ids hashed (we keep it simpler — sorted
// joined with "__" and capped at 64 chars by truncating; Mattermost's own
// implementation uses a fixed-length hash but the canonical-name semantic
// is the same: "this exact set of users → this channel id, idempotent").
// Membership is 3-8 users; smaller sets should use EnsureDirect, larger
// sets should be a private channel with explicit invites.
func (s *Service) EnsureGroup(ctx context.Context, userIDs []string) (*Channel, error) {
	if len(userIDs) < 3 || len(userIDs) > 8 {
		return nil, errors.New("group channels require 3-8 members")
	}
	ids := uniqueStrings(userIDs)
	if len(ids) < 3 {
		return nil, errors.New("group channels require 3-8 distinct members")
	}
	sort.Strings(ids)
	name := strings.Join(ids, "__")
	if len(name) > 64 {
		name = name[:64]
	}

	var c Channel
	var teamID *string
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, team_id, type, display_name, name, header, purpose, create_at, update_at, delete_at
		FROM channels WHERE type='G' AND name=$1 AND delete_at=0
	`, name).Scan(&c.ID, &teamID, &c.Type, &c.DisplayName, &c.Name, &c.Header, &c.Purpose, &c.CreateAt, &c.UpdateAt, &c.DeleteAt)
	if err == nil {
		if teamID != nil {
			c.TeamID = *teamID
		}
		return &c, nil
	}
	if err != pgx.ErrNoRows {
		return nil, err
	}

	now := time.Now().UnixMilli()
	c = Channel{
		ID:          uuid.NewString(),
		Type:        "G",
		Name:        name,
		DisplayName: "",
		CreateAt:    now,
		UpdateAt:    now,
	}
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES ($1, NULL, $2, $3, $4, $5, $5)
	`, c.ID, c.Type, c.DisplayName, c.Name, now); err != nil {
		return nil, err
	}
	for _, uid := range ids {
		if _, err := tx.Exec(ctx, `
			INSERT INTO channel_members (channel_id, user_id, roles, create_at)
			VALUES ($1,$2,'channel_user',$3)
			ON CONFLICT (channel_id, user_id) DO NOTHING
		`, c.ID, uid, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &c, nil
}

// MarkViewed bumps last_viewed_at for the (channel, user) pair to now and
// zeros the unread / mention counters. Returns the updated timestamp so
// callers can broadcast it.
func (s *Service) MarkViewed(ctx context.Context, channelID, userID string) (int64, error) {
	now := time.Now().UnixMilli()
	_, err := s.db.Pool.Exec(ctx, `
		UPDATE channel_members
		SET last_viewed_at=$1, msg_count=0, mention_count=0
		WHERE channel_id=$2 AND user_id=$3
	`, now, channelID, userID)
	return now, err
}

// MarkUnreadFromPost rewinds the user's read marker so the given post is the
// first unread row. Mirrors Mattermost's `POST /users/{uid}/posts/{pid}/set_unread`.
//
// We set last_viewed_at to (post.create_at - 1) so the post itself surfaces
// as unread; counters are rebuilt by counting posts on/after that boundary
// (capped — a runaway count would be expensive). mention_count is recomputed
// by counting posts whose `props.mention_user_ids` includes the caller.
func (s *Service) MarkUnreadFromPost(ctx context.Context, channelID, userID string, postCreateAt int64) (int64, int64, int64, error) {
	if postCreateAt <= 0 {
		return 0, 0, 0, nil
	}
	boundary := postCreateAt - 1
	// Recount visible posts at/after boundary (cap at 9999 so a deep history
	// rewind doesn't tie up the connection).
	var msgCount int64
	if err := s.db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM (
		  SELECT 1 FROM posts WHERE channel_id=$1 AND delete_at=0 AND create_at > $2 LIMIT 9999
		) t
	`, channelID, boundary).Scan(&msgCount); err != nil {
		return 0, 0, 0, err
	}
	// Mention recount uses the existing props->'mention_user_ids' JSONB array
	// the post handler stamps on creation.
	var mentionCount int64
	_ = s.db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM posts
		WHERE channel_id=$1 AND delete_at=0 AND create_at > $2
		  AND props ? 'mention_user_ids'
		  AND props->'mention_user_ids' @> to_jsonb($3::text)
	`, channelID, boundary, userID).Scan(&mentionCount)
	_, err := s.db.Pool.Exec(ctx, `
		UPDATE channel_members
		SET last_viewed_at = $1, msg_count = $2, mention_count = $3
		WHERE channel_id = $4 AND user_id = $5
	`, boundary, msgCount, mentionCount, channelID, userID)
	if err != nil {
		return 0, 0, 0, err
	}
	return boundary, msgCount, mentionCount, nil
}

// MemberWithCounts is a channel_members row with unread counters and the
// caller's notify props inlined. Returned by ListForUserWithCounts so the
// webapp can restore sidebar badges on reconnect in one round-trip.
type MemberWithCounts struct {
	ChannelID    string         `json:"channel_id"`
	UserID       string         `json:"user_id"`
	Roles        string         `json:"roles"`
	LastViewedAt int64          `json:"last_viewed_at"`
	MsgCount     int64          `json:"msg_count"`
	MentionCount int64          `json:"mention_count"`
	NotifyProps  map[string]any `json:"notify_props"`
}

// ListForUserWithCounts returns every channel_members row the user owns
// in a given team, including counters + notify props. Joined on channels
// so DMs (team_id IS NULL) and team channels can both be filtered off a
// single query — DMs are included only when teamID is "" (sidebar home).
func (s *Service) ListForUserWithCounts(ctx context.Context, userID, teamID string) ([]MemberWithCounts, error) {
	q := `
		SELECT m.channel_id, m.user_id, m.roles, m.last_viewed_at,
		       m.msg_count, m.mention_count, m.notify_props
		FROM channel_members m
		JOIN channels c ON c.id = m.channel_id
		WHERE m.user_id = $1 AND c.delete_at = 0
		  AND (c.team_id = $2 OR ($2 = '' AND c.team_id IS NULL) OR c.type = 'D')
	`
	rows, err := s.db.Pool.Query(ctx, q, userID, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MemberWithCounts{}
	for rows.Next() {
		var m MemberWithCounts
		var propsRaw []byte
		if err := rows.Scan(&m.ChannelID, &m.UserID, &m.Roles, &m.LastViewedAt, &m.MsgCount, &m.MentionCount, &propsRaw); err != nil {
			return nil, err
		}
		m.NotifyProps = map[string]any{}
		if len(propsRaw) > 0 {
			_ = json.Unmarshal(propsRaw, &m.NotifyProps)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListAllForUserWithCounts returns every active channel_members row owned by
// the user across all teams in one query. Moyro Flow uses this read model to
// avoid issuing one membership request per team when rendering global badges.
func (s *Service) ListAllForUserWithCounts(ctx context.Context, userID string) ([]MemberWithCounts, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT m.channel_id, m.user_id, m.roles, m.last_viewed_at,
		       m.msg_count, m.mention_count, m.notify_props
		FROM channel_members m
		JOIN channels c ON c.id = m.channel_id
		WHERE m.user_id = $1 AND c.delete_at = 0
		ORDER BY COALESCE(c.team_id, ''), c.create_at ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MemberWithCounts{}
	for rows.Next() {
		var m MemberWithCounts
		var propsRaw []byte
		if err := rows.Scan(&m.ChannelID, &m.UserID, &m.Roles, &m.LastViewedAt, &m.MsgCount, &m.MentionCount, &propsRaw); err != nil {
			return nil, err
		}
		m.NotifyProps = map[string]any{}
		if len(propsRaw) > 0 {
			_ = json.Unmarshal(propsRaw, &m.NotifyProps)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type ChannelUnread struct {
	TeamID       string         `json:"team_id"`
	ChannelID    string         `json:"channel_id"`
	MsgCount     int64          `json:"msg_count"`
	MentionCount int64          `json:"mention_count"`
	NotifyProps  map[string]any `json:"notify_props"`
}

func (s *Service) GetUnread(ctx context.Context, userID, channelID string) (*ChannelUnread, error) {
	var out ChannelUnread
	var propsRaw []byte
	err := s.db.Pool.QueryRow(ctx, `
		SELECT COALESCE(c.team_id,''), cm.channel_id, cm.msg_count, cm.mention_count, cm.notify_props
		FROM channel_members cm
		JOIN channels c ON c.id = cm.channel_id
		WHERE cm.user_id=$1 AND cm.channel_id=$2 AND c.delete_at=0
	`, userID, channelID).Scan(&out.TeamID, &out.ChannelID, &out.MsgCount, &out.MentionCount, &propsRaw)
	if err != nil {
		return nil, err
	}
	out.NotifyProps = defaultNotifyProps()
	if len(propsRaw) > 0 {
		user := map[string]any{}
		if jerr := json.Unmarshal(propsRaw, &user); jerr == nil {
			for k, v := range user {
				out.NotifyProps[k] = v
			}
		}
	}
	return &out, nil
}

// GetNotifyProps returns the caller's notification prefs for a channel,
// merged with the server defaults so clients never see an empty map.
func (s *Service) GetNotifyProps(ctx context.Context, channelID, userID string) (map[string]any, error) {
	var raw []byte
	err := s.db.Pool.QueryRow(ctx, `
		SELECT notify_props FROM channel_members WHERE channel_id=$1 AND user_id=$2
	`, channelID, userID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	props := defaultNotifyProps()
	if len(raw) > 0 {
		user := map[string]any{}
		if jerr := json.Unmarshal(raw, &user); jerr == nil {
			for k, v := range user {
				props[k] = v
			}
		}
	}
	return props, nil
}

// SetNotifyProps overwrites the notify_props JSONB with the given map.
// Unknown keys are allowed (forward-compat with future settings).
func (s *Service) SetNotifyProps(ctx context.Context, channelID, userID string, props map[string]any) error {
	if props == nil {
		props = map[string]any{}
	}
	raw, _ := json.Marshal(props)
	_, err := s.db.Pool.Exec(ctx, `
		UPDATE channel_members SET notify_props=$1
		WHERE channel_id=$2 AND user_id=$3
	`, raw, channelID, userID)
	return err
}

func defaultNotifyProps() map[string]any {
	return map[string]any{
		"desktop":     "all", // all | mentions | none
		"mark_unread": "all", // all | mention (mention-only counts as muted)
	}
}

// Counter is the per-member result of BumpUnread. Desktop surfaces the
// current notification preference so the handler can skip the WS fanout
// for muted / DND users without a second query.
type Counter struct {
	UserID       string `json:"user_id"`
	MsgCount     int64  `json:"msg_count"`
	MentionCount int64  `json:"mention_count"`
	Desktop      string `json:"desktop"`
}

// BumpUnread increments msg_count for every member of the channel except
// the author, and mention_count for each mentioned user. Muted members
// (mark_unread=mention) get msg_count bumped only when they're also
// mentioned. Runs in a single SQL statement for atomicity + speed.
func (s *Service) BumpUnread(ctx context.Context, channelID, authorID string, mentionedIDs []string) ([]Counter, error) {
	if mentionedIDs == nil {
		mentionedIDs = []string{}
	}
	rows, err := s.db.Pool.Query(ctx, `
		UPDATE channel_members
		SET mention_count = mention_count + CASE WHEN user_id = ANY($2) THEN 1 ELSE 0 END,
		    msg_count     = msg_count + CASE
		      WHEN (notify_props->>'mark_unread' = 'mention' AND NOT (user_id = ANY($2))) THEN 0
		      ELSE 1 END
		WHERE channel_id = $1 AND user_id <> $3
		RETURNING user_id, msg_count, mention_count,
		          COALESCE(notify_props->>'desktop', 'all') AS desktop
	`, channelID, mentionedIDs, authorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Counter{}
	for rows.Next() {
		var c Counter
		if err := rows.Scan(&c.UserID, &c.MsgCount, &c.MentionCount, &c.Desktop); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func uniqueStrings(in []string) []string {
	seen := map[string]struct{}{}
	out := in[:0]
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// MembersAutocomplete returns up to `limit` members of the channel whose
// username starts with (or contains) the given prefix. Results are ordered
// so prefix matches float to the top — a username starting with "al" beats
// one that merely contains "al" in the middle. The `prefix` is matched
// against usernames only; email is skipped deliberately to keep the
// mention-picker visually tight and to avoid leaking email addresses to
// users who happen to share a channel but aren't admins.
//
// The caller is expected to have already verified the requesting user is
// a member of the channel; this method returns rows unconditionally.
func (s *Service) MembersAutocomplete(ctx context.Context, channelID, prefix string, limit int) ([]channelMember, error) {
	if limit <= 0 || limit > 50 {
		limit = 8
	}
	// LOWER() on both sides lets the SQL planner use btree(username) if it
	// exists, while staying case-insensitive. `prefix` is already the raw
	// token the user typed (no surrounding %), so we build both patterns
	// here for ORDER BY differentiation.
	prefixPat := prefix + "%"
	anyPat := "%" + prefix + "%"
	rows, err := s.db.Pool.Query(ctx, `
		SELECT u.id, u.username, u.email, u.roles, COALESCE(u.picture,'')
		FROM channel_members m
		JOIN users u ON u.id = m.user_id
		WHERE m.channel_id = $1
		  AND u.delete_at = 0
		  AND u.username ILIKE $3
		ORDER BY
		  CASE WHEN u.username ILIKE $2 THEN 0 ELSE 1 END,
		  u.username ASC
		LIMIT $4
	`, channelID, prefixPat, anyPat, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []channelMember{}
	for rows.Next() {
		var m channelMember
		if err := rows.Scan(&m.ID, &m.Username, &m.Email, &m.Roles, &m.Picture); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// channelMember mirrors auth.User without importing that package (avoids a
// potential import cycle). The handler re-shapes into auth.User on the way
// out so the wire format stays consistent across endpoints.
type channelMember struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Roles    string `json:"roles"`
	Picture  string `json:"picture"`
}

// Archive soft-deletes the channel by stamping delete_at. The row stays so
// existing posts + audit log entries keep resolving, but ListForUser hides
// it by default. Returns whether anything changed so the caller can skip
// audit + broadcast on a no-op.
func (s *Service) Archive(ctx context.Context, channelID string) (bool, error) {
	now := time.Now().UnixMilli()
	cmd, err := s.db.Pool.Exec(ctx, `
		UPDATE channels SET delete_at=$2, update_at=$2 WHERE id=$1 AND delete_at=0
	`, channelID, now)
	if err != nil {
		return false, err
	}
	return cmd.RowsAffected() > 0, nil
}

// Restore clears delete_at so the channel shows up in listings again.
func (s *Service) Restore(ctx context.Context, channelID string) (bool, error) {
	now := time.Now().UnixMilli()
	cmd, err := s.db.Pool.Exec(ctx, `
		UPDATE channels SET delete_at=0, update_at=$2 WHERE id=$1 AND delete_at<>0
	`, channelID, now)
	if err != nil {
		return false, err
	}
	return cmd.RowsAffected() > 0, nil
}

// ListForUserIncludingDeleted is ListForUser with an optional include-
// archived toggle. When includeDeleted is true the delete_at filter is
// dropped so the sidebar can show archived channels dimmed.
func (s *Service) ListForUserIncludingDeleted(ctx context.Context, userID, teamID string, includeDeleted bool) ([]Channel, error) {
	q := `
		SELECT c.id, COALESCE(c.team_id,''), c.type, c.display_name, c.name, c.header, c.purpose, c.create_at, c.update_at, c.delete_at
		FROM channels c
		JOIN channel_members m ON m.channel_id = c.id
		WHERE m.user_id = $1 AND c.team_id = $2`
	if !includeDeleted {
		q += ` AND c.delete_at = 0`
	}
	q += ` ORDER BY c.create_at ASC`
	rows, err := s.db.Pool.Query(ctx, q, userID, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.TeamID, &c.Type, &c.DisplayName, &c.Name, &c.Header, &c.Purpose, &c.CreateAt, &c.UpdateAt, &c.DeleteAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) IsMember(ctx context.Context, channelID, userID string) (bool, error) {
	var exists bool
	err := s.db.Pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM channel_members WHERE channel_id=$1 AND user_id=$2)
	`, channelID, userID).Scan(&exists)
	return exists, err
}

// GetByName resolves a channel by its team-scoped name. Mirrors
// `GET /api/v4/teams/{team_id}/channels/name/{channel_name}` for clients
// that route by URL slug rather than UUID.
func (s *Service) GetByName(ctx context.Context, teamID, name string) (*Channel, error) {
	var c Channel
	var tid *string
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, team_id, type, display_name, name, header, purpose, create_at, update_at, delete_at
		FROM channels WHERE team_id=$1 AND name=$2
	`, teamID, name).Scan(&c.ID, &tid, &c.Type, &c.DisplayName, &c.Name, &c.Header, &c.Purpose, &c.CreateAt, &c.UpdateAt, &c.DeleteAt)
	if err != nil {
		return nil, err
	}
	if tid != nil {
		c.TeamID = *tid
	}
	return &c, nil
}

// Stats returns the Mattermost-shaped channel stats payload. Member counts
// are split into total + guest + pinned-post counts so dashboards can show
// each independently. We don't track guest accounts yet, so guest_count is
// always zero — the field is kept so the response shape exactly matches
// the official contract.
type Stats struct {
	ChannelID       string `json:"channel_id"`
	MemberCount     int64  `json:"member_count"`
	GuestCount      int64  `json:"guest_count"`
	PinnedPostCount int64  `json:"pinnedpost_count"`
	FilesCount      int64  `json:"files_count"`
}

func (s *Service) Stats(ctx context.Context, channelID string) (*Stats, error) {
	out := &Stats{ChannelID: channelID}
	if err := s.db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM channel_members WHERE channel_id=$1
	`, channelID).Scan(&out.MemberCount); err != nil {
		return nil, err
	}
	if err := s.db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM posts WHERE channel_id=$1 AND delete_at=0 AND is_pinned=TRUE
	`, channelID).Scan(&out.PinnedPostCount); err != nil {
		return nil, err
	}
	if err := s.db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM file_infos WHERE post_id IN (SELECT id FROM posts WHERE channel_id=$1 AND delete_at=0)
	`, channelID).Scan(&out.FilesCount); err != nil {
		return nil, err
	}
	return out, nil
}

// SearchInTeam does a name/display_name ILIKE match within a team. Includes
// channels the caller has joined plus public-and-not-yet-joined ones, so
// the Cmd+K Quick Switcher can surface anything reachable. Excludes archived.
func (s *Service) SearchInTeam(ctx context.Context, teamID, userID, term string, limit int) ([]Channel, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	like := "%" + strings.TrimSpace(term) + "%"
	rows, err := s.db.Pool.Query(ctx, `
		SELECT c.id, COALESCE(c.team_id,''), c.type, c.display_name, c.name, c.header, c.purpose, c.create_at, c.update_at, c.delete_at
		FROM channels c
		WHERE c.team_id = $1
		  AND c.delete_at = 0
		  AND (c.display_name ILIKE $2 OR c.name ILIKE $2)
		  AND (
		    c.type = 'O'
		    OR EXISTS (SELECT 1 FROM channel_members m WHERE m.channel_id=c.id AND m.user_id=$3)
		  )
		ORDER BY (c.display_name ILIKE $4) DESC, c.display_name ASC
		LIMIT $5
	`, teamID, like, userID, strings.TrimSpace(term)+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.TeamID, &c.Type, &c.DisplayName, &c.Name, &c.Header, &c.Purpose, &c.CreateAt, &c.UpdateAt, &c.DeleteAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AutocompleteInTeam is the GET-style channel search used by official
// Mattermost clients for inline @-channel mentions and the channel switcher.
// Restricted to channels the caller can see (member or public).
func (s *Service) AutocompleteInTeam(ctx context.Context, teamID, userID, term string, limit int) ([]Channel, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	prefix := strings.TrimSpace(term) + "%"
	contains := "%" + strings.TrimSpace(term) + "%"
	rows, err := s.db.Pool.Query(ctx, `
		SELECT c.id, COALESCE(c.team_id,''), c.type, c.display_name, c.name, c.header, c.purpose, c.create_at, c.update_at, c.delete_at
		FROM channels c
		WHERE c.team_id = $1
		  AND c.delete_at = 0
		  AND c.type IN ('O','P')
		  AND (c.name ILIKE $2 OR c.display_name ILIKE $3)
		  AND (
		    c.type = 'O'
		    OR EXISTS (SELECT 1 FROM channel_members m WHERE m.channel_id=c.id AND m.user_id=$4)
		  )
		ORDER BY (c.name ILIKE $2) DESC, c.display_name ASC
		LIMIT $5
	`, teamID, prefix, contains, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.TeamID, &c.Type, &c.DisplayName, &c.Name, &c.Header, &c.Purpose, &c.CreateAt, &c.UpdateAt, &c.DeleteAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListPublicDiscoverable returns public (O-type) channels in the given team
// that the user has NOT joined, optionally filtered by a name/display-name
// prefix. Used by the "채널 탐색" modal so users can find channels they were
// never explicitly invited to. Excludes archived channels (delete_at != 0).
// Private (P), DM (D), and group (G) channels are intentionally skipped —
// the whole point of non-O types is that they aren't discoverable.
func (s *Service) ListPublicDiscoverable(ctx context.Context, teamID, userID, query string, limit, offset int) ([]Channel, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	if offset < 0 {
		offset = 0
	}
	args := []any{teamID, userID, limit, offset}
	sql := `
		SELECT c.id, COALESCE(c.team_id,''), c.type, c.display_name, c.name, c.header, c.purpose, c.create_at, c.update_at, c.delete_at
		FROM channels c
		WHERE c.team_id = $1
		  AND c.type = 'O'
		  AND c.delete_at = 0
		  AND NOT EXISTS (
		    SELECT 1 FROM channel_members m
		    WHERE m.channel_id = c.id AND m.user_id = $2
		  )`
	if q := strings.TrimSpace(query); q != "" {
		args = append(args, "%"+q+"%")
		sql += ` AND (c.name ILIKE $5 OR c.display_name ILIKE $5)`
	}
	sql += ` ORDER BY c.display_name ASC LIMIT $3 OFFSET $4`

	rows, err := s.db.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.TeamID, &c.Type, &c.DisplayName, &c.Name, &c.Header, &c.Purpose, &c.CreateAt, &c.UpdateAt, &c.DeleteAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
