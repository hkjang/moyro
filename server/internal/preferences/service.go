// Package preferences implements Mattermost v4-shaped user preferences.
//
// Each row is a (user_id, category, name) triplet with an opaque string
// value. The Mattermost OpenAPI contract treats `value` as a string, even
// when it carries JSON — keeping the storage TEXT preserves that contract
// so the official webapp/desktop/mobile clients deserialize without any
// translation layer. Common categories include "display_settings" (theme,
// message_display, channel_display_mode), "sidebar_settings",
// "favorite_channel" (name=channel_id, value="true"), "direct_channel_show",
// "advanced_settings", and "tutorial_step".
//
// All bulk operations (Upsert, Delete) run inside a single transaction
// because Mattermost clients send batches and treat the whole batch as
// atomic — partial application on a crash would leave the UI desynced.
package preferences

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/moddle/moddle/server/internal/store"
)

// Preference matches Mattermost's v4 preference shape exactly. Field names
// stay snake-case in JSON so official clients can be wired in without a
// renaming layer.
type Preference struct {
	UserID   string `json:"user_id"`
	Category string `json:"category"`
	Name     string `json:"name"`
	Value    string `json:"value"`
}

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

// ListAll returns every preference owned by userID. Mattermost returns an
// array (never null), so an empty result is `[]` not `nil` — we hand the
// caller a non-nil slice for that reason.
func (s *Service) ListAll(ctx context.Context, userID string) ([]Preference, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT user_id, category, name, value
		FROM preferences WHERE user_id=$1
		ORDER BY category, name
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Preference{}
	for rows.Next() {
		var p Preference
		if err := rows.Scan(&p.UserID, &p.Category, &p.Name, &p.Value); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListCategory returns all preferences in a single category for a user.
// Mirrors `GET /users/{user_id}/preferences/{category}`.
func (s *Service) ListCategory(ctx context.Context, userID, category string) ([]Preference, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT user_id, category, name, value
		FROM preferences WHERE user_id=$1 AND category=$2
		ORDER BY name
	`, userID, category)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Preference{}
	for rows.Next() {
		var p Preference
		if err := rows.Scan(&p.UserID, &p.Category, &p.Name, &p.Value); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetByName fetches a single preference. Returns pgx.ErrNoRows when the
// row is missing so the handler can 404 cleanly.
func (s *Service) GetByName(ctx context.Context, userID, category, name string) (*Preference, error) {
	var p Preference
	err := s.db.Pool.QueryRow(ctx, `
		SELECT user_id, category, name, value
		FROM preferences WHERE user_id=$1 AND category=$2 AND name=$3
	`, userID, category, name).Scan(&p.UserID, &p.Category, &p.Name, &p.Value)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, pgx.ErrNoRows
		}
		return nil, err
	}
	return &p, nil
}

// Upsert applies a batch of preferences atomically — every preference's
// user_id must equal the actor (the caller passes actorID separately so the
// service can reject any cross-user payload before touching the DB). All
// rows in the batch share an update_at so a multi-key save reads as a
// single edit in any future audit/sync stream.
func (s *Service) Upsert(ctx context.Context, actorID string, prefs []Preference) error {
	if len(prefs) == 0 {
		return nil
	}
	for i := range prefs {
		if prefs[i].UserID == "" {
			prefs[i].UserID = actorID
		}
		if prefs[i].UserID != actorID {
			return errors.New("preferences: user_id mismatch")
		}
		if prefs[i].Category == "" || prefs[i].Name == "" {
			return errors.New("preferences: category and name required")
		}
	}
	now := time.Now().UnixMilli()
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, p := range prefs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO preferences (user_id, category, name, value, update_at)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (user_id, category, name) DO UPDATE
			SET value=EXCLUDED.value, update_at=EXCLUDED.update_at
		`, p.UserID, p.Category, p.Name, p.Value, now); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Delete removes a batch of preferences by (category, name). Same actor
// gate as Upsert. Missing rows are not an error — Mattermost treats delete
// as idempotent.
func (s *Service) Delete(ctx context.Context, actorID string, prefs []Preference) error {
	if len(prefs) == 0 {
		return nil
	}
	for i := range prefs {
		if prefs[i].UserID == "" {
			prefs[i].UserID = actorID
		}
		if prefs[i].UserID != actorID {
			return errors.New("preferences: user_id mismatch")
		}
	}
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, p := range prefs {
		if _, err := tx.Exec(ctx, `
			DELETE FROM preferences WHERE user_id=$1 AND category=$2 AND name=$3
		`, p.UserID, p.Category, p.Name); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
