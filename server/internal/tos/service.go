// Package tos implements durable terms-of-service storage. Two tables:
// terms_of_service holds the admin-defined TOS body history (one row per
// revision), and user_terms_of_service tracks each user's accepted revision.
// Phase 28 shipped an in-memory stub for these endpoints; this package
// upgrades that to actual durability so a server restart no longer wipes
// the TOS body or every user's acceptance state.
package tos

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hkjang/moyro/server/internal/store"
)

var ErrNoCurrentTOS = errors.New("no current terms_of_service")

// TOS is one revision of the terms-of-service text.
type TOS struct {
	ID       string `json:"id"`
	UserID   string `json:"user_id"`
	Text     string `json:"text"`
	CreateAt int64  `json:"create_at"`
}

// UserTOS is one user's accepted-TOS pointer.
type UserTOS struct {
	UserID            string `json:"user_id"`
	TermsOfServiceID  string `json:"terms_of_service_id"`
	CreateAt          int64  `json:"create_at"`
	Accepted          bool   `json:"accepted"`
}

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

// Current returns the newest non-deleted TOS row, or ErrNoCurrentTOS when
// none exists. Mattermost's contract treats "no TOS configured" as a 200
// with an empty body, so the handler swaps the error for a zero TOS itself.
func (s *Service) Current(ctx context.Context) (*TOS, error) {
	var t TOS
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, user_id, text, create_at FROM terms_of_service
		WHERE delete_at = 0 ORDER BY create_at DESC LIMIT 1
	`).Scan(&t.ID, &t.UserID, &t.Text, &t.CreateAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoCurrentTOS
		}
		return nil, err
	}
	return &t, nil
}

// Create inserts a fresh TOS revision and returns it. Soft-deletes the
// previous "current" row so Current() always resolves the new one — this is
// the simplest way to keep the active-pointer logic monotonic without a
// separate "is_current" column.
func (s *Service) Create(ctx context.Context, actorID, text string) (*TOS, error) {
	now := time.Now().UnixMilli()
	id := "tos-" + strconv.FormatInt(now, 36)
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		UPDATE terms_of_service SET delete_at = $1 WHERE delete_at = 0
	`, now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO terms_of_service (id, user_id, text, create_at, delete_at)
		VALUES ($1, $2, $3, $4, 0)
	`, id, actorID, text, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &TOS{ID: id, UserID: actorID, Text: text, CreateAt: now}, nil
}

// GetForUser returns the per-user acceptance pointer. Missing row →
// (UserTOS{accepted:false}, nil) so the handler doesn't have to special-case
// "user has never accepted anything".
func (s *Service) GetForUser(ctx context.Context, userID string) (*UserTOS, error) {
	var u UserTOS
	err := s.db.Pool.QueryRow(ctx, `
		SELECT user_id, terms_of_service_id, create_at FROM user_terms_of_service
		WHERE user_id=$1
	`, userID).Scan(&u.UserID, &u.TermsOfServiceID, &u.CreateAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &UserTOS{UserID: userID, Accepted: false}, nil
		}
		return nil, err
	}
	u.Accepted = true
	return &u, nil
}

// Accept records that the user accepted the given TOS revision. Idempotent
// re-accept just updates the timestamp.
func (s *Service) Accept(ctx context.Context, userID, tosID string) error {
	if userID == "" || tosID == "" {
		return errors.New("user_id and terms_of_service_id required")
	}
	now := time.Now().UnixMilli()
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO user_terms_of_service (user_id, terms_of_service_id, create_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET
			terms_of_service_id = EXCLUDED.terms_of_service_id,
			create_at = EXCLUDED.create_at
	`, userID, tosID, now)
	return err
}
