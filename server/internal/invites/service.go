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
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidInvite = errors.New("invite token invalid, revoked, expired, or exhausted")
	ErrInvalidScope  = errors.New("invite guest scope is invalid")
)

type Kind string

const (
	KindMember Kind = "member"
	KindGuest  Kind = "guest"
)

type Invite struct {
	ID                       string   `json:"id"`
	TeamID                   string   `json:"team_id"`
	CreatedBy                string   `json:"created_by"`
	MaxUses                  int      `json:"max_uses"`
	UseCount                 int      `json:"use_count"`
	ExpiresAt                int64    `json:"expires_at"`
	RevokedAt                int64    `json:"revoked_at"`
	CreateAt                 int64    `json:"create_at"`
	Kind                     Kind     `json:"kind"`
	ChannelIDs               []string `json:"channel_ids"`
	GuestExpiresAfterSeconds int64    `json:"guest_expires_after_seconds"`
	GuestFileDownload        bool     `json:"guest_file_download"`
}

type CreateOptions struct {
	MaxUses           int
	TTL               time.Duration
	Kind              Kind
	ChannelIDs        []string
	GuestAccessTTL    time.Duration
	GuestFileDownload bool
}

type Consumption struct {
	TeamID                   string
	Kind                     Kind
	ChannelIDs               []string
	GuestExpiresAfterSeconds int64
	GuestFileDownload        bool
}

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

// Create issues a new invite token. `maxUses <= 0` is stored as 0
// ("unlimited"); `ttl` caps how long the token is valid from now. The
// caller is expected to have already verified the actor's admin role.
func (s *Service) Create(ctx context.Context, teamID, createdBy string, maxUses int, ttl time.Duration) (*Invite, error) {
	return s.CreateWithOptions(ctx, teamID, createdBy, CreateOptions{
		MaxUses: maxUses, TTL: ttl, Kind: KindMember, GuestFileDownload: true,
	})
}

