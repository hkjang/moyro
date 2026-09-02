// Package threads owns thread-membership (follow/unfollow + read state).
//
// In Mattermost, every reply implicitly follows you to the root, and a user
// can also explicitly follow/unfollow a thread. The webapp dims a thread
// once `last_viewed_at >= last_updated_at`. This service is the durable
// store behind those queries.
//
// We persist one row per (user_id, root_id). team_id is denormalised onto
// the row so the "mark all threads in this team as read" path doesn't have
// to walk posts→channels→teams. Callers (the posts handler) keep the field
// in sync when a user replies in a thread.
package threads

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/hkjang/moyro/server/internal/store"
)

var ErrNotFound = errors.New("thread membership not found")

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

// Membership mirrors Mattermost's `ThreadResponse` shape (subset). Fields
// the official client renders: last_viewed_at, last_updated_at,
// unread_replies, unread_mentions, following.
type Membership struct {
	UserID         string `json:"user_id"`
	TeamID         string `json:"team_id"`
	RootID         string `json:"root_id"`
	LastViewedAt   int64  `json:"last_viewed_at"`
	LastUpdatedAt  int64  `json:"last_updated_at"`
	UnreadMentions int    `json:"unread_mentions"`
	UnreadReplies  int    `json:"unread_replies"`
	Following      bool   `json:"following"`
}

// SetFollowing toggles the following flag. Creates the row if missing so a
// user can "follow" a thread they haven't replied to yet — clicking
// "Follow" in the UI on a thread you've only read.
func (s *Service) SetFollowing(ctx context.Context, userID, teamID, rootID string, following bool) error {
	now := time.Now().UnixMilli()
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO thread_memberships (user_id, team_id, root_id, following, last_updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, root_id) DO UPDATE SET
			following = EXCLUDED.following,
			team_id   = COALESCE(NULLIF(EXCLUDED.team_id,''), thread_memberships.team_id)
	`, userID, teamID, rootID, following, now)
	return err
}

// MarkRead stamps last_viewed_at on a single thread membership. Mattermost
// uses this to track per-thread read state separately from per-channel.
// Returns the resulting timestamp so callers can broadcast it.
func (s *Service) MarkRead(ctx context.Context, userID, teamID, rootID string, viewedAt int64) (int64, error) {
	if viewedAt <= 0 {
		viewedAt = time.Now().UnixMilli()
	}
	// The row is keyed by (user, root); the team is only needed when a row
	// has to be created below. Binding it here as an unused parameter made
	// PostgreSQL reject the statement (42P18) for every existing membership.
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE thread_memberships
		SET last_viewed_at = $3,
		    unread_mentions = 0,
		    unread_replies = 0
		WHERE user_id = $1 AND root_id = $2
	`, userID, rootID, viewedAt)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 0 {
		// Auto-create a fresh row at viewed=now so a "mark read" action on a
		// thread the user has only browsed (never replied to) succeeds. This
		// matches Mattermost's behaviour — clicking "Mark Read" never 404s.
		_, err = s.db.Pool.Exec(ctx, `
			INSERT INTO thread_memberships (user_id, team_id, root_id, last_viewed_at, following)
			VALUES ($1, $2, $3, $4, FALSE)
			ON CONFLICT (user_id, root_id) DO UPDATE SET
				last_viewed_at = EXCLUDED.last_viewed_at
		`, userID, teamID, rootID, viewedAt)
		if err != nil {
			return 0, err
		}
	}
	return viewedAt, nil
}

// MarkAllReadInTeam stamps last_viewed_at on every thread membership in a
// team. Mirrors `PUT /users/{user}/teams/{team}/threads/read`.
func (s *Service) MarkAllReadInTeam(ctx context.Context, userID, teamID string) (int64, error) {
	now := time.Now().UnixMilli()
	_, err := s.db.Pool.Exec(ctx, `
		UPDATE thread_memberships
		SET last_viewed_at = $3,
		    unread_mentions = 0,
		    unread_replies = 0
		WHERE user_id = $1 AND team_id = $2
	`, userID, teamID, now)
	if err != nil {
		return 0, err
	}
	return now, nil
}

// MarkUnreadFromPost rewinds the thread's last_viewed_at to (postCreateAt - 1)
// so the given reply becomes the first unread row in the panel. Mirrors
// `POST /users/{uid}/teams/{tid}/threads/{rid}/set_unread/{pid}`. Auto-
// creates the membership row when missing — same forgiveness as MarkRead —
// because a user can hit "set unread" on a thread they only browsed.
// Returns the resulting last_viewed_at boundary so the handler can echo it
// back to the client and broadcast it.
func (s *Service) MarkUnreadFromPost(ctx context.Context, userID, teamID, rootID string, postCreateAt int64) (int64, error) {
	if postCreateAt <= 0 {
		postCreateAt = time.Now().UnixMilli()
	}
	boundary := postCreateAt - 1
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE thread_memberships
		SET last_viewed_at = $3
		WHERE user_id = $1 AND root_id = $2
	`, userID, rootID, boundary)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 0 {
		_, err = s.db.Pool.Exec(ctx, `
			INSERT INTO thread_memberships (user_id, team_id, root_id, last_viewed_at, following)
			VALUES ($1, $2, $3, $4, FALSE)
			ON CONFLICT (user_id, root_id) DO UPDATE SET
				last_viewed_at = EXCLUDED.last_viewed_at
		`, userID, teamID, rootID, boundary)
		if err != nil {
			return 0, err
		}
	}
	return boundary, nil
}

// Get returns a single membership row. Used by the handlers as a precheck
// so a 404 surfaces cleanly.
func (s *Service) Get(ctx context.Context, userID, rootID string) (*Membership, error) {
	var m Membership
	err := s.db.Pool.QueryRow(ctx, `
		SELECT user_id, team_id, root_id, last_viewed_at, last_updated_at,
		       unread_mentions, unread_replies, following
		FROM thread_memberships
		WHERE user_id=$1 AND root_id=$2
	`, userID, rootID).Scan(&m.UserID, &m.TeamID, &m.RootID, &m.LastViewedAt,
		&m.LastUpdatedAt, &m.UnreadMentions, &m.UnreadReplies, &m.Following)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}
