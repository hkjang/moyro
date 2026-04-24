package teams

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/moddle/moddle/server/internal/store"
)

type Team struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	CreateAt    int64  `json:"create_at"`
	UpdateAt    int64  `json:"update_at"`
	DeleteAt    int64  `json:"delete_at"`
}

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

func (s *Service) Create(ctx context.Context, name, display, teamType, creatorID string) (*Team, error) {
	now := time.Now().UnixMilli()
	t := &Team{
		ID:          uuid.NewString(),
		Name:        name,
		DisplayName: display,
		Type:        teamType,
		CreateAt:    now,
		UpdateAt:    now,
	}
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO teams (id, name, display_name, type, create_at, update_at)
		VALUES ($1,$2,$3,$4,$5,$5)`, t.ID, t.Name, t.DisplayName, t.Type, now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES ($1,$2,'team_admin team_user',$3)`, t.ID, creatorID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return t, nil
}

// EnsureDefault creates the canonical "town-square" team if it doesn't
// exist yet. Returns the current team row either way.
func (s *Service) EnsureDefault(ctx context.Context) (*Team, error) {
	var t Team
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, name, display_name, type, create_at, update_at, delete_at
		FROM teams WHERE name = 'town-square' AND delete_at = 0
	`).Scan(&t.ID, &t.Name, &t.DisplayName, &t.Type, &t.CreateAt, &t.UpdateAt, &t.DeleteAt)
	if err == nil {
		return &t, nil
	}
	now := time.Now().UnixMilli()
	t = Team{
		ID:          uuid.NewString(),
		Name:        "town-square",
		DisplayName: "Town Square",
		Type:        "O",
		CreateAt:    now,
		UpdateAt:    now,
	}
	if _, err := s.db.Pool.Exec(ctx, `
		INSERT INTO teams (id, name, display_name, type, create_at, update_at)
		VALUES ($1,$2,$3,$4,$5,$5)
	`, t.ID, t.Name, t.DisplayName, t.Type, now); err != nil {
		return nil, err
	}
	return &t, nil
}

// Join adds a user as a regular team member, ignoring duplicates.
func (s *Service) Join(ctx context.Context, teamID, userID string) error {
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES ($1,$2,'team_user',$3)
		ON CONFLICT (team_id, user_id) DO NOTHING
	`, teamID, userID, time.Now().UnixMilli())
	return err
}

// IsTeamAdmin reports whether the given user holds team_admin on the team.
// Used by invite / admin handlers so a team owner can manage their own
// invites without needing global system_admin.
func (s *Service) IsTeamAdmin(ctx context.Context, teamID, userID string) (bool, error) {
	var roles string
	err := s.db.Pool.QueryRow(ctx, `
		SELECT roles FROM team_members WHERE team_id=$1 AND user_id=$2
	`, teamID, userID).Scan(&roles)
	if err != nil {
		return false, err
	}
	for _, r := range splitRoles(roles) {
		if r == "team_admin" {
			return true, nil
		}
	}
	return false, nil
}

// IsMember returns true when the user has any row in team_members for the
// given team — used by handlers that gate features on bare team membership
// (channel discovery, etc.) without caring about admin status.
func (s *Service) IsMember(ctx context.Context, teamID, userID string) (bool, error) {
	var exists bool
	err := s.db.Pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM team_members WHERE team_id=$1 AND user_id=$2)
	`, teamID, userID).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// Get fetches a single team record by id. Returns pgx.ErrNoRows when the
// id is unknown so callers can 404 cleanly.
func (s *Service) Get(ctx context.Context, teamID string) (*Team, error) {
	var t Team
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, name, display_name, type, create_at, update_at, delete_at
		FROM teams WHERE id=$1
	`, teamID).Scan(&t.ID, &t.Name, &t.DisplayName, &t.Type, &t.CreateAt, &t.UpdateAt, &t.DeleteAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func splitRoles(s string) []string {
	out := []string{}
	cur := ""
	flush := func() {
		if cur != "" {
			out = append(out, cur)
			cur = ""
		}
	}
	for _, r := range s {
		if r == ' ' || r == '\t' {
			flush()
			continue
		}
		cur += string(r)
	}
	flush()
	return out
}

func (s *Service) ListForUser(ctx context.Context, userID string) ([]Team, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT t.id, t.name, t.display_name, t.type, t.create_at, t.update_at, t.delete_at
		FROM teams t
		JOIN team_members m ON m.team_id = t.id
		WHERE m.user_id = $1 AND t.delete_at = 0
		ORDER BY t.create_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Team
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.Name, &t.DisplayName, &t.Type, &t.CreateAt, &t.UpdateAt, &t.DeleteAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