// CreateWithOptions creates either a regular team invitation or a guest
// invitation restricted to an explicit set of active channels in that team.
func (s *Service) CreateWithOptions(ctx context.Context, teamID, createdBy string, options CreateOptions) (*Invite, error) {
	teamID = strings.TrimSpace(teamID)
	createdBy = strings.TrimSpace(createdBy)
	if teamID == "" || createdBy == "" {
		return nil, fmt.Errorf("%w: team and creator are required", ErrInvalidScope)
	}
	if options.Kind == "" {
		options.Kind = KindMember
	}
	if options.Kind != KindMember && options.Kind != KindGuest {
		return nil, fmt.Errorf("%w: unsupported invite kind", ErrInvalidScope)
	}
	channelIDs, err := normalizeChannelIDs(options.ChannelIDs)
	if err != nil {
		return nil, err
	}
	if options.Kind == KindGuest {
		if len(channelIDs) == 0 {
			return nil, fmt.Errorf("%w: guests require at least one channel", ErrInvalidScope)
		}
		if options.GuestAccessTTL < time.Hour || options.GuestAccessTTL > 365*24*time.Hour {
			return nil, fmt.Errorf("%w: guest access must last between one hour and one year", ErrInvalidScope)
		}
		var validCount int
		if err := s.db.Pool.QueryRow(ctx, `
			SELECT count(*) FROM channels
			WHERE id=ANY($1::text[]) AND team_id=$2 AND delete_at=0 AND type IN ('O','P')
		`, channelIDs, teamID).Scan(&validCount); err != nil {
			return nil, err
		}
		if validCount != len(channelIDs) {
			return nil, fmt.Errorf("%w: every channel must be active and belong to the invited team", ErrInvalidScope)
		}
	} else {
		channelIDs = []string{}
		options.GuestAccessTTL = 0
		options.GuestFileDownload = true
	}
	if options.MaxUses < 0 {
		options.MaxUses = 0
	}
	if options.MaxUses > 10_000 {
		return nil, fmt.Errorf("%w: max uses exceeds 10000", ErrInvalidScope)
	}
	if options.TTL <= 0 {
		options.TTL = 7 * 24 * time.Hour
	}
	if options.TTL > 365*24*time.Hour {
		return nil, fmt.Errorf("%w: invitation lifetime exceeds one year", ErrInvalidScope)
	}
	now := time.Now()
	inv := &Invite{
		ID:        uuid.NewString(),
		TeamID:    teamID,
		CreatedBy: createdBy,
		MaxUses:   options.MaxUses,
		UseCount:  0,
		ExpiresAt: now.Add(options.TTL).UnixMilli(),
		CreateAt:  now.UnixMilli(),
		Kind:      options.Kind, ChannelIDs: channelIDs,
		GuestExpiresAfterSeconds: int64(options.GuestAccessTTL / time.Second),
		GuestFileDownload:        options.GuestFileDownload,
	}
	_, err = s.db.Pool.Exec(ctx, `
		INSERT INTO invite_tokens (
			id, team_id, created_by, max_uses, use_count, expires_at, revoked_at, create_at,
			invite_kind, channel_ids, guest_expires_after_seconds, guest_file_download
		) VALUES ($1,$2,$3,$4,0,$5,0,$6,$7,$8,$9,$10)
	`, inv.ID, inv.TeamID, inv.CreatedBy, inv.MaxUses, inv.ExpiresAt, inv.CreateAt,
		inv.Kind, inv.ChannelIDs, inv.GuestExpiresAfterSeconds, inv.GuestFileDownload)
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
		SELECT id, team_id, created_by, max_uses, use_count, expires_at, revoked_at, create_at,
		       invite_kind, channel_ids, guest_expires_after_seconds, guest_file_download
		FROM invite_tokens WHERE id = $1
	`, id).Scan(&inv.ID, &inv.TeamID, &inv.CreatedBy, &inv.MaxUses, &inv.UseCount, &inv.ExpiresAt, &inv.RevokedAt, &inv.CreateAt,
		&inv.Kind, &inv.ChannelIDs, &inv.GuestExpiresAfterSeconds, &inv.GuestFileDownload)
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
	details, err := s.ConsumeDetails(ctx, id)
	if err != nil {
		return "", err
	}
	return details.TeamID, nil
}

// ConsumeDetails is the scope-aware counterpart used by registration-like
// callers. New account creation uses the same RETURNING shape inside its own
// larger transaction so invite consumption and membership remain atomic.
func (s *Service) ConsumeDetails(ctx context.Context, id string) (Consumption, error) {
	now := time.Now().UnixMilli()
	var details Consumption
	row := s.db.Pool.QueryRow(ctx, `
		UPDATE invite_tokens
		SET use_count = use_count + 1
		WHERE id = $1
		  AND revoked_at = 0
		  AND expires_at > $2
		  AND (max_uses = 0 OR use_count < max_uses)
		RETURNING team_id, invite_kind, channel_ids,
		          guest_expires_after_seconds, guest_file_download
	`, id, now)
	if err := row.Scan(&details.TeamID, &details.Kind, &details.ChannelIDs,
		&details.GuestExpiresAfterSeconds, &details.GuestFileDownload); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Consumption{}, ErrInvalidInvite
		}
		return Consumption{}, err
	}
	return details, nil
}

// ListForTeam returns every non-revoked invite for the given team, newest
// first. Expired invites are still included so the admin UI can show them
// greyed out until explicitly deleted.
func (s *Service) ListForTeam(ctx context.Context, teamID string) ([]Invite, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, team_id, created_by, max_uses, use_count, expires_at, revoked_at, create_at,
		       invite_kind, channel_ids, guest_expires_after_seconds, guest_file_download
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
		if err := rows.Scan(&inv.ID, &inv.TeamID, &inv.CreatedBy, &inv.MaxUses, &inv.UseCount, &inv.ExpiresAt, &inv.RevokedAt, &inv.CreateAt,
			&inv.Kind, &inv.ChannelIDs, &inv.GuestExpiresAfterSeconds, &inv.GuestFileDownload); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func normalizeChannelIDs(values []string) ([]string, error) {
	if len(values) > 100 {
		return nil, fmt.Errorf("%w: too many channels", ErrInvalidScope)
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("%w: invalid channel id", ErrInvalidScope)
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out, nil
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
