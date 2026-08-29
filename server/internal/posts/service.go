package posts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
)

type Post struct {
	ID        string         `json:"id"`
	ChannelID string         `json:"channel_id"`
	UserID    string         `json:"user_id"`
	RootID    string         `json:"root_id"`
	Message   string         `json:"message"`
	Props     map[string]any `json:"props"`
	FileIDs   []string       `json:"file_ids"`
	IsPinned  bool           `json:"is_pinned"`
	CreateAt  int64          `json:"create_at"`
	UpdateAt  int64          `json:"update_at"`
	DeleteAt  int64          `json:"delete_at"`
	// Phase 18: server-generated OpenGraph unfurl cards. Empty by default;
	// populated asynchronously after creation and re-broadcast via post_edited.
	LinkMetadata []LinkPreview `json:"link_metadata"`
}

// LinkPreview is one OpenGraph card attached to a post. Fields other than
// URL are best-effort — a page with no og:title or og:description still
// produces a valid preview (the client renders what it has). Image proxying
// happens client-side via /api/v4/link_preview_image?url=... so we don't
// embed raw third-party URLs in DOM <img> tags.
type LinkPreview struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	ImageURL    string `json:"image_url"`
	FetchedAt   int64  `json:"fetched_at"`
}

type PostList struct {
	Order []string         `json:"order"`
	Posts map[string]*Post `json:"posts"`
}

// SearchFilters narrows a full-text search. Zero values mean "no filter".
// Multiple filters AND together. `HasLink` uses a cheap regex against the
// message body (same regex the link-extractor uses) so it's consistent with
// the preview pipeline.
type SearchFilters struct {
	FromUserID  string
	InChannelID string
	After       int64 // ms epoch, inclusive
	Before      int64 // ms epoch, exclusive (so callers can pass the *next* day's midnight)
	HasFile     bool
	HasLink     bool
}

// SearchResult is the ranked-search envelope. TotalHits is the unpaginated
// row count so the webapp can render "N results" + pagination controls.
type SearchResult struct {
	Order     []string         `json:"order"`
	Posts     map[string]*Post `json:"posts"`
	TotalHits int              `json:"total_hits"`
	Page      int              `json:"page"`
}

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

var ErrInvalidRoot = errors.New("post root must be a live root post in the same channel")

// allPostColumns lists every column we hydrate into a Post — kept in one
// place so adding a column only touches this constant and scanRow.
const allPostColumns = `id, channel_id, user_id, root_id, message, props, file_ids, is_pinned, create_at, update_at, delete_at, link_metadata`

// scanRow hydrates a Post from a row with the columns in allPostColumns
// order. Works with both pgx.Row and pgx.Rows thanks to the shared Scan
// interface.
type scannable interface {
	Scan(dest ...any) error
}

func scanPost(row scannable) (*Post, error) {
	var p Post
	var propsRaw, fileIDsRaw, linkRaw []byte
	if err := row.Scan(&p.ID, &p.ChannelID, &p.UserID, &p.RootID, &p.Message, &propsRaw, &fileIDsRaw, &p.IsPinned, &p.CreateAt, &p.UpdateAt, &p.DeleteAt, &linkRaw); err != nil {
		return nil, err
	}
	if len(propsRaw) > 0 {
		_ = json.Unmarshal(propsRaw, &p.Props)
	}
	if len(fileIDsRaw) > 0 {
		_ = json.Unmarshal(fileIDsRaw, &p.FileIDs)
	}
	if p.FileIDs == nil {
		p.FileIDs = []string{}
	}
	if len(linkRaw) > 0 {
		_ = json.Unmarshal(linkRaw, &p.LinkMetadata)
	}
	if p.LinkMetadata == nil {
		p.LinkMetadata = []LinkPreview{}
	}
	return &p, nil
}

