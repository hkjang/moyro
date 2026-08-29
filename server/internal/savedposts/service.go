// Package savedposts implements personal bookmarks. Each row is a
// (user, post) pair with its own create_at timestamp — the bookmark's
// create_at, not the post's, so the bookmark list reads chronologically
// by "when you starred it" rather than "when the post was written".
//
// The service is deliberately minimal; post hydration happens via the
// posts service so we don't duplicate column lists or WS fan-out here.
package savedposts

import (
	"context"
	"time"

	"github.com/hkjang/moyro/server/internal/store"
)

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

// Save bookmarks a post. Idempotent — starring twice is a no-op. Returns
// true on a fresh insert, false when the row already existed (used by the
// handler to skip the broadcast when nothing actually changed).
func (s *Service) Save(ctx context.Context, userID, postID string) (bool, error) {
	tag, err := s.db.Pool.Exec(ctx, `
		INSERT INTO saved_posts (user_id, post_id, create_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, post_id) DO NOTHING
	`, userID, postID, time.Now().UnixMilli())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Unsave removes a bookmark. Returns true if a row was deleted (used by the
// handler to decide whether to broadcast a saved_post_changed event).
func (s *Service) Unsave(ctx context.Context, userID, postID string) (bool, error) {
	tag, err := s.db.Pool.Exec(ctx, `DELETE FROM saved_posts WHERE user_id=$1 AND post_id=$2`, userID, postID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ListIDs returns post ids the user has bookmarked, newest-starred first.
// Bounded by the provided limit + offset; caller hydrates posts via
// posts.ListByIDs so the column set stays consistent with other post APIs.
func (s *Service) ListIDs(ctx context.Context, userID string, limit, offset int) ([]string, error) {
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT sp.post_id
		FROM saved_posts sp
		JOIN posts p ON p.id=sp.post_id AND p.delete_at=0
		JOIN channel_members cm ON cm.channel_id=p.channel_id AND cm.user_id=sp.user_id
		WHERE sp.user_id=$1
		ORDER BY sp.create_at DESC
		LIMIT $2 OFFSET $3
	`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// IsSavedBulk returns a map[postID]bool for the given post ids, seen from
// the perspective of `userID`. Used by the post-stream renderer to show
// filled vs. outline stars without a round-trip per post.
func (s *Service) IsSavedBulk(ctx context.Context, userID string, postIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(postIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT sp.post_id
		FROM saved_posts sp
		JOIN posts p ON p.id=sp.post_id AND p.delete_at=0
		JOIN channel_members cm ON cm.channel_id=p.channel_id AND cm.user_id=sp.user_id
		WHERE sp.user_id=$1 AND sp.post_id = ANY($2)
	`, userID, postIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
