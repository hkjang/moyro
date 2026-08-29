// Package emojis manages custom team emojis. Each emoji is a (name → file)
// pair: we upload the raw image through the existing files.Service so the
// storage/download plumbing is shared, then index it in the `emojis` table
// by a short, lowercased name that clients can reference from reactions
// and messages.
//
// Access model: admins can delete any emoji, users can only delete their
// own. Creation requires a logged-in user (enforced at the HTTP layer).
package emojis

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/hkjang/moyro/server/internal/files"
	"github.com/hkjang/moyro/server/internal/store"
)

type Emoji struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatorID string `json:"creator_id"`
	FileID    string `json:"file_id"`
	CreateAt  int64  `json:"create_at"`
	DeleteAt  int64  `json:"delete_at"`
}

type Service struct {
	db    *store.DB
	files *files.Service
}

func New(db *store.DB, f *files.Service) *Service {
	return &Service{db: db, files: f}
}

// nameRE mirrors Mattermost's convention: lowercase ASCII letters, digits,
// underscore and hyphen. 1..40 chars. Anything else is rejected up front so
// we don't litter the DB with emoji no client will ever render.
var nameRE = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)

// maxEmojiSize caps uploads at 256KB. Most legitimate emojis are <50KB;
// the cap keeps a single user from bloating storage and also sets an upper
// bound on the synchronous decode inside files.Service.
const maxEmojiSize = 256 * 1024

var (
	ErrInvalidName   = errors.New("invalid emoji name")
	ErrNameTaken     = errors.New("emoji name already in use")
	ErrNotFound      = errors.New("emoji not found")
	ErrForbidden     = errors.New("not allowed to delete this emoji")
	ErrTooLarge      = errors.New("emoji too large")
	ErrUnsupportedMIME = errors.New("unsupported emoji image type")
)

// Create uploads the image through files.Service and inserts the row. The
// caller is responsible for validating the image byte-stream size before
// passing it in; we re-check here from the uploaded FileInfo to be safe.
func (s *Service) Create(ctx context.Context, creatorID, name, mime string, body io.Reader) (*Emoji, error) {
	if !nameRE.MatchString(name) {
		return nil, ErrInvalidName
	}
	if !isSupportedMIME(mime) {
		return nil, ErrUnsupportedMIME
	}

	// Probe for name collision before the upload so we don't orphan a file
	// on conflict. The unique index on (name) WHERE delete_at=0 is still
	// the source of truth for race protection below.
	var dummy string
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id FROM emojis WHERE name=$1 AND delete_at=0
	`, name).Scan(&dummy)
	if err == nil {
		return nil, ErrNameTaken
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	// Reuse the files upload path. channelID="" => unattached file.
	fi, err := s.files.Upload(ctx, creatorID, "", fmt.Sprintf("emoji-%s", name), mime, body)
	if err != nil {
		return nil, err
	}
	if fi.Size > maxEmojiSize {
		// Clean up: the file row exists with a path; we don't currently
		// have a public Delete() on files.Service, so we soft-abort by
		// reporting the error — a future cleanup pass can reclaim it.
		return nil, ErrTooLarge
	}

	id := uuid.NewString()
	now := time.Now().UnixMilli()
	if _, err := s.db.Pool.Exec(ctx, `
		INSERT INTO emojis (id, name, creator_id, file_id, create_at)
		VALUES ($1,$2,$3,$4,$5)
	`, id, name, creatorID, fi.ID, now); err != nil {
		// Duplicate-name race: someone inserted concurrently between our
		// probe and this insert. Return the human-friendly error.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrNameTaken
		}
		return nil, err
	}
	return &Emoji{
		ID:        id,
		Name:      name,
		CreatorID: creatorID,
		FileID:    fi.ID,
		CreateAt:  now,
	}, nil
}

// GetByName resolves by lowercased name. Returns ErrNotFound when no live
// emoji matches — callers use this to translate :shortcode: references to
// image URLs without holding the full list client-side.
func (s *Service) GetByName(ctx context.Context, name string) (*Emoji, error) {
	e := &Emoji{}
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, name, creator_id, file_id, create_at, delete_at
		FROM emojis WHERE name=$1 AND delete_at=0
	`, name).Scan(&e.ID, &e.Name, &e.CreatorID, &e.FileID, &e.CreateAt, &e.DeleteAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return e, nil
}

// Get resolves by id.
func (s *Service) Get(ctx context.Context, id string) (*Emoji, error) {
	e := &Emoji{}
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, name, creator_id, file_id, create_at, delete_at
		FROM emojis WHERE id=$1 AND delete_at=0
	`, id).Scan(&e.ID, &e.Name, &e.CreatorID, &e.FileID, &e.CreateAt, &e.DeleteAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return e, nil
}

// List returns a page of live emojis, newest first. Caller-supplied page
// size is clamped to a sane range so a single request can't DoS the DB.
func (s *Service) List(ctx context.Context, page, perPage int) ([]Emoji, error) {
	if page < 0 {
		page = 0
	}
	if perPage <= 0 || perPage > 200 {
		perPage = 60
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, name, creator_id, file_id, create_at, delete_at
		FROM emojis WHERE delete_at=0
		ORDER BY create_at DESC
		LIMIT $1 OFFSET $2
	`, perPage, page*perPage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Emoji{}
	for rows.Next() {
		var e Emoji
		if err := rows.Scan(&e.ID, &e.Name, &e.CreatorID, &e.FileID, &e.CreateAt, &e.DeleteAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Delete soft-deletes. Admins can delete anything; non-admins can only
// delete their own. The caller passes `isAdmin` explicitly so this service
// doesn't need to know about roles.
func (s *Service) Delete(ctx context.Context, actorID, emojiID string, isAdmin bool) error {
	e, err := s.Get(ctx, emojiID)
	if err != nil {
		return err
	}
	if !isAdmin && e.CreatorID != actorID {
		return ErrForbidden
	}
	_, err = s.db.Pool.Exec(ctx, `
		UPDATE emojis SET delete_at=$1 WHERE id=$2 AND delete_at=0
	`, time.Now().UnixMilli(), emojiID)
	return err
}

func isSupportedMIME(m string) bool {
	switch m {
	case "image/png", "image/jpeg", "image/jpg", "image/gif", "image/webp":
		return true
	}
	return false
}
