// Package postacks implements Mattermost's "post acknowledgements" feature:
// when a sender wants explicit confirmation that recipients have read an
// important message, they tag the post with `requested_ack: true` (in
// `posts.props`), and recipients click an "Ack" button to record one row per
// (post_id, user_id) in this package's table. The list endpoint hydrates a
// post-ack badge on the recipient's UI.
package postacks

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hkjang/moyro/server/internal/store"
)

// Ack is one acknowledgement row. We intentionally leak the wire-shape names
// (acknowledged_at) used by the official Mattermost desktop client so the
// JSON marshals straight through without a separate adapter.
type Ack struct {
	PostID         string `json:"post_id"`
	UserID         string `json:"user_id"`
	AcknowledgedAt int64  `json:"acknowledged_at"`
}

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

// Save records an ack. Idempotent (re-acking the same post just touches
// ack_at). Returns the persisted row so the handler can broadcast a WS event
// with a stable timestamp.
func (s *Service) Save(ctx context.Context, postID, userID string) (*Ack, error) {
	if postID == "" || userID == "" {
		return nil, errors.New("post_id and user_id required")
	}
	now := time.Now().UnixMilli()
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO post_acknowledgements (post_id, user_id, ack_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (post_id, user_id) DO UPDATE SET ack_at = EXCLUDED.ack_at
	`, postID, userID, now)
	if err != nil {
		return nil, err
	}
	return &Ack{PostID: postID, UserID: userID, AcknowledgedAt: now}, nil
}

// Delete removes an ack. Returns (true, nil) if the row was actually deleted,
// (false, nil) if the user had never ack'd this post — both are 200-OK
// outcomes for the caller; the boolean is just for audit-log fidelity.
func (s *Service) Delete(ctx context.Context, postID, userID string) (bool, error) {
	tag, err := s.db.Pool.Exec(ctx, `
		DELETE FROM post_acknowledgements WHERE post_id=$1 AND user_id=$2
	`, postID, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ListForPost returns every ack for the given post, oldest first so the UI
// can render "ack'd by Alice 2 minutes ago, then Bob 30s ago".
func (s *Service) ListForPost(ctx context.Context, postID string) ([]Ack, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT post_id, user_id, ack_at FROM post_acknowledgements
		WHERE post_id=$1 ORDER BY ack_at ASC
	`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Ack{}
	for rows.Next() {
		var a Ack
		if err := rows.Scan(&a.PostID, &a.UserID, &a.AcknowledgedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListForPosts is a bulk lookup keyed by post_id → []Ack so the post-stream
// hydration only does one round-trip. Cap input at 200 ids.
func (s *Service) ListForPosts(ctx context.Context, postIDs []string) (map[string][]Ack, error) {
	out := map[string][]Ack{}
	if len(postIDs) == 0 {
		return out, nil
	}
	if len(postIDs) > 200 {
		postIDs = postIDs[:200]
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT post_id, user_id, ack_at FROM post_acknowledgements
		WHERE post_id = ANY($1) ORDER BY post_id, ack_at ASC
	`, postIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Ack
		if err := rows.Scan(&a.PostID, &a.UserID, &a.AcknowledgedAt); err != nil {
			return nil, err
		}
		out[a.PostID] = append(out[a.PostID], a)
	}
	return out, rows.Err()
}

// Get returns one ack row, or (nil, nil) if the user hasn't ack'd. Used by
// the handler to short-circuit DELETE before touching the table.
func (s *Service) Get(ctx context.Context, postID, userID string) (*Ack, error) {
	var a Ack
	err := s.db.Pool.QueryRow(ctx, `
		SELECT post_id, user_id, ack_at FROM post_acknowledgements
		WHERE post_id=$1 AND user_id=$2
	`, postID, userID).Scan(&a.PostID, &a.UserID, &a.AcknowledgedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}
