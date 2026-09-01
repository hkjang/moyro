// Package documents stores channel-scoped Markdown documents derived from a
// conversation. Source scope is always resolved on the server, and every read
// rechecks live team/channel membership instead of trusting persisted display
// metadata.
package documents

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
)

const (
	MaxTitleRunes       = 240
	MaxBodyRunes        = 100_000
	MaxIdentifierRunes  = 128
	MaxIdempotencyRunes = 200
	MaxSourcePosts      = 200
	MaxSourceRunes      = 120_000
	DefaultListLimit    = 50
	MaxListLimit        = 100

	// PostgreSQL transaction advisory locks use a package-independent
	// namespace plus the effective schema and canonical thread id. The posts
	// package deliberately uses the same value and SQL expression so a source
	// mutation and a document snapshot cannot commit across one another.
	sourceThreadLockNamespace int32 = 1297041746 // "MOYR"
)

var (
	ErrInvalid             = errors.New("document input is invalid")
	ErrNotFound            = errors.New("document not found")
	ErrForbidden           = errors.New("document operation forbidden")
	ErrRevisionConflict    = errors.New("document revision conflict")
	ErrSourceChanged       = errors.New("document source changed")
	ErrSourceTooLarge      = errors.New("document source is too large")
	ErrIdempotencyConflict = errors.New("document idempotency conflict")
)

type Document struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	CreatedBy      string `json:"created_by"`
	TeamID         string `json:"team_id,omitempty"`
	ChannelID      string `json:"channel_id"`
	SourceThreadID string `json:"source_thread_id"`
	SourceCursorAt int64  `json:"source_cursor_at"`
	Revision       int64  `json:"revision"`
	CreateAt       int64  `json:"create_at"`
	UpdateAt       int64  `json:"update_at"`
	DeleteAt       int64  `json:"delete_at"`
	Stale          bool   `json:"stale"`
	IdempotencyKey string `json:"-"`
	// CreateFingerprint records the normalized creation request permanently.
	// Patch never changes it, so a response-lost Create retry remains
	// idempotent even after the document title or body has been edited.
	CreateFingerprint string `json:"-"`
}

type SourcePost struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	RootID    string `json:"root_id"`
	Message   string `json:"message"`
	CreateAt  int64  `json:"create_at"`
	UpdateAt  int64  `json:"update_at"`
}

type SourceBundle struct {
	TeamID    string `json:"team_id,omitempty"`
	ChannelID string `json:"channel_id"`
	ThreadID  string `json:"thread_id"`
	// CursorAt is an opaque content revision, not a wall-clock timestamp.
	CursorAt int64        `json:"cursor_at"`
	Posts    []SourcePost `json:"posts"`
}

type CreateInput struct {
	Title          string
	Body           string
	SourcePostID   string
	SourceCursorAt int64
	IdempotencyKey string
}

type PatchInput struct {
	Title            *string
	Body             *string
	SourceCursorAt   *int64
	ExpectedRevision int64
}

type ListOptions struct {
	Limit int
}

type Service struct {
	db    *store.DB
	nowMS func() int64
}

func New(db *store.DB) *Service {
	return &Service{db: db, nowMS: func() int64 { return time.Now().UnixMilli() }}
}

const documentColumns = `
	d.id, d.title, d.body, d.created_by, COALESCE(d.team_id,''), d.channel_id,
	d.source_thread_id, d.source_cursor_at, d.idempotency_key, d.create_fingerprint, d.revision,
	d.create_at, d.update_at, d.delete_at`

const staleExpression = `(
	NOT EXISTS (
		SELECT 1 FROM posts root
		WHERE root.id=d.source_thread_id AND COALESCE(root.root_id,'')=''
		  AND root.channel_id=d.channel_id AND root.delete_at=0
	)
	OR EXISTS (
		SELECT 1
		FROM document_sources captured
		LEFT JOIN posts current_source ON current_source.id=captured.post_id
		WHERE captured.document_id=d.id
		  AND (
			current_source.id IS NULL OR current_source.delete_at<>0
			OR current_source.channel_id<>d.channel_id
			OR NOT (
				(current_source.id=d.source_thread_id AND COALESCE(current_source.root_id,'')='')
				OR current_source.root_id=d.source_thread_id
			)
			OR md5(current_source.message)<>captured.captured_content_digest
		  )
	)
	OR (
		SELECT COUNT(*) FROM posts current_source
		WHERE (current_source.id=d.source_thread_id OR current_source.root_id=d.source_thread_id)
		  AND current_source.channel_id=d.channel_id AND current_source.delete_at=0
	) <> (
		SELECT COUNT(*) FROM document_sources captured
		WHERE captured.document_id=d.id
	)
)`