func (s *Service) Create(ctx context.Context, channelID, userID, rootID, message string, props map[string]any, fileIDs []string) (*Post, error) {
	now := time.Now().UnixMilli()
	if props == nil {
		props = map[string]any{}
	}
	if fileIDs == nil {
		fileIDs = []string{}
	}
	rawProps, _ := json.Marshal(props)
	rawFileIDs, _ := json.Marshal(fileIDs)
	p := &Post{
		ID:           uuid.NewString(),
		ChannelID:    channelID,
		UserID:       userID,
		RootID:       rootID,
		Message:      message,
		Props:        props,
		FileIDs:      fileIDs,
		CreateAt:     now,
		UpdateAt:     now,
		LinkMetadata: []LinkPreview{},
	}
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Validate replies at the persistence boundary so every caller (REST,
	// scheduled posts, MCP, and integrations) gets the same channel-isolation
	// guarantee. FOR SHARE keeps the validated root stable until the insert
	// commits and prevents a concurrent thread move from crossing channels.
	if rootID != "" {
		var one int
		err = tx.QueryRow(ctx, `
			SELECT 1 FROM posts
			WHERE id=$1 AND channel_id=$2 AND delete_at=0
			  AND COALESCE(root_id, '')=''
			FOR SHARE
		`, rootID, channelID).Scan(&one)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalidRoot
		}
		if err != nil {
			return nil, err
		}
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO posts (id, channel_id, user_id, root_id, message, props, file_ids, is_pinned, create_at, update_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,FALSE,$8,$8)
	`, p.ID, p.ChannelID, p.UserID, p.RootID, p.Message, rawProps, rawFileIDs, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

// GetByApprovalRequest returns the unique post produced by one approved
// workflow request. A partial unique index on props->>'approval_request_id'
// turns retries across network failures into a read of the original result.
func (s *Service) GetByApprovalRequest(ctx context.Context, requestID string) (*Post, error) {
	return scanPost(s.db.Pool.QueryRow(ctx, `
		SELECT `+allPostColumns+` FROM posts
		WHERE props->>'approval_request_id'=$1 AND delete_at=0
	`, requestID))
}

// UpdateFileIDs overwrites a post's file_ids after files are associated.
func (s *Service) UpdateFileIDs(ctx context.Context, postID string, fileIDs []string) error {
	if fileIDs == nil {
		fileIDs = []string{}
	}
	raw, _ := json.Marshal(fileIDs)
	_, err := s.db.Pool.Exec(ctx, `UPDATE posts SET file_ids=$1, update_at=$2 WHERE id=$3`, raw, time.Now().UnixMilli(), postID)
	return err
}

// UpdateLinkMetadata overwrites the link_metadata JSONB column. Does NOT
// bump update_at — link previews are metadata we attached, not a real edit;
// we don't want them showing up as "edited" in the UI.
func (s *Service) UpdateLinkMetadata(ctx context.Context, postID string, previews []LinkPreview) error {
	if previews == nil {
		previews = []LinkPreview{}
	}
	raw, _ := json.Marshal(previews)
	_, err := s.db.Pool.Exec(ctx, `UPDATE posts SET link_metadata=$1 WHERE id=$2`, raw, postID)
	return err
}

func (s *Service) ListForChannel(ctx context.Context, channelID string, page, perPage int) (*PostList, error) {
	if perPage <= 0 || perPage > 200 {
		perPage = 60
	}
	if page < 0 {
		page = 0
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+allPostColumns+`
		FROM posts WHERE channel_id=$1 AND delete_at=0
		ORDER BY create_at DESC
		LIMIT $2 OFFSET $3
	`, channelID, perPage, page*perPage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := &PostList{Order: []string{}, Posts: map[string]*Post{}}
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		list.Order = append(list.Order, p.ID)
		list.Posts[p.ID] = p
	}
	return list, rows.Err()
}

// PageOpts is the union of the four cursor modes Mattermost's
// `GET /channels/{id}/posts` accepts: `since`, `before`, `after`, or plain
// page/per_page. All fields are optional; zero values mean "ignore".
//
//	Since>0  → posts with create_at >= since (ordered ascending), capped at PerPage
//	Before!= → posts strictly older than the post with id=Before (descending)
//	After!=  → posts strictly newer than the post with id=After (ascending)
//	otherwise → standard offset paging via Page+PerPage
//
// before/after take a post id (not a timestamp) for parity with the official
// API — clients pass the boundary post they already have.
type PageOpts struct {
	Since   int64
	Before  string
	After   string
	Page    int
	PerPage int
}

// ListForChannelPaged is the cursor-aware variant of ListForChannel. It
// supports Mattermost's full set of post-paging knobs. Returns ascending
// or descending order depending on the cursor — Mattermost clients flip
// rendering based on `order` so they don't reverse the array themselves.
func (s *Service) ListForChannelPaged(ctx context.Context, channelID string, opts PageOpts) (*PostList, error) {
	perPage := opts.PerPage
	if perPage <= 0 || perPage > 200 {
		perPage = 60
	}
	list := &PostList{Order: []string{}, Posts: map[string]*Post{}}

	switch {
	case opts.Since > 0:
		// `since` is a unix-ms epoch. Mattermost returns posts at-or-newer
		// than this timestamp, ascending so the client can append-only.
		rows, err := s.db.Pool.Query(ctx, `
			SELECT `+allPostColumns+`
			FROM posts WHERE channel_id=$1 AND delete_at=0 AND create_at >= $2
			ORDER BY create_at ASC
			LIMIT $3
		`, channelID, opts.Since, perPage)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanPost(rows)
			if err != nil {
				return nil, err
			}
			list.Order = append(list.Order, p.ID)
			list.Posts[p.ID] = p
		}
		return list, rows.Err()

	case opts.Before != "":
		// "give me the page just *before* this post id". Resolve the
		// boundary post's create_at first, then page descending.
		var anchor int64
		if err := s.db.Pool.QueryRow(ctx, `SELECT create_at FROM posts WHERE id=$1`, opts.Before).Scan(&anchor); err != nil {
			return list, nil // missing anchor → empty page, not 500
		}
		rows, err := s.db.Pool.Query(ctx, `
			SELECT `+allPostColumns+`
			FROM posts WHERE channel_id=$1 AND delete_at=0 AND create_at < $2
			ORDER BY create_at DESC
			LIMIT $3
		`, channelID, anchor, perPage)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanPost(rows)
			if err != nil {
				return nil, err
			}
			list.Order = append(list.Order, p.ID)
			list.Posts[p.ID] = p
		}
		return list, rows.Err()

	case opts.After != "":
		var anchor int64
		if err := s.db.Pool.QueryRow(ctx, `SELECT create_at FROM posts WHERE id=$1`, opts.After).Scan(&anchor); err != nil {
			return list, nil
		}
		rows, err := s.db.Pool.Query(ctx, `
			SELECT `+allPostColumns+`
			FROM posts WHERE channel_id=$1 AND delete_at=0 AND create_at > $2
			ORDER BY create_at ASC
			LIMIT $3
		`, channelID, anchor, perPage)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanPost(rows)
			if err != nil {
				return nil, err
			}
			list.Order = append(list.Order, p.ID)
			list.Posts[p.ID] = p
		}
		return list, rows.Err()

	default:
		page := opts.Page
		if page < 0 {
			page = 0
		}
		return s.ListForChannel(ctx, channelID, page, perPage)
	}
}

func (s *Service) Delete(ctx context.Context, postID, userID string) (bool, error) {
	now := time.Now().UnixMilli()
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE posts SET delete_at=$1, update_at=$1
		WHERE id=$2 AND user_id=$3 AND delete_at=0
	`, now, postID, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Search runs a ranked full-text search across posts in channels the caller
// is a member of within a team. Uses ts_rank_cd for relevance + create_at
// as a tiebreaker so newer posts float among equally-ranked hits. Returns
// a SearchResult envelope with total hit count for pagination UIs.
func (s *Service) Search(ctx context.Context, userID, teamID, terms string, filters SearchFilters, page, perPage int) (*SearchResult, error) {
	if perPage <= 0 || perPage > 100 {
		perPage = 20
	}
	if page < 0 {
		page = 0
	}
	// Build WHERE + args dynamically. $1 is always the query; $2 is userID;
	// $3 is teamID. Filter args follow starting at $4. plainto_tsquery handles
	// operator-free input safely — users can type anything and we won't blow
	// up on a stray `&` or `!`.
	args := []any{terms, userID, teamID}
	where := []string{
		"p.delete_at = 0",
		"p.tsv @@ plainto_tsquery('simple', $1)",
		"m.user_id = $2",
		"c.team_id = $3",
	}
	next := 4
	if filters.FromUserID != "" {
		args = append(args, filters.FromUserID)
		where = append(where, fmt.Sprintf("p.user_id = $%d", next))
		next++
	}
	if filters.InChannelID != "" {
		args = append(args, filters.InChannelID)
		where = append(where, fmt.Sprintf("p.channel_id = $%d", next))
		next++
	}
	if filters.After > 0 {
		args = append(args, filters.After)
		where = append(where, fmt.Sprintf("p.create_at >= $%d", next))
		next++
	}
	if filters.Before > 0 {
		args = append(args, filters.Before)
		where = append(where, fmt.Sprintf("p.create_at < $%d", next))
		next++
	}
	if filters.HasFile {
		// file_ids is a JSONB array; jsonb_array_length is NULL-safe only
		// for actual arrays, so we coerce via COALESCE to an empty array.
		where = append(where, "jsonb_array_length(COALESCE(p.file_ids, '[]'::jsonb)) > 0")
	}
	if filters.HasLink {
		where = append(where, "p.message ~ 'https?://'")
	}

	whereSQL := strings.Join(where, " AND ")

	// Count query — same joins, no ranking/paging.
	var total int
	countSQL := `
		SELECT COUNT(*) FROM posts p
		JOIN channels c        ON c.id = p.channel_id
		JOIN channel_members m ON m.channel_id = p.channel_id AND m.user_id = $2
		WHERE ` + whereSQL
	if err := s.db.Pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, err
	}

	// Data query — same joins + ranking + paging. Two more args: limit + offset.
	args = append(args, perPage, page*perPage)
	dataSQL := `
		SELECT ` + prefixedCols("p.") + `
		FROM posts p
		JOIN channels c        ON c.id = p.channel_id
		JOIN channel_members m ON m.channel_id = p.channel_id AND m.user_id = $2
		WHERE ` + whereSQL + `
		ORDER BY ts_rank_cd(p.tsv, plainto_tsquery('simple', $1)) DESC, p.create_at DESC
		LIMIT $` + fmt.Sprint(next) + ` OFFSET $` + fmt.Sprint(next+1)

	rows, err := s.db.Pool.Query(ctx, dataSQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := &SearchResult{Order: []string{}, Posts: map[string]*Post{}, TotalHits: total, Page: page}
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		result.Order = append(result.Order, p.ID)
		result.Posts[p.ID] = p
	}
	return result, rows.Err()
}

// prefixedCols returns allPostColumns with each bare identifier prefixed by
// the given alias (e.g. "p." → "p.id, p.channel_id, ..."). Keeps the one-
// place-for-columns invariant intact even for JOIN queries.
func prefixedCols(prefix string) string {
	parts := strings.Split(allPostColumns, ", ")
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = prefix + p
	}
	return strings.Join(out, ", ")
}

