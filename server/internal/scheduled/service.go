// Package scheduled implements time-delayed post delivery. Users can queue
// a post to fire at a specific wall-clock time; a Worker running on a 30s
// ticker claims due rows and hands them to the regular posts.Service so
// every invariant (plugin hooks aside — we skip those for scheduled
// delivery) that applies to a live post also applies here.
//
// Claim flow: pending rows have sent_at=0; the worker flips sent_at to -1
// (in-progress) in one UPDATE … RETURNING so concurrent workers can't
// double-fire. On success we stamp sent_at = now(). On failure we leave
// sent_at = -1 and set error_text; the next tick's WHERE clause (sent_at
// IN (0, -1) AND create_at < now()-retryBackoff) retries stuck rows.
package scheduled

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/moddle/moddle/server/internal/store"
)

type ScheduledPost struct {
	ID        string         `json:"id"`
	UserID    string         `json:"user_id"`
	ChannelID string         `json:"channel_id"`
	RootID    string         `json:"root_id"`
	Message   string         `json:"message"`
	FileIDs   []string       `json:"file_ids"`
	Props     map[string]any `json:"props"`
	SendAt    int64          `json:"send_at"`
	CreateAt  int64          `json:"create_at"`
	SentAt    int64          `json:"sent_at"`
	ErrorText string         `json:"error_text"`
}

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

// Create enqueues a scheduled post. sendAt must be in the future; callers
// validate up-front because we have no clock-skew policy here.
func (s *Service) Create(ctx context.Context, userID, channelID, rootID, message string, fileIDs []string, props map[string]any, sendAt int64) (*ScheduledPost, error) {
	if fileIDs == nil {
		fileIDs = []string{}
	}
	if props == nil {
		props = map[string]any{}
	}
	rawFiles, _ := json.Marshal(fileIDs)
	rawProps, _ := json.Marshal(props)
	sp := &ScheduledPost{
		ID:        uuid.NewString(),
		UserID:    userID,
		ChannelID: channelID,
		RootID:    rootID,
		Message:   message,
		FileIDs:   fileIDs,
		Props:     props,
		SendAt:    sendAt,
		CreateAt:  time.Now().UnixMilli(),
	}
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO scheduled_posts (id, user_id, channel_id, root_id, message, file_ids, props, send_at, create_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, sp.ID, sp.UserID, sp.ChannelID, sp.RootID, sp.Message, rawFiles, rawProps, sp.SendAt, sp.CreateAt)
	if err != nil {
		return nil, err
	}
	return sp, nil
}

// ListPending returns the caller's pending scheduled posts (sent_at=0),
// ordered by send_at ASC so the UI shows "next up" first.
func (s *Service) ListPending(ctx context.Context, userID string) ([]*ScheduledPost, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, user_id, channel_id, root_id, message, file_ids, props, send_at, create_at, sent_at, error_text
		FROM scheduled_posts
		WHERE user_id=$1 AND sent_at <= 0
		ORDER BY send_at ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ScheduledPost{}
	for rows.Next() {
		sp, err := scanScheduled(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// ListPendingForTeam returns the caller's pending scheduled posts scoped to
// channels in one team. Mirrors Mattermost's GET /posts/scheduled/team/{id}.
func (s *Service) ListPendingForTeam(ctx context.Context, userID, teamID string) ([]*ScheduledPost, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT sp.id, sp.user_id, sp.channel_id, sp.root_id, sp.message, sp.file_ids, sp.props,
		       sp.send_at, sp.create_at, sp.sent_at, sp.error_text
		FROM scheduled_posts sp
		JOIN channels c ON c.id = sp.channel_id
		WHERE sp.user_id=$1 AND c.team_id=$2 AND sp.sent_at <= 0
		ORDER BY sp.send_at ASC
	`, userID, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ScheduledPost{}
	for rows.Next() {
		sp, err := scanScheduled(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// Delete owner-scoped; returns true if a row was removed (for the handler
// to decide between 200 and 404). Only pending rows are deletable; once
// sent_at > 0 the post has already gone out, so the row is immutable.
func (s *Service) Delete(ctx context.Context, id, userID string) (bool, error) {
	tag, err := s.db.Pool.Exec(ctx, `
		DELETE FROM scheduled_posts WHERE id=$1 AND user_id=$2 AND sent_at <= 0
	`, id, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ClaimDue atomically claims up to `limit` pending rows whose send_at <= now
// by flipping sent_at to -1. Returns the claimed rows. Retry-safe: if the
// worker crashes mid-dispatch the rows sit at -1 until the next tick picks
// them back up (via the OR clause on sent_at = -1 AND send_at < now - 5m).
func (s *Service) ClaimDue(ctx context.Context, now int64, limit int) ([]*ScheduledPost, error) {
	if limit <= 0 {
		limit = 50
	}
	// Retry stuck rows (-1) that were claimed > 5 min ago — handles crashed
	// workers. Fresh -1 rows are left alone to avoid double-fire.
	retryBefore := now - 5*60*1000
	rows, err := s.db.Pool.Query(ctx, `
		UPDATE scheduled_posts
		SET sent_at = -1
		WHERE id IN (
			SELECT id FROM scheduled_posts
			WHERE (sent_at = 0 OR (sent_at = -1 AND create_at < $2))
			  AND send_at <= $1
			ORDER BY send_at ASC
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, user_id, channel_id, root_id, message, file_ids, props, send_at, create_at, sent_at, error_text
	`, now, retryBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ScheduledPost{}
	for rows.Next() {
		sp, err := scanScheduled(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// MarkSent stamps sent_at = now so a delivered row never re-fires.
func (s *Service) MarkSent(ctx context.Context, id string, sentAt int64) error {
	_, err := s.db.Pool.Exec(ctx, `UPDATE scheduled_posts SET sent_at=$1, error_text='' WHERE id=$2`, sentAt, id)
	return err
}

// MarkFailed records the last error and resets sent_at to 0 so ClaimDue's
// retry path picks the row up again on the next tick.
func (s *Service) MarkFailed(ctx context.Context, id, errText string) error {
	_, err := s.db.Pool.Exec(ctx, `UPDATE scheduled_posts SET sent_at=0, error_text=$1 WHERE id=$2`, errText, id)
	return err
}

type scannable interface {
	Scan(dest ...any) error
}

func scanScheduled(row scannable) (*ScheduledPost, error) {
	var sp ScheduledPost
	var filesRaw, propsRaw []byte
	if err := row.Scan(&sp.ID, &sp.UserID, &sp.ChannelID, &sp.RootID, &sp.Message, &filesRaw, &propsRaw, &sp.SendAt, &sp.CreateAt, &sp.SentAt, &sp.ErrorText); err != nil {
		return nil, err
	}
	if len(filesRaw) > 0 {
		_ = json.Unmarshal(filesRaw, &sp.FileIDs)
	}
	if sp.FileIDs == nil {
		sp.FileIDs = []string{}
	}
	if len(propsRaw) > 0 {
		_ = json.Unmarshal(propsRaw, &sp.Props)
	}
	if sp.Props == nil {
		sp.Props = map[string]any{}
	}
	return &sp, nil
}
