// Package knowledge implements offline-safe full-text retrieval over messages
// and conversation-derived documents. It returns authoritative source cards;
// AI answer generation remains an optional consumer of those already filtered
// sources.
package knowledge

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/store"
)

const (
	DefaultLimit    = 20
	MaxLimit        = 50
	MaxQueryRunes   = 500
	maxExcerptRunes = 1_200
)

var (
	ErrInvalid  = errors.New("knowledge search input is invalid")
	ErrNotFound = errors.New("knowledge search scope not found")
)

type SearchInput struct {
	Query     string
	TeamID    string
	ChannelID string
	Limit     int
}

type Source struct {
	Ref            string  `json:"ref"`
	Kind           string  `json:"kind"`
	ID             string  `json:"id"`
	TeamID         string  `json:"team_id,omitempty"`
	ChannelID      string  `json:"channel_id"`
	PostID         string  `json:"post_id,omitempty"`
	DocumentID     string  `json:"document_id,omitempty"`
	SourceThreadID string  `json:"source_thread_id,omitempty"`
	Title          string  `json:"title,omitempty"`
	Excerpt        string  `json:"excerpt"`
	AuthorID       string  `json:"author_id"`
	AuthorName     string  `json:"author_name"`
	CreateAt       int64   `json:"create_at"`
	UpdateAt       int64   `json:"update_at"`
	Rank           float64 `json:"rank"`
}

type SearchResult struct {
	Sources   []Source `json:"sources"`
	TotalHits int      `json:"total_hits"`
}

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

func normalizeSearchInput(input SearchInput) (SearchInput, error) {
	input.Query = strings.TrimSpace(input.Query)
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.ChannelID = strings.TrimSpace(input.ChannelID)
	if input.Query == "" || input.TeamID == "" || utf8.RuneCountInString(input.Query) > MaxQueryRunes ||
		utf8.RuneCountInString(input.TeamID) > 128 || utf8.RuneCountInString(input.ChannelID) > 128 {
		return SearchInput{}, ErrInvalid
	}
	if !validIdentifier(input.TeamID) || (input.ChannelID != "" && !validIdentifier(input.ChannelID)) {
		return SearchInput{}, ErrInvalid
	}
	var searchable bool
	for _, char := range input.Query {
		if unicode.IsLetter(char) || unicode.IsNumber(char) {
			searchable = true
		}
		if unicode.IsControl(char) {
			return SearchInput{}, ErrInvalid
		}
	}
	if !searchable {
		return SearchInput{}, ErrInvalid
	}
	if input.Limit <= 0 {
		input.Limit = DefaultLimit
	}
	if input.Limit > MaxLimit {
		return SearchInput{}, ErrInvalid
	}
	return input, nil
}