// Get fetches a single post by id regardless of author. Returns nil,nil if missing.
func (s *Service) Get(ctx context.Context, postID string) (*Post, error) {
	row := s.db.Pool.QueryRow(ctx, `SELECT `+allPostColumns+` FROM posts WHERE id=$1`, postID)
	return scanPost(row)
}

// SetPinned toggles is_pinned and returns the refreshed post. Returns
// nil if no row matched (missing / deleted).
func (s *Service) SetPinned(ctx context.Context, postID string, pinned bool) (*Post, error) {
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE posts SET is_pinned=$1, update_at=$2 WHERE id=$3 AND delete_at=0
	`, pinned, time.Now().UnixMilli(), postID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, nil
	}
	return s.Get(ctx, postID)
}

// ListPinned returns currently-pinned posts in a channel, newest first.
func (s *Service) ListPinned(ctx context.Context, channelID string) (*PostList, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+allPostColumns+`
		FROM posts WHERE channel_id=$1 AND is_pinned=TRUE AND delete_at=0
		ORDER BY create_at DESC
	`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := &PostList{Order: []string{}, Posts: map[string]*Post{}}
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		list.Order = append(list.Order, p.ID)
		list.Posts[p.ID] = p
	}
	return list, rows.Err()
}

// ListThread returns the root post plus every live reply whose root_id
// matches, ordered oldest-first. The root itself is included so a client
// that opens the thread sidebar can render it without a separate fetch.
func (s *Service) ListThread(ctx context.Context, rootID string) (*PostList, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+allPostColumns+`
		FROM posts
		WHERE (id=$1 OR root_id=$1) AND delete_at=0
		ORDER BY create_at ASC
	`, rootID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := &PostList{Order: []string{}, Posts: map[string]*Post{}}
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		list.Order = append(list.Order, p.ID)
		list.Posts[p.ID] = p
	}
	return list, rows.Err()
}

// Update rewrites message/props for a post the caller owns. Returns the
// updated post or nil if no row matched (wrong owner / deleted / missing).
func (s *Service) Update(ctx context.Context, postID, userID, message string, props map[string]any) (*Post, error) {
	if props == nil {
		props = map[string]any{}
	}
	rawProps, _ := json.Marshal(props)
	now := time.Now().UnixMilli()
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE posts SET message=$1, props=$2, update_at=$3
		WHERE id=$4 AND user_id=$5 AND delete_at=0
	`, message, rawProps, now, postID, userID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, nil
	}
	return s.Get(ctx, postID)
}