type scanner interface {
	Scan(...any) error
}

type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func scanDocument(row scanner, stale *bool) (*Document, error) {
	var document Document
	destinations := []any{
		&document.ID, &document.Title, &document.Body, &document.CreatedBy,
		&document.TeamID, &document.ChannelID, &document.SourceThreadID,
		&document.SourceCursorAt, &document.IdempotencyKey, &document.CreateFingerprint, &document.Revision,
		&document.CreateAt, &document.UpdateAt, &document.DeleteAt,
	}
	if stale != nil {
		destinations = append(destinations, stale)
	}
	if err := row.Scan(destinations...); err != nil {
		return nil, err
	}
	if stale != nil {
		document.Stale = *stale
	}
	return &document, nil
}

func normalizeIdentifier(value string, max int, required bool) (string, error) {
	value = strings.TrimSpace(value)
	length := utf8.RuneCountInString(value)
	if (required && length == 0) || length > max || !validText(value, false) {
		return "", ErrInvalid
	}
	return value, nil
}

func normalizeRequiredText(value string, max int, multiline bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > max || !validText(value, multiline) {
		return "", ErrInvalid
	}
	return value, nil
}

func validText(value string, multiline bool) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if !unicode.IsControl(char) {
			continue
		}
		if multiline && (char == '\n' || char == '\r' || char == '\t') {
			continue
		}
		return false
	}
	return true
}

func validatePrincipal(principal rbac.Principal) (rbac.Principal, error) {
	userID, err := normalizeIdentifier(principal.UserID, MaxIdentifierRunes, true)
	if err != nil {
		return rbac.Principal{}, err
	}
	principal.UserID = userID
	return principal, nil
}

// Source resolves the complete live thread containing postID. It is the only
// supported source-input path, so clients cannot inject messages from another
// channel into a document or AI prompt.
func (s *Service) Source(ctx context.Context, principal rbac.Principal, postID string) (*SourceBundle, error) {
	principal, err := validatePrincipal(principal)
	if err != nil {
		return nil, err
	}
	postID, err = normalizeIdentifier(postID, MaxIdentifierRunes, true)
	if err != nil {
		return nil, err
	}
	return resolveSource(ctx, s.db.Pool, principal, postID)
}

