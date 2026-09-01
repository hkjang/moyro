// Package registration owns the atomic local-account onboarding transaction.
// It deliberately spans the invite, user, team, and channel tables: consuming
// a limited invite before an account exists (or creating the account before
// consuming the invite) leaves an externally visible partial state on failure.
package registration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/auth"
	"github.com/hkjang/moyro/server/internal/invites"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

type Input struct {
	Username string
	Email    string
	Password string
	InviteID string
}

type registrationTx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Commit(context.Context) error
	Rollback(context.Context) error
}

type Service struct {
	begin func(context.Context) (registrationTx, error)
	now   func() time.Time
}

func New(db *store.DB) *Service {
	return &Service{
		begin: func(ctx context.Context) (registrationTx, error) {
			return db.Pool.Begin(ctx)
		},
		now: time.Now,
	}
}

// Register consumes the invite (when supplied), creates the user, and grants
// both default-workspace and invited-team memberships in one transaction. The
// conditional UPDATE locks a limited invite row; concurrent callers racing for
// its final use are re-checked after the winner commits and fail before user
// insertion.
func (s *Service) Register(ctx context.Context, input Input) (*auth.User, string, error) {
	input.Username = strings.ToLower(strings.TrimSpace(input.Username))
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.InviteID = strings.TrimSpace(input.InviteID)
	if input.Username == "" || input.Email == "" {
		return nil, "", errors.New("registration: username and email are required")
	}
	if err := auth.ValidatePassword(input.Password); err != nil {
		return nil, "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, "", err
	}

	tx, err := s.begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback(ctx)

	now := s.now().UnixMilli()
	inviteTeamID := ""
	inviteKind := invites.KindMember
	inviteChannelIDs := []string{}
	guestExpiresAfterSeconds := int64(0)
	guestFileDownload := true
	if input.InviteID != "" {
		err = tx.QueryRow(ctx, `
			UPDATE invite_tokens AS invite
			SET use_count = invite.use_count + 1
			FROM teams AS team
			WHERE invite.id = $1
			  AND team.id = invite.team_id
			  AND team.delete_at = 0
			  AND invite.revoked_at = 0
			  AND invite.expires_at > $2
			  AND (invite.max_uses = 0 OR invite.use_count < invite.max_uses)
			  AND (
				invite.invite_kind = 'member'
				OR (
					cardinality(invite.channel_ids) > 0
					AND NOT EXISTS (
						SELECT 1
						FROM unnest(invite.channel_ids) AS requested(channel_id)
						LEFT JOIN channels c ON c.id=requested.channel_id
							AND c.team_id=invite.team_id AND c.delete_at=0 AND c.type IN ('O','P')
						WHERE c.id IS NULL
					)
				)
			  )
			RETURNING invite.team_id, invite.invite_kind, invite.channel_ids,
			          invite.guest_expires_after_seconds, invite.guest_file_download
		`, input.InviteID, now).Scan(&inviteTeamID, &inviteKind, &inviteChannelIDs,
			&guestExpiresAfterSeconds, &guestFileDownload)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", invites.ErrInvalidInvite
		}
		if err != nil {
			return nil, "", err
		}
	}

	roles := "system_user"
	guestExpiresAt := int64(0)
	if inviteKind == invites.KindGuest {
		roles = "system_guest"
		guestExpiresAt = now + guestExpiresAfterSeconds*1000
	}
	user := &auth.User{
		ID: uuid.NewString(), Username: input.Username, Email: input.Email, Roles: roles,
		GuestExpiresAt: guestExpiresAt, GuestFileDownload: guestFileDownload,
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO users (
			id, username, email, password_hash, roles, guest_expires_at,
			guest_file_download, create_at, update_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)
	`, user.ID, user.Username, user.Email, string(hash), user.Roles,
		user.GuestExpiresAt, user.GuestFileDownload, now); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, "", auth.ErrUserExists
		}
		return nil, "", err
	}

	if inviteKind == invites.KindGuest {
		if inviteTeamID == "" || len(inviteChannelIDs) == 0 || guestExpiresAfterSeconds <= 0 {
			return nil, "", invites.ErrInvalidInvite
		}
		if err := joinGuestScope(ctx, tx, inviteTeamID, inviteChannelIDs, user.ID, now); err != nil {
			return nil, "", err
		}
	} else {
		defaultTeamID, err := ensureTeam(ctx, tx, now)
		if err != nil {
			return nil, "", err
		}
		if err := joinTeamAndGeneral(ctx, tx, defaultTeamID, user.ID, now); err != nil {
			return nil, "", err
		}
		if inviteTeamID != "" && inviteTeamID != defaultTeamID {
			if err := joinTeamAndGeneral(ctx, tx, inviteTeamID, user.ID, now); err != nil {
				return nil, "", err
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	return user, inviteTeamID, nil
}

func joinGuestScope(ctx context.Context, tx registrationTx, teamID string, channelIDs []string, userID string, now int64) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES ($1,$2,'team_user',$3)
		ON CONFLICT (team_id, user_id) DO NOTHING
	`, teamID, userID, now); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO channel_members (channel_id, user_id, roles, create_at)
		SELECT c.id, $2, 'channel_user', $3
		FROM channels c
		WHERE c.id=ANY($1::text[]) AND c.team_id=$4 AND c.delete_at=0 AND c.type IN ('O','P')
		ON CONFLICT (channel_id, user_id) DO NOTHING
	`, channelIDs, userID, now, teamID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != int64(len(channelIDs)) {
		return invites.ErrInvalidInvite
	}
	return nil
}

func ensureTeam(ctx context.Context, tx registrationTx, now int64) (string, error) {
	var teamID string
	var deleteAt int64
	err := tx.QueryRow(ctx, `
		INSERT INTO teams (id, name, display_name, type, create_at, update_at)
		VALUES ($1,'town-square','Town Square','O',$2,$2)
		ON CONFLICT (name) DO UPDATE SET update_at=teams.update_at
		RETURNING id, delete_at
	`, uuid.NewString(), now).Scan(&teamID, &deleteAt)
	if err != nil {
		return "", err
	}
	if deleteAt != 0 {
		return "", errors.New("registration: default workspace is inactive")
	}
	return teamID, nil
}

func joinTeamAndGeneral(ctx context.Context, tx registrationTx, teamID, userID string, now int64) error {
	var channelID string
	var deleteAt int64
	err := tx.QueryRow(ctx, `
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES ($1,$2,'O','General','general',$3,$3)
		ON CONFLICT (team_id, name) DO UPDATE SET update_at=channels.update_at
		RETURNING id, delete_at
	`, uuid.NewString(), teamID, now).Scan(&channelID, &deleteAt)
	if err != nil {
		return err
	}
	if deleteAt != 0 {
		return fmt.Errorf("registration: general channel for team %s is inactive", teamID)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES ($1,$2,'team_user',$3)
		ON CONFLICT (team_id, user_id) DO NOTHING
	`, teamID, userID, now); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO channel_members (channel_id, user_id, roles, create_at)
		VALUES ($1,$2,'channel_user',$3)
		ON CONFLICT (channel_id, user_id) DO NOTHING
	`, channelID, userID, now)
	return err
}
