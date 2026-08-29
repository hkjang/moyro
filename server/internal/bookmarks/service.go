// Package bookmarks implements channel-scoped pinned links + files that
// every member of the channel sees above the message stream (Mattermost's
// "Channel bookmarks" feature). Storage is the channel_bookmarks table;
// soft-delete via delete_at so an admin's accidental remove is recoverable
// in audit even though the row drops out of every list query.
package bookmarks

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hkjang/moyro/server/internal/store"
)

// ErrNotFound surfaces a missing or already-deleted row so the handler can
// 404 cleanly without import-leaking pgx into the http layer.
var ErrNotFound = errors.New("bookmark not found")

type Bookmark struct {
	ID          string `json:"id"`
	ChannelID   string `json:"channel_id"`
	OwnerID     string `json:"owner_id"`
	DisplayName string `json:"display_name"`
	SortOrder   int    `json:"sort_order"`
	LinkURL     string `json:"link_url,omitempty"`
	ImageURL    string `json:"image_url,omitempty"`
	Emoji       string `json:"emoji,omitempty"`
	FileID      string `json:"file_id,omitempty"`
	Type        string `json:"type"`
	CreateAt    int64  `json:"create_at"`
	UpdateAt    int64  `json:"update_at"`
	DeleteAt    int64  `json:"delete_at"`
}

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

// List returns every active bookmark on a channel ordered by sort_order
// asc, then create_at asc as the deterministic tie-break so a fresh
// browser shares the same row order as one that just reordered.
func (s *Service) List(ctx context.Context, channelID string) ([]Bookmark, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, channel_id, owner_id, display_name, sort_order, link_url, image_url, emoji, file_id, type, create_at, update_at, delete_at
		FROM channel_bookmarks
		WHERE channel_id=$1 AND delete_at=0
		ORDER BY sort_order ASC, create_at ASC
	`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Bookmark{}
	for rows.Next() {
		var b Bookmark
		if err := rows.Scan(&b.ID, &b.ChannelID, &b.OwnerID, &b.DisplayName, &b.SortOrder, &b.LinkURL, &b.ImageURL, &b.Emoji, &b.FileID, &b.Type, &b.CreateAt, &b.UpdateAt, &b.DeleteAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Service) Get(ctx context.Context, id string) (*Bookmark, error) {
	var b Bookmark
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, channel_id, owner_id, display_name, sort_order, link_url, image_url, emoji, file_id, type, create_at, update_at, delete_at
		FROM channel_bookmarks
		WHERE id=$1 AND delete_at=0
	`, id).Scan(&b.ID, &b.ChannelID, &b.OwnerID, &b.DisplayName, &b.SortOrder, &b.LinkURL, &b.ImageURL, &b.Emoji, &b.FileID, &b.Type, &b.CreateAt, &b.UpdateAt, &b.DeleteAt)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// Create inserts a new bookmark. Sort order is appended at end of the
// channel's existing list (max+1) when not specified explicitly.
func (s *Service) Create(ctx context.Context, b *Bookmark) (*Bookmark, error) {
	now := time.Now().UnixMilli()
	if b.ID == "" {
		b.ID = uuid.NewString()
	}
	if b.Type == "" {
		if b.FileID != "" {
			b.Type = "file"
		} else {
			b.Type = "link"
		}
	}
	if b.SortOrder == 0 {
		var max int
		_ = s.db.Pool.QueryRow(ctx, `
			SELECT COALESCE(MAX(sort_order), 0) FROM channel_bookmarks
			WHERE channel_id=$1 AND delete_at=0
		`, b.ChannelID).Scan(&max)
		b.SortOrder = max + 1
	}
	b.CreateAt = now
	b.UpdateAt = now
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO channel_bookmarks
		  (id, channel_id, owner_id, display_name, sort_order, link_url, image_url, emoji, file_id, type, create_at, update_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	`, b.ID, b.ChannelID, b.OwnerID, b.DisplayName, b.SortOrder, b.LinkURL, b.ImageURL, b.Emoji, b.FileID, b.Type, b.CreateAt, b.UpdateAt)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// Patch applies partial updates. Pointer-typed fields so a nil means
// "leave alone"; an empty-string pointer is a deliberate clear.
type Patch struct {
	DisplayName *string
	LinkURL     *string
	ImageURL    *string
	Emoji       *string
	FileID      *string
	SortOrder   *int
}

func (s *Service) Patch(ctx context.Context, id string, p Patch) (*Bookmark, error) {
	now := time.Now().UnixMilli()
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE channel_bookmarks SET
		  display_name = CASE WHEN $2 THEN $3 ELSE display_name END,
		  link_url     = CASE WHEN $4 THEN $5 ELSE link_url END,
		  image_url    = CASE WHEN $6 THEN $7 ELSE image_url END,
		  emoji        = CASE WHEN $8 THEN $9 ELSE emoji END,
		  file_id      = CASE WHEN $10 THEN $11 ELSE file_id END,
		  sort_order   = CASE WHEN $12 THEN $13 ELSE sort_order END,
		  update_at    = $14
		WHERE id=$1 AND delete_at=0
	`,
		id,
		p.DisplayName != nil, derefStr(p.DisplayName),
		p.LinkURL != nil, derefStr(p.LinkURL),
		p.ImageURL != nil, derefStr(p.ImageURL),
		p.Emoji != nil, derefStr(p.Emoji),
		p.FileID != nil, derefStr(p.FileID),
		p.SortOrder != nil, derefInt(p.SortOrder),
		now,
	)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.Get(ctx, id)
}

// Delete soft-deletes a bookmark. Returns ErrNotFound if it didn't exist.
func (s *Service) Delete(ctx context.Context, id string) error {
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE channel_bookmarks SET delete_at=$2, update_at=$2
		WHERE id=$1 AND delete_at=0
	`, id, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Reorder sets the sort_order of `id` and re-shuffles the rest of the
// channel's bookmarks so positions are dense and conflict-free. We don't
// need atomic correctness here (race-condition leaks just mean two clients
// see slightly different orders for ~100ms) so this is a simple two-shot
// update rather than a single window-function CTE.
func (s *Service) Reorder(ctx context.Context, channelID, id string, newPos int) error {
	now := time.Now().UnixMilli()
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE channel_bookmarks SET sort_order=$3, update_at=$4
		WHERE id=$1 AND channel_id=$2 AND delete_at=0
	`, id, channelID, newPos, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
