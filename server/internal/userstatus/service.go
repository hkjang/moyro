package userstatus

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/hkjang/moyro/server/internal/store"
)

// Valid status values. Mattermost-compatible.
const (
	Online  = "online"
	Away    = "away"
	DND     = "dnd"
	Offline = "offline"
)

type Status struct {
	UserID         string `json:"user_id"`
	Status         string `json:"status"`
	Manual         bool   `json:"manual"`
	LastActivityAt int64  `json:"last_activity_at"`
}

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

// Get returns the current status for a user, synthesising an offline
// default if the user has never checked in.
func (s *Service) Get(ctx context.Context, userID string) (*Status, error) {
	var st Status
	err := s.db.Pool.QueryRow(ctx, `
		SELECT user_id, status, manual, last_activity_at FROM user_statuses WHERE user_id=$1
	`, userID).Scan(&st.UserID, &st.Status, &st.Manual, &st.LastActivityAt)
	if err != nil {
		// Synthesise an offline status rather than 404 — matches Mattermost.
		return &Status{UserID: userID, Status: Offline}, nil
	}
	return &st, nil
}

// GetMany fetches status for a list of user IDs in one round-trip.
func (s *Service) GetMany(ctx context.Context, userIDs []string) ([]Status, error) {
	out := make([]Status, 0, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT user_id, status, manual, last_activity_at FROM user_statuses WHERE user_id = ANY($1::text[])
	`, userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]struct{}{}
	for rows.Next() {
		var st Status
		if err := rows.Scan(&st.UserID, &st.Status, &st.Manual, &st.LastActivityAt); err != nil {
			return nil, err
		}
		out = append(out, st)
		seen[st.UserID] = struct{}{}
	}
	// Pad unseen users with offline so callers don't need to merge.
	for _, uid := range userIDs {
		if _, ok := seen[uid]; !ok {
			out = append(out, Status{UserID: uid, Status: Offline})
		}
	}
	return out, rows.Err()
}

// Set upserts a status row. manual=true means the user explicitly picked
// this state (e.g. DND) and auto-presence should not override it.
func (s *Service) Set(ctx context.Context, userID, status string, manual bool) (*Status, error) {
	now := time.Now().UnixMilli()
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO user_statuses (user_id, status, manual, last_activity_at, update_at)
		VALUES ($1,$2,$3,$4,$4)
		ON CONFLICT (user_id) DO UPDATE SET
			status = EXCLUDED.status,
			manual = EXCLUDED.manual,
			last_activity_at = EXCLUDED.last_activity_at,
			update_at = EXCLUDED.update_at
	`, userID, status, manual, now)
	if err != nil {
		return nil, err
	}
	return &Status{UserID: userID, Status: status, Manual: manual, LastActivityAt: now}, nil
}

// CustomStatus mirrors Mattermost's `{emoji, text, duration?, expires_at?}`
// payload. We store it as a JSONB blob on user_statuses.custom_status so the
// shape stays open-ended (Mattermost sometimes adds fields with point releases)
// without a migration per addition.
type CustomStatus struct {
	Emoji     string `json:"emoji"`
	Text      string `json:"text"`
	Duration  string `json:"duration,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// GetCustomStatus returns the user's custom status, or nil if unset / empty.
// Mattermost's contract is that an absent custom status returns 404, but the
// official client tolerates a 200 + empty object — we return the empty form so
// callers don't need a special-case.
func (s *Service) GetCustomStatus(ctx context.Context, userID string) (*CustomStatus, error) {
	var raw []byte
	err := s.db.Pool.QueryRow(ctx, `SELECT custom_status FROM user_statuses WHERE user_id=$1`, userID).Scan(&raw)
	if err == pgx.ErrNoRows {
		return &CustomStatus{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "{}" {
		return &CustomStatus{}, nil
	}
	var cs CustomStatus
	if err := json.Unmarshal(raw, &cs); err != nil {
		return &CustomStatus{}, nil
	}
	return &cs, nil
}

// SetCustomStatus stamps the JSONB column. We auto-create the user_statuses
// row if it doesn't exist yet so a brand-new account that hasn't touched the
// presence WS yet can still set a custom status.
func (s *Service) SetCustomStatus(ctx context.Context, userID string, cs CustomStatus) error {
	raw, err := json.Marshal(cs)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	_, err = s.db.Pool.Exec(ctx, `
		INSERT INTO user_statuses (user_id, status, manual, last_activity_at, update_at, custom_status)
		VALUES ($1, 'online', false, $2, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET
			custom_status = EXCLUDED.custom_status,
			update_at     = EXCLUDED.update_at
	`, userID, now, raw)
	return err
}

// ClearCustomStatus zeroes the JSONB blob (without touching presence/manual).
func (s *Service) ClearCustomStatus(ctx context.Context, userID string) error {
	_, err := s.db.Pool.Exec(ctx, `UPDATE user_statuses SET custom_status='{}'::jsonb, update_at=$2 WHERE user_id=$1`, userID, time.Now().UnixMilli())
	return err
}

// SetAuto writes a status only when the user has not explicitly pinned a
// state (i.e. existing row has manual=false, or no row exists yet). Used
// by the WS hub on connect/disconnect so DND/away choices stick across
// reconnects.
func (s *Service) SetAuto(ctx context.Context, userID, status string) (*Status, error) {
	now := time.Now().UnixMilli()
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO user_statuses (user_id, status, manual, last_activity_at, update_at)
		VALUES ($1,$2,false,$3,$3)
		ON CONFLICT (user_id) DO UPDATE SET
			status = EXCLUDED.status,
			last_activity_at = EXCLUDED.last_activity_at,
			update_at = EXCLUDED.update_at
		WHERE user_statuses.manual = false
	`, userID, status, now)
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, userID)
}
