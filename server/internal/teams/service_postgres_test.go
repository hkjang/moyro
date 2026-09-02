package teams

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestTeamMembershipAndRoles pins the membership contract: creators are
// admins, joins are idempotent and non-admin, role writes are canonicalised,
// and removal leaves other teams alone.
func TestTeamMembershipAndRoles(t *testing.T) {
	db := newTeamsTestDB(t)
	ctx := teamsTestContext(t)
	seedTeamsFixture(t, ctx, db)
	service := New(db)

	created, err := service.Create(ctx, "new-team", "New Team", "O", "user-c")
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	if admin, err := service.IsTeamAdmin(ctx, created.ID, "user-c"); err != nil || !admin {
		t.Fatalf("creator admin = %v, %v", admin, err)
	}

	if err := service.Join(ctx, created.ID, "user-a"); err != nil {
		t.Fatalf("join: %v", err)
	}
	if err := service.Join(ctx, created.ID, "user-a"); err != nil {
		t.Fatalf("second join must be a no-op: %v", err)
	}
	if admin, _ := service.IsTeamAdmin(ctx, created.ID, "user-a"); admin {
		t.Fatal("joining member became admin")
	}
	if member, err := service.IsMember(ctx, created.ID, "user-a"); err != nil || !member {
		t.Fatalf("member after join = %v, %v", member, err)
	}

	// Roles are de-duplicated and whitespace-normalised; an empty role string
	// is refused rather than stripping every role.
	if ok, err := service.SetMemberRoles(ctx, created.ID, "user-a", "  team_user   team_admin team_user "); err != nil || !ok {
		t.Fatalf("set roles = %v, %v", ok, err)
	}
	if got, err := service.GetMember(ctx, created.ID, "user-a"); err != nil || got.Roles != "team_user team_admin" {
		t.Fatalf("roles after set = %#v, %v", got, err)
	}
	if ok, err := service.SetMemberRoles(ctx, created.ID, "user-a", "   "); err != nil || ok {
		t.Fatalf("blank roles accepted: %v, %v", ok, err)
	}
	if ok, err := service.SetMemberRoles(ctx, created.ID, "user-nobody", "team_user"); err != nil || ok {
		t.Fatalf("roles for non-member reported success: %v, %v", ok, err)
	}

	if ok, err := service.RemoveMember(ctx, created.ID, "user-a"); err != nil || !ok {
		t.Fatalf("remove member = %v, %v", ok, err)
	}
	if member, _ := service.IsMember(ctx, created.ID, "user-a"); member {
		t.Fatal("member still present after removal")
	}
	if member, _ := service.IsMember(ctx, "team-1", "user-a"); !member {
		t.Fatal("removal from one team removed membership in another")
	}
	if _, err := service.IsTeamAdmin(ctx, created.ID, "user-a"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("admin check for removed member = %v, want ErrNoRows", err)
	}
}

// TestTeamUnreadAggregatesLiveChannelsOnly keeps the team badge honest:
// archived channels and other teams do not count.
func TestTeamUnreadAggregatesLiveChannelsOnly(t *testing.T) {
	db := newTeamsTestDB(t)
	ctx := teamsTestContext(t)
	seedTeamsFixture(t, ctx, db)
	service := New(db)

	unread, err := service.GetUnread(ctx, "user-a", "team-1")
	if err != nil {
		t.Fatalf("get unread: %v", err)
	}
	if unread.MsgCount != 5 || unread.MentionCount != 1 {
		t.Fatalf("team-1 unread = %#v", unread)
	}

	if _, err := db.Pool.Exec(ctx, `UPDATE channels SET delete_at=1 WHERE id='channel-2'`); err != nil {
		t.Fatalf("archive channel: %v", err)
	}
	unread, _ = service.GetUnread(ctx, "user-a", "team-1")
	if unread.MsgCount != 3 {
		t.Fatalf("archived channel still counted: %#v", unread)
	}

	all, err := service.ListUnreadForUser(ctx, "user-a")
	if err != nil {
		t.Fatalf("list unread: %v", err)
	}
	byTeam := map[string]TeamUnread{}
	for _, u := range all {
		byTeam[u.TeamID] = u
	}
	if byTeam["team-1"].MsgCount != 3 || byTeam["team-2"].MsgCount != 5 || byTeam["team-2"].MentionCount != 2 {
		t.Fatalf("per-team unread = %#v", byTeam)
	}
}

