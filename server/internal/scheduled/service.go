// Package scheduled implements durable, leased delivery of time-delayed posts.
package scheduled

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/store"
)

const (
	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusSucceeded  = "succeeded"
	StatusRetry      = "retry"
	StatusDead       = "dead"
	StatusCancelled  = "cancelled"

	claimLeaseDuration  = 2 * time.Minute
	legacyClaimGrace    = 5 * time.Minute
	maxDeliveryAttempts = 5
	initialRetryDelay   = 30 * time.Second
	maximumRetryDelay   = 15 * time.Minute
)

var ErrStaleClaim = errors.New("scheduled: stale claim")

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

	Status        string `json:"status"`
	ClaimedAt     int64  `json:"claimed_at,omitempty"`
	LeaseUntil    int64  `json:"lease_until,omitempty"`
	ClaimToken    string `json:"-"`
	AttemptCount  int    `json:"attempt_count"`
	NextAttemptAt int64  `json:"next_attempt_at,omitempty"`
	LastErrorCode string `json:"last_error_code,omitempty"`
	LastErrorText string `json:"last_error_text,omitempty"`
	ResultPostID  string `json:"result_post_id,omitempty"`
}

const scheduledPostColumns = `
	id, user_id, channel_id, root_id, message, file_ids, props,
	send_at, create_at, sent_at, error_text,
	status, claimed_at, lease_until, claim_token, attempt_count,
	next_attempt_at, last_error_code, last_error_text, result_post_id`

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
		ID:            uuid.NewString(),
		UserID:        userID,
		ChannelID:     channelID,
		RootID:        rootID,
		Message:       message,
		FileIDs:       fileIDs,
		Props:         props,
		SendAt:        sendAt,
		CreateAt:      time.Now().UnixMilli(),
		Status:        StatusPending,
		NextAttemptAt: sendAt,
	}
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO scheduled_posts
			(id, user_id, channel_id, root_id, message, file_ids, props,
			 send_at, create_at, status, next_attempt_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $8)
	`, sp.ID, sp.UserID, sp.ChannelID, sp.RootID, sp.Message, rawFiles, rawProps, sp.SendAt, sp.CreateAt, sp.Status)
	if err != nil {
		return nil, err
	}
	return sp, nil
}

// ListPending returns only rows the caller can still edit or delete. Processing
// rows are deliberately hidden so the compatibility update handler cannot
// delete-and-recreate a message after a worker owns its lease.
func (s *Service) ListPending(ctx context.Context, userID string) ([]*ScheduledPost, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+scheduledPostColumns+`
		FROM scheduled_posts
		WHERE user_id=$1 AND status IN ('pending', 'retry') AND sent_at=0
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
		       sp.send_at, sp.create_at, sp.sent_at, sp.error_text,
		       sp.status, sp.claimed_at, sp.lease_until, sp.claim_token, sp.attempt_count,
		       sp.next_attempt_at, sp.last_error_code, sp.last_error_text, sp.result_post_id
		FROM scheduled_posts sp
		JOIN channels c ON c.id = sp.channel_id
		WHERE sp.user_id=$1 AND c.team_id=$2
		  AND sp.status IN ('pending', 'retry') AND sp.sent_at=0
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