func validIdentifier(value string) bool {
	if value == "" || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func (s *Service) Search(ctx context.Context, principal rbac.Principal, input SearchInput) (*SearchResult, error) {
	var err error
	principal.UserID = strings.TrimSpace(principal.UserID)
	if utf8.RuneCountInString(principal.UserID) > 128 || !validIdentifier(principal.UserID) {
		return nil, ErrInvalid
	}
	input, err = normalizeSearchInput(input)
	if err != nil {
		return nil, err
	}
	constraints := rbac.ResourceConstraintsFor(principal)
	if !constraints.AllowsTeam(input.TeamID) || (input.ChannelID != "" && !constraints.AllowsChannel(input.ChannelID)) {
		return nil, ErrNotFound
	}

	rows, err := s.db.Pool.Query(ctx, `
		WITH search_query AS (
			SELECT websearch_to_tsquery('simple', $1) AS query
		), visible_messages AS (
			SELECT 'message'::text AS kind, p.id, c.team_id, p.channel_id,
			       p.id AS post_id, ''::text AS document_id,
			       CASE WHEN COALESCE(p.root_id,'')='' THEN p.id ELSE p.root_id END AS source_thread_id,
			       ''::text AS title, p.message AS content, p.user_id AS author_id,
			       COALESCE(author.username,'') AS author_name,
			       p.create_at, p.update_at,
			       ts_rank_cd(p.tsv, search_query.query)::double precision AS rank
			FROM search_query, posts p
			JOIN channels c ON c.id=p.channel_id AND c.delete_at=0
			JOIN teams t ON t.id=c.team_id AND t.delete_at=0
			JOIN users viewer ON viewer.id=$2 AND viewer.delete_at=0
			JOIN team_members tm ON tm.team_id=c.team_id AND tm.user_id=viewer.id
			JOIN channel_members cm ON cm.channel_id=c.id AND cm.user_id=viewer.id
			LEFT JOIN users author ON author.id=p.user_id
			WHERE p.delete_at=0 AND c.team_id=$3
			  AND ($4='' OR c.id=$4)
			  AND p.tsv @@ search_query.query
			  AND (NOT $5::boolean OR cardinality($6::text[])=0 OR c.team_id=ANY($6::text[]))
			  AND (NOT $5::boolean OR cardinality($7::text[])=0 OR c.id=ANY($7::text[]))
		), visible_documents AS (
			SELECT 'document'::text AS kind, d.id, COALESCE(d.team_id,'') AS team_id,
			       d.channel_id, ''::text AS post_id, d.id AS document_id,
			       d.source_thread_id, d.title, d.body AS content,
			       d.created_by AS author_id, COALESCE(author.username,'') AS author_name,
			       d.create_at, d.update_at,
			       (ts_rank_cd(d.tsv, search_query.query) * 1.1)::double precision AS rank
			FROM search_query, documents d
			JOIN channels c ON c.id=d.channel_id AND c.delete_at=0
			JOIN teams t ON t.id=c.team_id AND t.delete_at=0
			JOIN users viewer ON viewer.id=$2 AND viewer.delete_at=0
			JOIN team_members tm ON tm.team_id=c.team_id AND tm.user_id=viewer.id
			JOIN channel_members cm ON cm.channel_id=c.id AND cm.user_id=viewer.id
			LEFT JOIN users author ON author.id=d.created_by
			WHERE d.delete_at=0 AND c.team_id=$3
			  AND COALESCE(d.team_id,'')=c.team_id
			  AND ($4='' OR c.id=$4)
			  AND d.tsv @@ search_query.query
			  AND (NOT $5::boolean OR cardinality($6::text[])=0 OR c.team_id=ANY($6::text[]))
			  AND (NOT $5::boolean OR cardinality($7::text[])=0 OR c.id=ANY($7::text[]))
		), combined AS (
			SELECT * FROM visible_messages
			UNION ALL
			SELECT * FROM visible_documents
		)
		SELECT kind, id, team_id, channel_id, post_id, document_id,
		       source_thread_id, title, content, author_id, author_name,
		       create_at, update_at, rank, COUNT(*) OVER()
		FROM combined
		ORDER BY rank DESC, update_at DESC, id DESC
		LIMIT $8
	`, input.Query, principal.UserID, input.TeamID, input.ChannelID,
		constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs, input.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := &SearchResult{Sources: []Source{}}
	messageIndex, documentIndex := 0, 0
	for rows.Next() {
		var source Source
		var content string
		if err := rows.Scan(
			&source.Kind, &source.ID, &source.TeamID, &source.ChannelID,
			&source.PostID, &source.DocumentID, &source.SourceThreadID,
			&source.Title, &content, &source.AuthorID, &source.AuthorName,
			&source.CreateAt, &source.UpdateAt, &source.Rank, &result.TotalHits,
		); err != nil {
			return nil, err
		}
		source.Excerpt = truncateRunes(strings.TrimSpace(content), maxExcerptRunes)
		if source.Kind == "document" {
			documentIndex++
			source.Ref = "D" + strconv.Itoa(documentIndex)
		} else {
			messageIndex++
			source.Ref = "M" + strconv.Itoa(messageIndex)
		}
		result.Sources = append(result.Sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit])) + "…"
}
