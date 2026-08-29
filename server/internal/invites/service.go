// Package invites issues + validates team invitation tokens. A token is
// a uuid stored in invite_tokens; the shareable URL embeds the id as a
// fragment parameter (#invite=<id>) that the webapp parses on signup.
//
// Design notes:
//   - `max_uses = 0` means "unlimited until expiry" — handy for team-wide
//     links posted in a chat. Any positive value is decremented on each
//     successful consumption.
//   - Consume runs a single UPDATE with all validity checks baked into
//     the WHERE clause and returns the team_id on success. This makes
//     consumption atomic under concurrent signups without explicit
//     row locking.
//   - Validate is a read-only variant used by the public GET endpoint so
//     the signup form can preview which team the user is about to join.
package invites

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidInvite = errors.New("invite token invalid, revoked, expired, or exhausted")
)

type Invite struct {
	ID        string `json:"id"`
	TeamID    string `json:"team_id"`
	CreatedBy string `json:"created_by"`
	MaxUses   int    `json:"max_uses"`
	UseCount  int    `json:"use_count"`
	ExpiresAt int64  `json:"expires_at"`
	RevokedAt int64  `json:"revoked_at"`
	CreateAt  int64  `json:"create_at"`
}

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

// Create issues a new invite token. `maxUses <= 0` is stored as 0
// ("unlimited"); `ttl` caps how long the token is valid from now. The
// caller is expected to have already verified the actor's admin role.
func (s *Service) Create(ctx context.Context, teamID, createdBy string, maxUses int, ttl time.Duration) (*Invite, error) {
	if maxUses < 0 {
		maxUses = 0
	}
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	now := time.Now()
	inv := &Invite{
		ID:        uuid.NewString(),
		TeamID:    teamID,
		CreatedBy: createdBy,
		MaxUses:   maxUses,
		UseCount:  0,
		ExpiresAt: now.Add(ttl).UnixMilli(),
		CreateAt:  now.UnixMilli(),
	}
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO invite_tokens (id, team_id, created_by, max_uses, use_count, expires_at, revoked_at, create_at)
		VALUES ($1,$2,$3,$4,0,$5,0,$6)
	`, inv.ID, inv.TeamID, inv.CreatedBy, inv.MaxUses, inv.ExpiresAt, inv.CreateAt)
	if err != nil {
		return nil, err
	}
	return inv, nil
}

// Validate reads the invite row and reports whether it's usable right
// now. Never mutates state — safe to call from a public rate-limited
// endpoint. Returns ErrInvalidInvite if the token is missing, revoked,
// expired, or has no uses remaining.
func (s *Service) Validate(ctx context.Context, id string) (*Invite, error) {
	var inv Invite
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, team_id, created_by, max_uses, use_count, expires_at, revoked_at, create_at
		FROM invite_tokens WHERE id = $1
	`, id).Scan(&inv.ID, &inv.TeamID, &inv.CreatedBy, &inv.MaxUses, &inv.UseCount, &inv.ExpiresAt, &inv.RevokedAt, &inv.CreateAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidInvite
	}
	if err != nil {
		return nil, err
	}
	if inv.RevokedAt > 0 {
		return nil, ErrInvalidInvite
	}
	if inv.ExpiresAt <= time.Now().UnixMilli() {
		return nil, ErrInvalidInvite
	}
	if inv.MaxUses > 0 && inv.UseCount >= inv.MaxUses {
		return nil, ErrInvalidInvite
	}
	return &inv, nil
}

// Consume atomically increments use_count if the token is still valid
// and returns its team_id. Two concurrent signups racing on the last
// remaining use will see exactly one succeed — the other hits
// ErrInvalidInvite.
func (s *Service) Consume(ctx context.Context, id string) (teamID string, err error) {
	now := time.Now().UnixMilli()
	row := s.db.Pool.QueryRow(ctx, `
		UPDATE invite_tokens
		SET use_count = use_count + 1
		WHERE id = $1
		  AND revoked_at = 0
		  AND expires_at > $2
		  AND (max_uses = 0 OR use_count < max_uses)
		RETURNING team_id
	`, id, now)
	if err := row.Scan(&teamID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrInvalidInvite
		}
		return "", err
	}
	return teamID, nil
}

// ListForTeam returns every non-revoked invite for the given team, newest
// first. Expired invites are still included so the admin UI can show them
// greyed out until explicitly deleted.
func (s *Service) ListForTeam(ctx context.Context, teamID string) ([]Invite, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, team_id, created_by, max_uses, use_count, expires_at, revoked_at, create_at
		FROM invite_tokens
		WHERE team_id = $1 AND revoked_at = 0
		ORDER BY create_at DESC
	`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invite{}
	for rows.Next() {
		var inv Invite
		if err := rows.Scan(&inv.ID, &inv.TeamID, &inv.CreatedBy, &inv.MaxUses, &inv.UseCount, &inv.ExpiresAt, &inv.RevokedAt, &inv.CreateAt); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// Revoke stamps the invite as revoked. Idempotent — a second revoke on
// the same token is a no-op at the data layer.
func (s *Service) Revoke(ctx context.Context, id, teamID string) error {
	_, err := s.db.Pool.Exec(ctx, `
		UPDATE invite_tokens SET revoked_at = $3
		WHERE id = $1 AND team_id = $2 AND revoked_at = 0
	`, id, teamID, time.Now().UnixMilli())
	return err
}
