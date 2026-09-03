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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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

// Create validates and buffers the image, uploads it through files.Service,
// and inserts the row. Every failure after the upload reclaims the file, so a
// rejected attempt leaves nothing behind.
func (s *Service) Create(ctx context.Context, creatorID, name, mime string, body io.Reader) (*Emoji, error) {
	if !nameRE.MatchString(name) {
		return nil, ErrInvalidName
	}
	if !isSupportedMIME(mime) {
		return nil, ErrUnsupportedMIME
	}

	// Buffer the image before storage sees it. Reading one byte past the cap
	// is enough to tell "exactly at the limit" from "over it", and it keeps an
	// oversized upload from ever reaching files.Service: the size check used to
	// run on the returned FileInfo, so every too-large attempt wrote the bytes
	// and a file_infos row and only then reported ErrTooLarge, leaving both
	// behind forever. A 256KB buffer is well under the 1MiB the multipart
	// parser in front of this already holds in memory.
	image, err := io.ReadAll(io.LimitReader(body, maxEmojiSize+1))
	if err != nil {
		return nil, err
	}
	if len(image) > maxEmojiSize {
		return nil, ErrTooLarge
	}
	// The declared MIME arrives in a client-supplied multipart header, so trust
	// the bytes instead: sniff the real format and store that. An emoji whose
	// content type says one thing and whose bytes say another is served back to
	// every client from our own origin.
	sniffed, ok := sniffImageMIME(image)
	if !ok {
		return nil, ErrUnsupportedMIME
	}

	// Probe for name collision before the upload so we don't upload a file we
	// already know we can't index. The partial unique index on (name) WHERE
	// delete_at=0 is still the source of truth for race protection below.
	var dummy string
	err = s.db.Pool.QueryRow(ctx, `
		SELECT id FROM emojis WHERE name=$1 AND delete_at=0
	`, name).Scan(&dummy)
	if err == nil {
		return nil, ErrNameTaken
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	// Reuse the files upload path. channelID="" => unattached file.
	fi, err := s.files.Upload(ctx, creatorID, "", fmt.Sprintf("emoji-%s", name), sniffed, bytes.NewReader(image))
	if err != nil {
		return nil, err
	}

	id := uuid.NewString()
	now := time.Now().UnixMilli()
	if _, err := s.db.Pool.Exec(ctx, `
		INSERT INTO emojis (id, name, creator_id, file_id, create_at)
		VALUES ($1,$2,$3,$4,$5)
	`, id, name, creatorID, fi.ID, now); err != nil {
		// Nothing references the upload now, so hand it back to files.Service
		// instead of stranding it.
		s.discard(ctx, fi.ID)
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

// discard reclaims an upload on an abort path. It runs on its own timeout
// because the request context may already be the reason we're aborting, and a
// failed cleanup must not mask the error that triggered it.
func (s *Service) discard(ctx context.Context, fileID string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_ = s.files.Discard(cleanupCtx, fileID)
}

// sniffImageMIME reports the canonical content type of an image we are willing
// to store, derived from its magic bytes. http.DetectContentType never returns
// the `image/jpg` alias that isSupportedMIME accepts from clients, so the
// stored type is always the canonical spelling.
func sniffImageMIME(image []byte) (string, bool) {
	switch detected := http.DetectContentType(image); detected {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return detected, true
	}
	return "", false
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
	return collect(rows)
}

// Search returns live emojis whose name contains term, newest first. The
// match runs in the database rather than over a fetched page so a workspace
// with more custom emojis than one page can hold stays fully searchable;
// filtering a client-side slice silently hid everything but the newest rows.
//
// term is matched literally with strpos, not LIKE, so `%` and `_` inside a
// user-supplied autocomplete term stay ordinary characters. An empty term
// matches everything, which is what the autocomplete endpoint asks for
// before the user has typed anything.
func (s *Service) Search(ctx context.Context, term string, limit int) ([]Emoji, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, name, creator_id, file_id, create_at, delete_at
		FROM emojis
		WHERE delete_at=0 AND ($1 = '' OR strpos(name, $1) > 0)
		ORDER BY create_at DESC
		LIMIT $2
	`, term, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows)
}

// GetManyByNames resolves a batch of names in one round-trip and returns the
// live matches in the caller's requested order. Names that resolve to nothing
// are simply absent, mirroring GetByName's per-name contract without paying a
// query per requested name.
func (s *Service) GetManyByNames(ctx context.Context, names []string) ([]Emoji, error) {
	out := []Emoji{}
	if len(names) == 0 {
		return out, nil
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, name, creator_id, file_id, create_at, delete_at
		FROM emojis WHERE delete_at=0 AND name = ANY($1::text[])
	`, names)
	if err != nil {
		return nil, err
	}
	found, err := collect(rows)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]Emoji, len(found))
	for _, e := range found {
		byName[e.Name] = e
	}
	for _, name := range names {
		if e, ok := byName[name]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}

// collect drains a rows cursor selecting the standard emoji column list. It
// always returns a non-nil slice so handlers marshal `[]` instead of `null`.
func collect(rows pgx.Rows) ([]Emoji, error) {
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
