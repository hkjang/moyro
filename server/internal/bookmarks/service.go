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

// Patch addresses the row through its channel. The handler authorizes the
// caller against the channel in the request path, so resolving by id alone let
// that authorization stand in for a channel the row does not belong to.
func (s *Service) Patch(ctx context.Context, channelID, id string, p Patch) (*Bookmark, error) {
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
		WHERE id=$1 AND channel_id=$15 AND delete_at=0
	`,
		id,
		p.DisplayName != nil, derefStr(p.DisplayName),
		p.LinkURL != nil, derefStr(p.LinkURL),
		p.ImageURL != nil, derefStr(p.ImageURL),
		p.Emoji != nil, derefStr(p.Emoji),
		p.FileID != nil, derefStr(p.FileID),
		p.SortOrder != nil, derefInt(p.SortOrder),
		now,
		channelID,
	)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.Get(ctx, id)
}

// Delete soft-deletes a bookmark inside one channel. Returns ErrNotFound if
// the channel has no such active row.
func (s *Service) Delete(ctx context.Context, channelID, id string) error {
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE channel_bookmarks SET delete_at=$2, update_at=$2
		WHERE id=$1 AND channel_id=$3 AND delete_at=0
	`, id, time.Now().UnixMilli(), channelID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Reorder moves `id` to position newPos within its channel and rewrites every
// active position in that channel to a dense 0..n-1 sequence, so no two
// bookmarks in one channel ever share a sort_order. Writing only the target
// row would leave duplicates behind, and List breaks ties on create_at — so
// the older bookmark would stay in front and the move would land a slot off
// its target, or be swallowed entirely.
//
// newPos is 0-based, matching Mattermost's endpoint, and is clamped into range
// rather than rejected: an out-of-range value means "either end", which is the
// response contract callers already have. Note Create still appends with
// COALESCE(MAX(sort_order),0)+1, so a channel that has never been reordered
// starts at 1 while a reordered one starts at 0. List exposes only relative
// order, so that asymmetry is invisible to clients.
//
// The read that decides the sequence and the write that applies it share one
// transaction. Without that, two concurrent reorders can interleave and
// recreate the duplicate positions this exists to prevent.
func (s *Service) Reorder(ctx context.Context, channelID, id string, newPos int) error {
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Same ordering as List, so the sequence being rewritten is the one the
	// client was looking at when it asked for the move.
	rows, err := tx.Query(ctx, `
		SELECT id FROM channel_bookmarks
		WHERE channel_id=$1 AND delete_at=0
		ORDER BY sort_order ASC, create_at ASC
	`, channelID)
	if err != nil {
		return err
	}
	current := []string{}
	for rows.Next() {
		var rowID string
		if err := rows.Scan(&rowID); err != nil {
			rows.Close()
			return err
		}
		current = append(current, rowID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	from := -1
	for i, rowID := range current {
		if rowID == id {
			from = i
			break
		}
	}
	if from < 0 {
		return ErrNotFound
	}

	rest := make([]string, 0, len(current))
	rest = append(rest, current[:from]...)
	rest = append(rest, current[from+1:]...)
	if newPos < 0 {
		newPos = 0
	}
	if newPos > len(rest) {
		newPos = len(rest)
	}
	ordered := make([]string, 0, len(current))
	ordered = append(ordered, rest[:newPos]...)
	ordered = append(ordered, id)
	ordered = append(ordered, rest[newPos:]...)

	positions := make([]int, len(ordered))
	for i := range ordered {
		positions[i] = i
	}
	// One statement for the whole channel. The channel and delete_at filters
	// keep another channel's rows and soft-deleted rows out of the rewrite.
	if _, err := tx.Exec(ctx, `
		UPDATE channel_bookmarks b
		SET sort_order=x.sort_order, update_at=$1
		FROM unnest($2::text[], $3::int[]) AS x(id, sort_order)
		WHERE b.id = x.id AND b.channel_id=$4 AND b.delete_at=0
	`, time.Now().UnixMilli(), ordered, positions, channelID); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