// ListByIDs hydrates an ordered list of posts from an ID slice. Used by
// savedposts so the bookmark list can render full post bodies. Returned
// in the same order as `ids`; missing ids are dropped silently.
// Restore zeroes delete_at on a soft-deleted post. Mirrors Delete but
// without the user_id ownership filter — restores are admin-mediated.
// Returns (true, nil) when a row was updated; (false, nil) for a no-op
// (post already active or unknown). Reply posts can be restored without
// restoring the root — Mattermost behaves the same way.
func (s *Service) Restore(ctx context.Context, postID string) (bool, error) {
	now := time.Now().UnixMilli()
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE posts SET delete_at = 0, update_at = $2
		WHERE id = $1 AND delete_at != 0
	`, postID, now)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// MoveThread relocates a root post (and all its replies) to a different
// channel in a single transaction. The post's create_at is preserved so
// the existing reply order stays intact at the destination. Caller is
// responsible for the membership/permission gate. Returns the count of
// rows touched (root + replies). Returns 0 with no error if no rows
// matched (post deleted or already in destination — both are no-ops).
func (s *Service) MoveThread(ctx context.Context, postID, destChannelID string) (int64, error) {
	now := time.Now().UnixMilli()
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	// Move the root + every direct reply (root_id = postID) in one UPDATE
	// using a CTE so we don't hit the table twice. Reply rows have
	// root_id = postID; the root has root_id = '' (or NULL — handled by COALESCE).
	tag, err := tx.Exec(ctx, `
		UPDATE posts
		   SET channel_id = $2,
		       update_at  = $3
		 WHERE (id = $1 OR root_id = $1)
		   AND channel_id <> $2
		   AND delete_at = 0
	`, postID, destChannelID, now)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *Service) ListByIDs(ctx context.Context, ids []string) ([]*Post, error) {
	if len(ids) == 0 {
		return []*Post{}, nil
	}
	rows, err := s.db.Pool.Query(ctx, `SELECT `+allPostColumns+` FROM posts WHERE id = ANY($1) AND delete_at=0`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]*Post{}
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		byID[p.ID] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*Post, 0, len(ids))
	for _, id := range ids {
		if p, ok := byID[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// ListByIDsForUser is the membership-safe variant used for saved/flagged
// posts. A bookmark must never keep exposing a private-channel message after
// its owner leaves that channel.
func (s *Service) ListByIDsForUser(ctx context.Context, userID string, ids []string) ([]*Post, error) {
	if len(ids) == 0 {
		return []*Post{}, nil
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+prefixedCols("p.")+`
		FROM posts p
		JOIN channel_members cm ON cm.channel_id=p.channel_id AND cm.user_id=$2
		WHERE p.id = ANY($1) AND p.delete_at=0
	`, ids, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]*Post{}
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		byID[p.ID] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*Post, 0, len(ids))
	for _, id := range ids {
		if p, ok := byID[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}
