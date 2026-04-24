package userstatus

import (
	"context"
	"time"

	"github.com/moddle/moddle/server/internal/store"
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
