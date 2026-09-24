// Package reminders implements post reminders. A user picks a post and a
// future time; when the time arrives a `reminder_fired` WS event is
// delivered to the owning user's sockets with a short excerpt and a deep
// link back to the channel.
//
// Same claim discipline as the scheduled-posts worker: delivered_at=0 is
// pending, flip to -1 during dispatch, then stamp to now on success. The -1 is
// a lease recorded in claimed_at, not a terminal state — a worker that stops
// mid-dispatch would otherwise strand the row out of reach of every reader. A
// dropped delivery isn't catastrophic (user can see the post already in
// their sidebar badge) so we don't retry failed claims aggressively.
package reminders

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/store"
)

// claimLeaseDuration bounds an in-flight dispatch. A worker that stops before
// stamping delivered_at holds its row only this long; the next ClaimDue then
// takes it back. Matches the scheduled-posts worker, whose delivery path has
// the same shape and a far larger per-item budget than a reminder broadcast.
const claimLeaseDuration = 2 * time.Minute

type Reminder struct {
	ID          string `json:"id"`
	UserID      string `json:"user_id"`
	PostID      string `json:"post_id"`
	RemindAt    int64  `json:"remind_at"`
	CreateAt    int64  `json:"create_at"`
	DeliveredAt int64  `json:"delivered_at"`
}

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

// Create enqueues a reminder. Caller validates the user can see the post
// (channel membership) up-front.
func (s *Service) Create(ctx context.Context, userID, postID string, remindAt int64) (*Reminder, error) {
	r := &Reminder{
		ID:       uuid.NewString(),
		UserID:   userID,
		PostID:   postID,
		RemindAt: remindAt,
		CreateAt: time.Now().UnixMilli(),
	}
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO post_reminders (id, user_id, post_id, remind_at, create_at)
		VALUES ($1, $2, $3, $4, $5)
	`, r.ID, r.UserID, r.PostID, r.RemindAt, r.CreateAt)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// ListPending returns reminders that haven't fired yet for the caller.
func (s *Service) ListPending(ctx context.Context, userID string) ([]*Reminder, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, user_id, post_id, remind_at, create_at, delivered_at
		FROM post_reminders
		WHERE user_id=$1 AND delivered_at = 0
		ORDER BY remind_at ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Reminder{}
	for rows.Next() {
		var r Reminder
		if err := rows.Scan(&r.ID, &r.UserID, &r.PostID, &r.RemindAt, &r.CreateAt, &r.DeliveredAt); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// Delete owner-scoped; only pending reminders can be deleted. Delivered
// ones are historical and stay around for user visibility if we ever
// surface them (currently we just hide delivered=1 from the API).
func (s *Service) Delete(ctx context.Context, id, userID string) (bool, error) {
	tag, err := s.db.Pool.Exec(ctx, `
		DELETE FROM post_reminders WHERE id=$1 AND user_id=$2 AND delivered_at = 0
	`, id, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ClaimDue atomically picks pending reminders whose remind_at <= now, plus any
// in-flight claim whose lease has expired. Matches the scheduled-posts pattern:
// a claim is a lease, not a terminal state, so a worker that stops mid-dispatch
// does not strand its rows where no reader can reach them.
func (s *Service) ClaimDue(ctx context.Context, now int64, limit int) ([]*Reminder, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Pool.Query(ctx, `
		UPDATE post_reminders
		SET delivered_at = -1, claimed_at = $1
		WHERE id IN (
			SELECT id FROM post_reminders
			WHERE (delivered_at = 0 AND remind_at <= $1)
			   OR (delivered_at = -1 AND claimed_at <= $1 - $3)
			ORDER BY remind_at ASC
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, user_id, post_id, remind_at, create_at, delivered_at
	`, now, limit, claimLeaseDuration.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Reminder{}
	for rows.Next() {
		var r Reminder
		if err := rows.Scan(&r.ID, &r.UserID, &r.PostID, &r.RemindAt, &r.CreateAt, &r.DeliveredAt); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// MarkDelivered stamps delivered_at = now and releases the lease.
func (s *Service) MarkDelivered(ctx context.Context, id string, at int64) error {
	_, err := s.db.Pool.Exec(ctx, `UPDATE post_reminders SET delivered_at=$1, claimed_at=0 WHERE id=$2`, at, id)
	return err
}