func resolveSource(ctx context.Context, query querier, principal rbac.Principal, postID string) (*SourceBundle, error) {
	var rootID, channelID, teamID string
	err := query.QueryRow(ctx, `
		SELECT CASE WHEN COALESCE(p.root_id,'')='' THEN p.id ELSE p.root_id END,
		       p.channel_id, COALESCE(c.team_id,'')
		FROM posts p
		JOIN channels c ON c.id=p.channel_id AND c.delete_at=0
		JOIN users viewer ON viewer.id=$1 AND viewer.delete_at=0
		JOIN channel_members cm ON cm.channel_id=c.id AND cm.user_id=viewer.id
		LEFT JOIN teams t ON t.id=c.team_id AND t.delete_at=0
		LEFT JOIN team_members tm ON tm.team_id=c.team_id AND tm.user_id=viewer.id
		WHERE p.id=$2 AND p.delete_at=0
		  AND (c.team_id IS NULL OR (t.id IS NOT NULL AND tm.user_id IS NOT NULL))
	`, principal.UserID, postID).Scan(&rootID, &channelID, &teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !rbac.ResourceConstraintsFor(principal).Allows(teamID, channelID) {
		return nil, ErrNotFound
	}

	rows, err := query.Query(ctx, `
		SELECT p.id, p.channel_id, p.user_id, COALESCE(u.username,''),
		       COALESCE(p.root_id,''), p.message, p.create_at, p.update_at
		FROM posts p
		LEFT JOIN users u ON u.id=p.user_id
		WHERE (p.id=$1 OR p.root_id=$1) AND p.channel_id=$2 AND p.delete_at=0
		ORDER BY CASE WHEN p.id=$1 THEN 0 ELSE 1 END, p.create_at, p.id
		LIMIT $3
	`, rootID, channelID, MaxSourcePosts+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bundle := &SourceBundle{TeamID: teamID, ChannelID: channelID, ThreadID: rootID, Posts: []SourcePost{}}
	totalRunes := 0
	for rows.Next() {
		var source SourcePost
		if err := rows.Scan(
			&source.ID, &source.ChannelID, &source.UserID, &source.Username,
			&source.RootID, &source.Message, &source.CreateAt, &source.UpdateAt,
		); err != nil {
			return nil, err
		}
		bundle.Posts = append(bundle.Posts, source)
		totalRunes += utf8.RuneCountInString(source.Message)
		if len(bundle.Posts) > MaxSourcePosts || totalRunes > MaxSourceRunes {
			return nil, ErrSourceTooLarge
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(bundle.Posts) == 0 || bundle.Posts[0].ID != rootID {
		return nil, ErrNotFound
	}
	bundle.CursorAt = sourceRevision(bundle.Posts)
	return bundle, nil
}

// sourceRevision is an opaque compare-and-swap token. Length-prefixing every
// value avoids concatenation ambiguity. Message content detects edits (even
// within one millisecond), while identifiers and creation timestamps detect
// additions, removals, and ordering changes. update_at is intentionally not
// included: pin, attachment, link-preview, and props-only changes are post
// metadata and must not make a conversation-derived document stale.
// The high bit is cleared because PostgreSQL BIGINT is signed.
func sourceRevision(posts []SourcePost) int64 {
	hash := sha256.New()
	var buffer [8]byte
	writeString := func(value string) {
		binary.BigEndian.PutUint64(buffer[:], uint64(len(value)))
		_, _ = hash.Write(buffer[:])
		_, _ = hash.Write([]byte(value))
	}
	for _, post := range posts {
		writeString(post.ID)
		writeString(post.ChannelID)
		writeString(post.RootID)
		binary.BigEndian.PutUint64(buffer[:], uint64(post.CreateAt))
		_, _ = hash.Write(buffer[:])
		writeString(post.Message)
	}
	revision := binary.BigEndian.Uint64(hash.Sum(nil)[:8]) & uint64(^uint64(0)>>1)
	if revision == 0 {
		revision = 1
	}
	return int64(revision)
}

func lockSourceThread(ctx context.Context, tx pgx.Tx, threadID string) error {
	_, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(
			$1::integer,
			hashtext(COALESCE(current_schema(),'') || ':' || $2::text)
		)
	`, sourceThreadLockNamespace, threadID)
	return err
}

// resolveSourceForWrite first performs the normal authorization-aware lookup
// to discover the canonical root, then acquires its transaction lock and
// resolves the complete thread again. The second read is essential: a reply,
// edit, delete, restore, or move may have committed while this transaction was
// waiting for the lock.
func resolveSourceForWrite(ctx context.Context, tx pgx.Tx, principal rbac.Principal, postID string) (*SourceBundle, error) {
	preflight, err := resolveSource(ctx, tx, principal, postID)
	if err != nil {
		return nil, err
	}
	if err := lockSourceThread(ctx, tx, preflight.ThreadID); err != nil {
		return nil, err
	}
	return resolveSource(ctx, tx, principal, postID)
}

func createFingerprint(document *Document) string {
	if document == nil {
		return ""
	}
	hash := sha256.New()
	var buffer [8]byte
	writeString := func(value string) {
		binary.BigEndian.PutUint64(buffer[:], uint64(len(value)))
		_, _ = hash.Write(buffer[:])
		_, _ = hash.Write([]byte(value))
	}
	writeString(document.Title)
	writeString(document.Body)
	writeString(document.CreatedBy)
	writeString(document.TeamID)
	writeString(document.ChannelID)
	writeString(document.SourceThreadID)
	binary.BigEndian.PutUint64(buffer[:], uint64(document.SourceCursorAt))
	_, _ = hash.Write(buffer[:])
	return hex.EncodeToString(hash.Sum(nil))
}

func (s *Service) Create(ctx context.Context, principal rbac.Principal, input CreateInput) (*Document, bool, error) {
	principal, err := validatePrincipal(principal)
	if err != nil {
		return nil, false, err
	}
	if input.Title, err = normalizeRequiredText(input.Title, MaxTitleRunes, false); err != nil {
		return nil, false, err
	}
	if input.Body, err = normalizeRequiredText(input.Body, MaxBodyRunes, true); err != nil {
		return nil, false, err
	}
	if input.SourcePostID, err = normalizeIdentifier(input.SourcePostID, MaxIdentifierRunes, true); err != nil {
		return nil, false, err
	}
	if input.IdempotencyKey, err = normalizeIdentifier(input.IdempotencyKey, MaxIdempotencyRunes, true); err != nil || input.SourceCursorAt <= 0 {
		return nil, false, ErrInvalid
	}

	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	bundle, err := resolveSourceForWrite(ctx, tx, principal, input.SourcePostID)
	if err != nil {
		return nil, false, err
	}
	// A response-lost retry must return the originally committed document even
	// when the source thread changed afterward. Scope is resolved first so a
	// revoked member cannot replay or discover the document through its key.
	requested := &Document{
		Title: input.Title, Body: input.Body, CreatedBy: principal.UserID,
		TeamID: bundle.TeamID, ChannelID: bundle.ChannelID, SourceThreadID: bundle.ThreadID,
		SourceCursorAt: input.SourceCursorAt, IdempotencyKey: input.IdempotencyKey,
	}
	requested.CreateFingerprint = createFingerprint(requested)
	existing, lookupErr := scanDocument(tx.QueryRow(ctx, `
		SELECT `+documentColumns+` FROM documents d
		WHERE d.created_by=$1 AND d.idempotency_key=$2
	`, principal.UserID, input.IdempotencyKey), nil)
	if lookupErr == nil {
		if existing.DeleteAt != 0 || !sameCreate(existing, requested) {
			return nil, false, ErrIdempotencyConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		refreshed, err := s.Get(ctx, principal, existing.ID)
		return refreshed, true, err
	}
	if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return nil, false, lookupErr
	}
	if bundle.CursorAt != input.SourceCursorAt {
		return nil, false, ErrSourceChanged
	}

	now := s.nowMS()
	document := &Document{
		ID: uuid.NewString(), Title: input.Title, Body: input.Body,
		CreatedBy: principal.UserID, TeamID: bundle.TeamID, ChannelID: bundle.ChannelID,
		SourceThreadID: bundle.ThreadID, SourceCursorAt: bundle.CursorAt,
		IdempotencyKey: input.IdempotencyKey, Revision: 1, CreateAt: now, UpdateAt: now,
	}
	document.CreateFingerprint = createFingerprint(document)
	tag, err := tx.Exec(ctx, `
		INSERT INTO documents (
			id, title, body, created_by, team_id, channel_id, source_thread_id,
			source_cursor_at, idempotency_key, create_fingerprint, revision, create_at, update_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,1,$11,$11)
		ON CONFLICT (created_by, idempotency_key) DO NOTHING
	`, document.ID, document.Title, document.Body, document.CreatedBy,
		nullableString(document.TeamID), document.ChannelID, document.SourceThreadID,
		document.SourceCursorAt, document.IdempotencyKey, document.CreateFingerprint, now)
	if err != nil {
		return nil, false, err
	}
	if tag.RowsAffected() == 0 {
		existing, lookupErr = scanDocument(tx.QueryRow(ctx, `
			SELECT `+documentColumns+` FROM documents d
			WHERE d.created_by=$1 AND d.idempotency_key=$2
		`, principal.UserID, input.IdempotencyKey), nil)
		if lookupErr != nil {
			return nil, false, lookupErr
		}
		if existing.DeleteAt != 0 || !sameCreate(existing, document) {
			return nil, false, ErrIdempotencyConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		refreshed, err := s.Get(ctx, principal, existing.ID)
		return refreshed, true, err
	}
	if err := replaceSources(ctx, tx, document.ID, bundle); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	refreshed, err := s.Get(ctx, principal, document.ID)
	return refreshed, false, err
}

func sameCreate(existing, requested *Document) bool {
	return existing != nil && requested != nil && existing.CreateFingerprint != "" &&
		requested.CreateFingerprint != "" && existing.CreateFingerprint == requested.CreateFingerprint
}

func replaceSources(ctx context.Context, tx pgx.Tx, documentID string, bundle *SourceBundle) error {
	if _, err := tx.Exec(ctx, `DELETE FROM document_sources WHERE document_id=$1`, documentID); err != nil {
		return err
	}
	for position, source := range bundle.Posts {
		if _, err := tx.Exec(ctx, `
			INSERT INTO document_sources (
				document_id, post_id, position, captured_update_at, captured_content_digest
			) VALUES ($1,$2,$3,$4,md5($5))
		`, documentID, source.ID, position, max(source.CreateAt, source.UpdateAt), source.Message); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) List(ctx context.Context, principal rbac.Principal, options ListOptions) ([]Document, error) {
	principal, err := validatePrincipal(principal)
	if err != nil {
		return nil, err
	}
	limit := options.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		return nil, ErrInvalid
	}
	constraints := rbac.ResourceConstraintsFor(principal)
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+documentColumns+`, `+staleExpression+`
		FROM documents d
		JOIN users viewer ON viewer.id=$1 AND viewer.delete_at=0
		JOIN channels c ON c.id=d.channel_id AND c.delete_at=0
		JOIN channel_members cm ON cm.channel_id=c.id AND cm.user_id=viewer.id
		LEFT JOIN teams t ON t.id=c.team_id AND t.delete_at=0
		LEFT JOIN team_members tm ON tm.team_id=c.team_id AND tm.user_id=viewer.id
		WHERE d.delete_at=0
		  AND COALESCE(d.team_id,'')=COALESCE(c.team_id,'')
		  AND (c.team_id IS NULL OR (t.id IS NOT NULL AND tm.user_id IS NOT NULL))
		  AND (NOT $2::boolean OR cardinality($3::text[])=0 OR COALESCE(d.team_id,'')=ANY($3::text[]))
		  AND (NOT $2::boolean OR cardinality($4::text[])=0 OR d.channel_id=ANY($4::text[]))
		ORDER BY d.update_at DESC, d.id DESC
		LIMIT $5
	`, principal.UserID, constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	documents := []Document{}
	for rows.Next() {
		var stale bool
		document, err := scanDocument(rows, &stale)
		if err != nil {
			return nil, err
		}
		documents = append(documents, *document)
	}
	return documents, rows.Err()
}

func (s *Service) Get(ctx context.Context, principal rbac.Principal, documentID string) (*Document, error) {
	principal, err := validatePrincipal(principal)
	if err != nil {
		return nil, err
	}
	documentID, err = normalizeIdentifier(documentID, MaxIdentifierRunes, true)
	if err != nil {
		return nil, err
	}
	constraints := rbac.ResourceConstraintsFor(principal)
	var stale bool
	document, err := scanDocument(s.db.Pool.QueryRow(ctx, `
		SELECT `+documentColumns+`, `+staleExpression+`
		FROM documents d
		JOIN users viewer ON viewer.id=$1 AND viewer.delete_at=0
		JOIN channels c ON c.id=d.channel_id AND c.delete_at=0
		JOIN channel_members cm ON cm.channel_id=c.id AND cm.user_id=viewer.id
		LEFT JOIN teams t ON t.id=c.team_id AND t.delete_at=0
		LEFT JOIN team_members tm ON tm.team_id=c.team_id AND tm.user_id=viewer.id
		WHERE d.id=$2 AND d.delete_at=0
		  AND COALESCE(d.team_id,'')=COALESCE(c.team_id,'')
		  AND (c.team_id IS NULL OR (t.id IS NOT NULL AND tm.user_id IS NOT NULL))
		  AND (NOT $3::boolean OR cardinality($4::text[])=0 OR COALESCE(d.team_id,'')=ANY($4::text[]))
		  AND (NOT $3::boolean OR cardinality($5::text[])=0 OR d.channel_id=ANY($5::text[]))
	`, principal.UserID, documentID, constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs), &stale)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return document, err
}

func (s *Service) Patch(ctx context.Context, principal rbac.Principal, documentID string, input PatchInput) (*Document, error) {
	principal, err := validatePrincipal(principal)
	if err != nil {
		return nil, err
	}
	documentID, err = normalizeIdentifier(documentID, MaxIdentifierRunes, true)
	if err != nil || input.ExpectedRevision <= 0 || (input.Title == nil && input.Body == nil && input.SourceCursorAt == nil) {
		return nil, ErrInvalid
	}
	if input.SourceCursorAt != nil && (input.Body == nil || *input.SourceCursorAt <= 0) {
		return nil, ErrInvalid
	}

	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	existing, err := s.lockAccessible(ctx, tx, principal, documentID)
	if err != nil {
		return nil, err
	}
	if existing.CreatedBy != principal.UserID {
		return nil, ErrForbidden
	}
	if existing.Revision != input.ExpectedRevision {
		return nil, ErrRevisionConflict
	}
	// Even a title/body-only patch is ordered with source mutations. This
	// gives every successful document revision a single linearization point
	// relative to edits in the conversation it represents.
	if err := lockSourceThread(ctx, tx, existing.SourceThreadID); err != nil {
		return nil, err
	}
	title, body := existing.Title, existing.Body
	if input.Title != nil {
		title, err = normalizeRequiredText(*input.Title, MaxTitleRunes, false)
		if err != nil {
			return nil, err
		}
	}
	if input.Body != nil {
		body, err = normalizeRequiredText(*input.Body, MaxBodyRunes, true)
		if err != nil {
			return nil, err
		}
	}
	cursorAt := existing.SourceCursorAt
	var bundle *SourceBundle
	if input.SourceCursorAt != nil {
		// The canonical thread lock is already held, so this authorization and
		// content read remains stable until replaceSources and commit finish.
		bundle, err = resolveSource(ctx, tx, principal, existing.SourceThreadID)
		if err != nil {
			return nil, err
		}
		if bundle.TeamID != existing.TeamID || bundle.ChannelID != existing.ChannelID || bundle.ThreadID != existing.SourceThreadID {
			return nil, ErrSourceChanged
		}
		if bundle.CursorAt != *input.SourceCursorAt {
			return nil, ErrSourceChanged
		}
		cursorAt = bundle.CursorAt
	}
	now := s.nowMS()
	tag, err := tx.Exec(ctx, `
		UPDATE documents
		SET title=$1, body=$2, source_cursor_at=$3, revision=revision+1, update_at=$4
		WHERE id=$5 AND revision=$6 AND delete_at=0
	`, title, body, cursorAt, now, documentID, input.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, ErrRevisionConflict
	}
	if bundle != nil {
		if err := replaceSources(ctx, tx, documentID, bundle); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Get(ctx, principal, documentID)
}

func (s *Service) Delete(ctx context.Context, principal rbac.Principal, documentID string, expectedRevision int64) (*Document, error) {
	principal, err := validatePrincipal(principal)
	if err != nil {
		return nil, err
	}
	documentID, err = normalizeIdentifier(documentID, MaxIdentifierRunes, true)
	if err != nil || expectedRevision <= 0 {
		return nil, ErrInvalid
	}
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	existing, err := s.lockAccessible(ctx, tx, principal, documentID)
	if err != nil {
		return nil, err
	}
	if existing.CreatedBy != principal.UserID {
		return nil, ErrForbidden
	}
	if existing.Revision != expectedRevision {
		return nil, ErrRevisionConflict
	}
	now := s.nowMS()
	tag, err := tx.Exec(ctx, `
		UPDATE documents
		SET delete_at=$1, update_at=$1, revision=revision+1
		WHERE id=$2 AND revision=$3 AND delete_at=0
	`, now, documentID, expectedRevision)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, ErrRevisionConflict
	}
	existing.DeleteAt, existing.UpdateAt, existing.Revision = now, now, existing.Revision+1
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return existing, nil
}

func (s *Service) lockAccessible(ctx context.Context, tx pgx.Tx, principal rbac.Principal, documentID string) (*Document, error) {
	constraints := rbac.ResourceConstraintsFor(principal)
	document, err := scanDocument(tx.QueryRow(ctx, `
		SELECT `+documentColumns+`
		FROM documents d
		JOIN users viewer ON viewer.id=$1 AND viewer.delete_at=0
		JOIN channels c ON c.id=d.channel_id AND c.delete_at=0
		JOIN channel_members cm ON cm.channel_id=c.id AND cm.user_id=viewer.id
		LEFT JOIN teams t ON t.id=c.team_id AND t.delete_at=0
		LEFT JOIN team_members tm ON tm.team_id=c.team_id AND tm.user_id=viewer.id
		WHERE d.id=$2 AND d.delete_at=0
		  AND COALESCE(d.team_id,'')=COALESCE(c.team_id,'')
		  AND (c.team_id IS NULL OR (t.id IS NOT NULL AND tm.user_id IS NOT NULL))
		  AND (NOT $3::boolean OR cardinality($4::text[])=0 OR COALESCE(d.team_id,'')=ANY($4::text[]))
		  AND (NOT $3::boolean OR cardinality($5::text[])=0 OR d.channel_id=ANY($5::text[]))
		FOR UPDATE OF d
	`, principal.UserID, documentID, constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs), nil)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return document, err
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