// Delete is owner-scoped and only removes mutable pending/retry rows.
func (s *Service) Delete(ctx context.Context, id, userID string) (bool, error) {
	tag, err := s.db.Pool.Exec(ctx, `
		DELETE FROM scheduled_posts
		WHERE id=$1 AND user_id=$2
		  AND status IN ('pending', 'retry') AND sent_at=0
	`, id, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ClaimDue atomically leases due pending/retry rows and processing rows whose
// lease expired. Each row receives a distinct token; completion must compare
// and swap that token so an expired worker cannot finalize a newer claim.
func (s *Service) ClaimDue(ctx context.Context, now int64, limit int) ([]*ScheduledPost, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	// During a coordinated v0.1 upgrade, a legacy worker may have left a claim or
	// completion in sent_at without knowing about status or claim_token. Give a
	// newly observed legacy claim a fresh grace lease and reconcile legacy
	// completion before examining expired leases. This is restart recovery, not
	// a mixed-version rolling-upgrade guarantee. In particular, sent_at>0 must
	// never be reclaimed even when an earlier processing lease has expired.
	if _, err := s.db.Pool.Exec(ctx, `
		UPDATE scheduled_posts AS sp
		SET status = CASE
		        WHEN sp.sent_at > 0 THEN 'succeeded'
		        WHEN sp.sent_at = -1 THEN 'processing'
		        ELSE 'retry'
		    END,
		    claimed_at = CASE WHEN sp.sent_at = -1 THEN timing.now_ms ELSE 0 END,
		    lease_until = CASE WHEN sp.sent_at = -1 THEN timing.legacy_lease_until ELSE 0 END,
		    claim_token = CASE WHEN sp.sent_at = -1 THEN 'legacy-' || gen_random_uuid()::text ELSE '' END,
		    attempt_count = CASE WHEN sp.sent_at = -1 THEN sp.attempt_count + 1 ELSE sp.attempt_count END,
		    next_attempt_at = CASE
		        WHEN sp.sent_at > 0 THEN 0
		        WHEN sp.sent_at = -1 THEN timing.legacy_lease_until
		        ELSE GREATEST(sp.send_at, timing.now_ms)
		    END,
		    last_error_code = CASE
		        WHEN sp.sent_at > 0 THEN ''
		        WHEN sp.error_text <> '' THEN 'legacy_error'
		        ELSE sp.last_error_code
		    END,
		    last_error_text = CASE WHEN sp.sent_at > 0 THEN '' ELSE sp.error_text END
		FROM (SELECT $1::BIGINT AS now_ms, $2::BIGINT AS legacy_lease_until) AS timing
		WHERE (sp.status IN ('pending', 'retry') AND sp.sent_at <> 0)
		   OR (sp.status='processing' AND sp.sent_at <> -1)
	`, now, now+legacyClaimGrace.Milliseconds()); err != nil {
		return nil, fmt.Errorf("scheduled: reconcile legacy claim state: %w", err)
	}
	leaseUntil := now + claimLeaseDuration.Milliseconds()
	rows, err := s.db.Pool.Query(ctx, `
		WITH due AS (
			SELECT id
			FROM scheduled_posts
			WHERE send_at <= $1
			  AND sent_at IN (0, -1)
			  AND (
				(status IN ('pending', 'retry') AND sent_at=0 AND next_attempt_at <= $1)
				OR
				(status='processing' AND sent_at=-1 AND lease_until <= $1)
			  )
			ORDER BY CASE WHEN status='processing' THEN lease_until ELSE next_attempt_at END ASC,
			         send_at ASC, id ASC
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		), leased AS (
			SELECT id, gen_random_uuid()::text AS claim_token
			FROM due
		)
		UPDATE scheduled_posts AS sp
		SET status='processing',
		    sent_at=-1,
		    claimed_at=$1,
		    lease_until=$3,
		    claim_token=leased.claim_token,
		    attempt_count=sp.attempt_count+1
		FROM leased
		WHERE sp.id=leased.id
		RETURNING sp.id, sp.user_id, sp.channel_id, sp.root_id, sp.message, sp.file_ids, sp.props,
		          sp.send_at, sp.create_at, sp.sent_at, sp.error_text,
		          sp.status, sp.claimed_at, sp.lease_until, sp.claim_token, sp.attempt_count,
		          sp.next_attempt_at, sp.last_error_code, sp.last_error_text, sp.result_post_id
	`, now, limit, leaseUntil)
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

// MarkSent finalizes only the current lease owner. The generated post id is
// persisted with the compatibility sent_at field for replay diagnostics.
func (s *Service) MarkSent(ctx context.Context, id, claimToken, resultPostID string, sentAt int64) error {
	if claimToken == "" {
		return fmt.Errorf("%w: scheduled post %s", ErrStaleClaim, id)
	}
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE scheduled_posts
		SET status='succeeded', sent_at=$1, error_text='',
		    claimed_at=0, lease_until=0, claim_token='', next_attempt_at=0,
		    last_error_code='', last_error_text='', result_post_id=$2
		WHERE id=$3 AND claim_token=$4 AND status='processing'
	`, sentAt, nullableString(resultPostID), id, claimToken)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: scheduled post %s", ErrStaleClaim, id)
	}
	return nil
}

// MarkFailed uses bounded exponential backoff and moves exhausted work to the
// dead state. Like MarkSent, it is a compare-and-swap on the lease token.
func (s *Service) MarkFailed(ctx context.Context, id, claimToken, errCode, errText string, attemptCount int, failedAt int64) error {
	if claimToken == "" {
		return fmt.Errorf("%w: scheduled post %s", ErrStaleClaim, id)
	}
	status := StatusRetry
	sentAt := int64(0)
	nextAttemptAt := failedAt + retryDelay(attemptCount).Milliseconds()
	if attemptCount >= maxDeliveryAttempts {
		status = StatusDead
		sentAt = -2 // legacy workers claim only 0/-1; keep dead work inert.
		nextAttemptAt = 0
	}
	errText = truncateError(errText)
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE scheduled_posts
		SET status=$1, sent_at=$2, error_text=$3,
		    claimed_at=0, lease_until=0, claim_token='', next_attempt_at=$4,
		    last_error_code=$5, last_error_text=$3
		WHERE id=$6 AND claim_token=$7 AND status='processing'
	`, status, sentAt, errText, nextAttemptAt, errCode, id, claimToken)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: scheduled post %s", ErrStaleClaim, id)
	}
	return nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanScheduled(row scannable) (*ScheduledPost, error) {
	var sp ScheduledPost
	var filesRaw, propsRaw []byte
	var resultPostID *string
	if err := row.Scan(
		&sp.ID, &sp.UserID, &sp.ChannelID, &sp.RootID, &sp.Message, &filesRaw, &propsRaw,
		&sp.SendAt, &sp.CreateAt, &sp.SentAt, &sp.ErrorText,
		&sp.Status, &sp.ClaimedAt, &sp.LeaseUntil, &sp.ClaimToken, &sp.AttemptCount,
		&sp.NextAttemptAt, &sp.LastErrorCode, &sp.LastErrorText, &resultPostID,
	); err != nil {
		return nil, err
	}
	if resultPostID != nil {
		sp.ResultPostID = *resultPostID
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

func retryDelay(attemptCount int) time.Duration {
	if attemptCount < 1 {
		attemptCount = 1
	}
	delay := initialRetryDelay
	for i := 1; i < attemptCount && delay < maximumRetryDelay; i++ {
		delay *= 2
		if delay >= maximumRetryDelay {
			return maximumRetryDelay
		}
	}
	return delay
}

func truncateError(message string) string {
	const maxErrorBytes = 4096
	if len(message) <= maxErrorBytes {
		return message
	}
	message = message[:maxErrorBytes]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