// TestTeamLifecycleAndLookup covers archive/restore visibility and the
// name-based lookups the router relies on.
func TestTeamLifecycleAndLookup(t *testing.T) {
	db := newTeamsTestDB(t)
	ctx := teamsTestContext(t)
	seedTeamsFixture(t, ctx, db)
	service := New(db)

	if exists, err := service.Exists(ctx, "team-1"); err != nil || !exists {
		t.Fatalf("exists = %v, %v", exists, err)
	}
	if exists, _ := service.Exists(ctx, "no-such-team"); exists {
		t.Fatal("unknown team reported as existing")
	}
	if team, err := service.GetByName(ctx, "team-1"); err != nil || team.ID != "team-1" {
		t.Fatalf("get by name = %#v, %v", team, err)
	}

	mine, err := service.ListForUser(ctx, "user-a")
	if err != nil || len(mine) != 2 {
		t.Fatalf("teams for user-a = %#v, %v", mine, err)
	}

	if ok, err := service.Delete(ctx, "team-2"); err != nil || !ok {
		t.Fatalf("delete = %v, %v", ok, err)
	}
	if ok, err := service.Delete(ctx, "team-2"); err != nil || ok {
		t.Fatalf("second delete = %v, %v (want false, nil)", ok, err)
	}
	mine, _ = service.ListForUser(ctx, "user-a")
	if len(mine) != 1 || mine[0].ID != "team-1" {
		t.Fatalf("archived team still listed: %#v", mine)
	}
	if ok, err := service.Restore(ctx, "team-2"); err != nil || !ok {
		t.Fatalf("restore = %v, %v", ok, err)
	}
	mine, _ = service.ListForUser(ctx, "user-a")
	if len(mine) != 2 {
		t.Fatalf("restored team missing: %#v", mine)
	}

	// Search matches name or display name, case-insensitively.
	hits, err := service.Search(ctx, "TEAM t", 0, 20)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "team-2" {
		t.Fatalf("search hits = %#v", hits)
	}
}

func seedTeamsFixture(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES
			('user-a', 'user-a', 'a@example.test', 'hash', 'system_user', 1, 1),
			('user-b', 'user-b', 'b@example.test', 'hash', 'system_user', 1, 1),
			('user-c', 'user-c', 'c@example.test', 'hash', 'system_user', 1, 1);
		INSERT INTO teams (id, name, display_name, type, create_at, update_at)
		VALUES ('team-1', 'team-1', 'Team One', 'O', 1, 1), ('team-2', 'team-2', 'Team Two', 'O', 1, 1);
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES ('team-1', 'user-a', 'team_admin team_user', 1), ('team-1', 'user-b', 'team_user', 1), ('team-2', 'user-a', 'team_user', 1);
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES ('channel-1', 'team-1', 'O', 'General', 'general', 1, 1), ('channel-2', 'team-1', 'O', 'Other', 'other', 1, 1), ('channel-3', 'team-2', 'O', 'Far', 'far', 1, 1);
		INSERT INTO channel_members (channel_id, user_id, roles, create_at, msg_count, mention_count)
		VALUES ('channel-1', 'user-a', 'channel_user', 1, 3, 1), ('channel-2', 'user-a', 'channel_user', 1, 2, 0), ('channel-3', 'user-a', 'channel_user', 1, 5, 2), ('channel-1', 'user-b', 'channel_user', 1, 0, 0);
		INSERT INTO posts (id, channel_id, user_id, root_id, message, props, file_ids, is_pinned, create_at, update_at)
		VALUES ('post-root', 'channel-1', 'user-a', '', 'root', '{}', '[]', FALSE, 1000, 1000),
		       ('post-reply', 'channel-1', 'user-b', 'post-root', 'reply', '{}', '[]', FALSE, 2000, 2000),
		       ('post-gone', 'channel-1', 'user-a', '', 'deleted', '{}', '[]', FALSE, 3000, 3000)
	`); err != nil {
		t.Fatalf("seed teams fixture: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE posts SET delete_at=4000 WHERE id='post-gone'`); err != nil {
		t.Fatalf("soft-delete fixture post: %v", err)
	}
}

func newTeamsTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("MOYRO_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("MOYRO_TEST_POSTGRES_DSN is not set")
	}
	ctx := teamsTestContext(t)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open teams test admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping teams test PostgreSQL: %v", err)
	}
	schemaName := "moyro_teams_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create teams test schema: %v", err)
	}
	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop teams test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse teams test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 4
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated teams test pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated teams test pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate teams test schema: %v", err)
	}
	return db
}

func teamsTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	return ctx
}
