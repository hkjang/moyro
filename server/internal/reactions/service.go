package reactions

import (
	"context"
	"time"

	"github.com/hkjang/moyro/server/internal/store"
)

type Reaction struct {
	UserID    string `json:"user_id"`
	PostID    string `json:"post_id"`
	EmojiName string `json:"emoji_name"`
	CreateAt  int64  `json:"create_at"`
}

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

// Add inserts a reaction, ignoring duplicates. The returned reaction
// reflects the existing row's create_at if the reaction already existed.
func (s *Service) Add(ctx context.Context, userID, postID, emoji string) (*Reaction, error) {
	now := time.Now().UnixMilli()
	var createAt int64
	err := s.db.Pool.QueryRow(ctx, `
		INSERT INTO reactions (user_id, post_id, emoji_name, create_at)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (user_id, post_id, emoji_name) DO UPDATE SET create_at = reactions.create_at
		RETURNING create_at
	`, userID, postID, emoji, now).Scan(&createAt)
	if err != nil {
		return nil, err
	}
	return &Reaction{UserID: userID, PostID: postID, EmojiName: emoji, CreateAt: createAt}, nil
}

func (s *Service) Remove(ctx context.Context, userID, postID, emoji string) error {
	_, err := s.db.Pool.Exec(ctx, `
		DELETE FROM reactions WHERE user_id=$1 AND post_id=$2 AND emoji_name=$3
	`, userID, postID, emoji)
	return err
}

func (s *Service) ListForPost(ctx context.Context, postID string) ([]Reaction, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT user_id, post_id, emoji_name, create_at
		FROM reactions WHERE post_id=$1 ORDER BY create_at ASC
	`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Reaction{}
	for rows.Next() {
		var r Reaction
		if err := rows.Scan(&r.UserID, &r.PostID, &r.EmojiName, &r.CreateAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ChannelForPost returns the channel_id of a post (used for authz + broadcast targeting).
func (s *Service) ChannelForPost(ctx context.Context, postID string) (string, error) {
	var ch string
	err := s.db.Pool.QueryRow(ctx, `SELECT channel_id FROM posts WHERE id=$1 AND delete_at=0`, postID).Scan(&ch)
	return ch, err
}
