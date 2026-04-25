package teams

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

// GetByName resolves a team by its URL-slug name. Mirrors
// `GET /api/v4/teams/name/{name}` so official clients can route by slug.
func (s *Service) GetByName(ctx context.Context, name string) (*Team, error) {
	var t Team
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, name, display_name, type, create_at, update_at, delete_at
		FROM teams WHERE name=$1
	`, name).Scan(&t.ID, &t.Name, &t.DisplayName, &t.Type, &t.CreateAt, &t.UpdateAt, &t.DeleteAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// TeamStats matches Mattermost's team stats response shape exactly. Active
// = total minus deactivated. Total includes deactivated for parity with the
// official endpoint (regular admins use the field for accurate license seat
// reporting; the discrepancy between Total/Active is the deactivated count).
type TeamStats struct {
	TeamID            string `json:"team_id"`
	TotalMemberCount  int64  `json:"total_member_count"`
	ActiveMemberCount int64  `json:"active_member_count"`
}

func (s *Service) Stats(ctx context.Context, teamID string) (*TeamStats, error) {
	out := &TeamStats{TeamID: teamID}
	if err := s.db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM team_members WHERE team_id=$1
	`, teamID).Scan(&out.TotalMemberCount); err != nil {
		return nil, err
	}
	if err := s.db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM team_members tm
		JOIN users u ON u.id = tm.user_id
		WHERE tm.team_id=$1 AND u.delete_at=0
	`, teamID).Scan(&out.ActiveMemberCount); err != nil {
		return nil, err
	}
	return out, nil
}

// TeamMember matches Mattermost's `TeamMember` shape — subset of fields
// the official clients actually consume (id, roles, scheme flags). We omit
// scheme_user/scheme_admin because we don't model team schemes; the role
// string covers both team_user and team_admin.
type TeamMember struct {
	TeamID   string `json:"team_id"`
	UserID   string `json:"user_id"`
	Roles    string `json:"roles"`
	CreateAt int64  `json:"create_at"`
	DeleteAt int64  `json:"delete_at"`
}

func scanTeamMember(row pgx.Row) (*TeamMember, error) {
	var m TeamMember
	if err := row.Scan(&m.TeamID, &m.UserID, &m.Roles, &m.CreateAt); err != nil {
		return nil, err
	}
	m.DeleteAt = 0
	return &m, nil
}

// ListMembers returns paginated team_members for the given team. The
// official endpoint accepts page/per_page and returns an array; we keep
// the same shape so the official admin UI's pagination works unchanged.
func (s *Service) ListMembers(ctx context.Context, teamID string, page, perPage int) ([]TeamMember, error) {
	if perPage <= 0 || perPage > 200 {
		perPage = 60
	}
	if page < 0 {
		page = 0
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT team_id, user_id, COALESCE(roles,''), create_at
		FROM team_members
		WHERE team_id=$1
		ORDER BY create_at ASC
		LIMIT $2 OFFSET $3
	`, teamID, perPage, page*perPage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TeamMember{}
	for rows.Next() {
		var m TeamMember
		if err := rows.Scan(&m.TeamID, &m.UserID, &m.Roles, &m.CreateAt); err != nil {
			return nil, err
		}
		// We don't track team-level membership soft-deletes (the user-level
		// delete_at flag is the source of truth). Field stays in the JSON
		// for response-shape parity with the official endpoint.
		m.DeleteAt = 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetMember returns one team_members row.
func (s *Service) GetMember(ctx context.Context, teamID, userID string) (*TeamMember, error) {
	return scanTeamMember(s.db.Pool.QueryRow(ctx, `
		SELECT team_id, user_id, COALESCE(roles,''), create_at
		FROM team_members
		WHERE team_id=$1 AND user_id=$2
	`, teamID, userID))
}

// ListMembershipsForUser returns every team_members row for a user across
// active teams. This mirrors GET /api/v4/users/{user_id}/teams/members.
func (s *Service) ListMembershipsForUser(ctx context.Context, userID string) ([]TeamMember, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT tm.team_id, tm.user_id, COALESCE(tm.roles,''), tm.create_at
		FROM team_members tm
		JOIN teams t ON t.id = tm.team_id
		WHERE tm.user_id=$1 AND t.delete_at=0
		ORDER BY tm.create_at ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TeamMember{}
	for rows.Next() {
		var m TeamMember
		if err := rows.Scan(&m.TeamID, &m.UserID, &m.Roles, &m.CreateAt); err != nil {
			return nil, err
		}
		m.DeleteAt = 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// MembersByIDs bulk-reads memberships inside a team. Missing user ids are
// intentionally skipped, matching Mattermost's tolerant bulk-read contract.
func (s *Service) MembersByIDs(ctx context.Context, teamID string, userIDs []string) ([]TeamMember, error) {
	out := []TeamMember{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT team_id, user_id, COALESCE(roles,''), create_at
		FROM team_members
		WHERE team_id=$1 AND user_id = ANY($2)
	`, teamID, userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m TeamMember
		if err := rows.Scan(&m.TeamID, &m.UserID, &m.Roles, &m.CreateAt); err != nil {
			return nil, err
		}
		m.DeleteAt = 0
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Service) AddMember(ctx context.Context, teamID, userID string) (*TeamMember, error) {
	if err := s.Join(ctx, teamID, userID); err != nil {
		return nil, err
	}
	return s.GetMember(ctx, teamID, userID)
}

type TeamUnread struct {
	TeamID       string `json:"team_id"`
	MsgCount     int64  `json:"msg_count"`
	MentionCount int64  `json:"mention_count"`
}

func (s *Service) GetUnread(ctx context.Context, userID, teamID string) (*TeamUnread, error) {
	out := &TeamUnread{TeamID: teamID}
	err := s.db.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(cm.msg_count),0), COALESCE(SUM(cm.mention_count),0)
		FROM channel_members cm
		JOIN channels c ON c.id = cm.channel_id
		WHERE cm.user_id=$1 AND c.team_id=$2 AND c.delete_at=0
	`, userID, teamID).Scan(&out.MsgCount, &out.MentionCount)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) ListUnreadForUser(ctx context.Context, userID string) ([]TeamUnread, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT COALESCE(c.team_id,''), COALESCE(SUM(cm.msg_count),0), COALESCE(SUM(cm.mention_count),0)
		FROM channel_members cm
		JOIN channels c ON c.id = cm.channel_id
		WHERE cm.user_id=$1 AND c.delete_at=0 AND c.team_id IS NOT NULL
		GROUP BY c.team_id
		ORDER BY c.team_id ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TeamUnread{}
	for rows.Next() {
		var u TeamUnread
		if err := rows.Scan(&u.TeamID, &u.MsgCount, &u.MentionCount); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Search runs a name+display_name ILIKE match for the team picker. Mirrors
// `POST /api/v4/teams/search` body `{term}`. Only public ('O') teams are
// returned; private teams (we don't model these yet, but the filter is in
// place for forward-compat) require membership before they show up. Caps
// hard at 100 hits because the official client paginates client-side.
func (s *Service) Search(ctx context.Context, term string, page, perPage int) ([]Team, error) {
	if perPage <= 0 || perPage > 100 {
		perPage = 25
	}
	if page < 0 {
		page = 0
	}
	like := "%" + term + "%"
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, name, display_name, type, create_at, update_at, delete_at
		FROM teams
		WHERE delete_at = 0 AND type='O'
		  AND (name ILIKE $1 OR display_name ILIKE $1)
		ORDER BY display_name ASC
		LIMIT $2 OFFSET $3
	`, like, perPage, page*perPage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Team{}
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.Name, &t.DisplayName, &t.Type, &t.CreateAt, &t.UpdateAt, &t.DeleteAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Exists reports whether a team with the given name slug already exists. Used
// by `GET /teams/name/{name}/exists` so signup forms can validate name
// uniqueness without leaking the team's existence to a non-member.
func (s *Service) Exists(ctx context.Context, name string) (bool, error) {
	var exists bool
	err := s.db.Pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM teams WHERE name=$1 AND delete_at=0)
	`, name).Scan(&exists)
	return exists, err
}

// ---- Phase 24 — team patch / update / privacy ----

// TeamPatch carries pointer-typed partial-update fields. nil = leave alone,
// non-nil = set (including empty string to clear). Mirrors Mattermost's
// `PATCH /teams/{id}/patch` body.
type TeamPatch struct {
	Name            *string
	DisplayName     *string
	Description     *string
	CompanyName     *string
	AllowedDomains  *string
	AllowOpenInvite *bool
}

// Patch applies a partial team update. SQL uses CASE WHEN to keep the
// UPDATE atomic — we never read-modify-write so two concurrent patches
// don't trample each other's untouched fields.
//
// We don't have description / company_name / allowed_domains / allow_open_invite
// columns yet — the first three are silently dropped (matching the official
// shape so the handler can accept the payload without erroring), and
// allow_open_invite would normally toggle invite-link visibility. We track
// only what we have today; clients that send the rest get a no-op for those
// fields rather than a 400. This keeps clients that round-trip the full
// object happy without committing us to the broader schema.
func (s *Service) Patch(ctx context.Context, teamID string, p TeamPatch) (*Team, error) {
	name, hasName := derefStr(p.Name)
	display, hasDisplay := derefStr(p.DisplayName)
	now := time.Now().UnixMilli()
	_, err := s.db.Pool.Exec(ctx, `
		UPDATE teams
		SET name         = CASE WHEN $2 THEN $3 ELSE name END,
		    display_name = CASE WHEN $4 THEN $5 ELSE display_name END,
		    update_at    = $6
		WHERE id = $1 AND delete_at = 0
	`, teamID,
		hasName, name,
		hasDisplay, display,
		now)
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, teamID)
}

// SetPrivacy flips team type between 'O' (open) and 'I' (invite-only). The
// official endpoint accepts a `privacy` body field; we accept either form
// for forward-compat.
func (s *Service) SetPrivacy(ctx context.Context, teamID, privacy string) (*Team, error) {
	switch privacy {
	case "O", "I":
	default:
		return nil, errors.New("invalid privacy: must be O or I")
	}
	now := time.Now().UnixMilli()
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE teams SET type=$2, update_at=$3 WHERE id=$1 AND delete_at=0
	`, teamID, privacy, now)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, errors.New("team not found")
	}
	return s.Get(ctx, teamID)
}

func derefStr(p *string) (string, bool) {
	if p == nil {
		return "", false
	}
	return *p, true
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
