package sidebar

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const sidebarTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

// TestListDropsChannelsTheUserCanNoLongerSee pins the membership gate on the
// stored join table. `sidebar_category_channels` rows survive both archiving
// and removal — neither deletes the channel row the foreign key cascades from
// — so a category used to keep listing channels the user had lost access to,
// and no client-side action could clear them.
func TestListDropsChannelsTheUserCanNoLongerSee(t *testing.T) {
	db := newSidebarTestDB(t)
	ctx := sidebarTestContext(t)
	seedSidebarFixture(t, ctx, db)
	service := New(db)

	if _, err := service.ListForTeam(ctx, "user-a", "team-main"); err != nil {
		t.Fatalf("bootstrap defaults: %v", err)
	}
	custom, err := service.Create(ctx, "user-a", "team-main", "Ops", []string{"chan-alpha", "chan-archived", "chan-beta"})
	if err != nil {
		t.Fatalf("create custom category: %v", err)
	}
	if got := custom.ChannelIDs; len(got) != 3 {
		t.Fatalf("fresh custom category = %v, want all three channels", got)
	}

	// chan-archived is archived; user-a is removed from chan-beta. Both keep
	// their sidebar_category_channels rows.
	if _, err := db.Pool.Exec(ctx, `UPDATE channels SET delete_at=99 WHERE id='chan-archived'`); err != nil {
		t.Fatalf("archive channel: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM channel_members WHERE channel_id='chan-beta' AND user_id='user-a'`); err != nil {
		t.Fatalf("remove membership: %v", err)
	}

	listed, err := service.ListForTeam(ctx, "user-a", "team-main")
	if err != nil {
		t.Fatalf("list categories: %v", err)
	}
	if got := categoryChannels(t, listed.Categories, TypeCustom); !equalIDs(got, []string{"chan-alpha"}) {
		t.Fatalf("custom category = %v, want only chan-alpha", got)
	}
	// The lost channels must not resurface under a default category either.
	if got := categoryChannels(t, listed.Categories, TypeChannels); !equalIDs(got, []string{}) {
		t.Fatalf("channels category = %v, want empty", got)
	}

	// Get on the single category agrees with the list view.
	one, err := service.Get(ctx, "user-a", "team-main", custom.ID)
	if err != nil {
		t.Fatalf("get custom category: %v", err)
	}
	if !equalIDs(one.ChannelIDs, []string{"chan-alpha"}) {
		t.Fatalf("get custom category = %v, want only chan-alpha", one.ChannelIDs)
	}
}

// TestFavoritePreferencesStayInsideTheirTeam pins the team gate on the legacy
// `favorite_channel` preference rows. Preferences carry no team, so every
// team's Favorites category used to list every starred channel the user had
// anywhere — including channels of teams the reader was viewing from.
func TestFavoritePreferencesStayInsideTheirTeam(t *testing.T) {
	db := newSidebarTestDB(t)
	ctx := sidebarTestContext(t)
	seedSidebarFixture(t, ctx, db)
	service := New(db)

	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO preferences (user_id, category, name, value, update_at)
		VALUES
			('user-a', 'favorite_channel', 'chan-beta', 'true', 1),
			('user-a', 'favorite_channel', 'chan-side', 'true', 1),
			('user-a', 'favorite_channel', 'chan-archived', 'true', 1)
	`); err != nil {
		t.Fatalf("seed favorites: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE channels SET delete_at=99 WHERE id='chan-archived'`); err != nil {
		t.Fatalf("archive channel: %v", err)
	}

	main, err := service.ListForTeam(ctx, "user-a", "team-main")
	if err != nil {
		t.Fatalf("list main team: %v", err)
	}
	if got := categoryChannels(t, main.Categories, TypeFavorites); !equalIDs(got, []string{"chan-beta"}) {
		t.Fatalf("team-main favorites = %v, want only chan-beta", got)
	}

	side, err := service.ListForTeam(ctx, "user-a", "team-side")
	if err != nil {
		t.Fatalf("list side team: %v", err)
	}
	if got := categoryChannels(t, side.Categories, TypeFavorites); !equalIDs(got, []string{"chan-side"}) {
		t.Fatalf("team-side favorites = %v, want only chan-side", got)
	}
}

// TestDefaultCategoryExcludesChannelsClaimedElsewhere covers the two ways a
// channel could end up listed twice: Get on a default category used to
// auto-classify channels a custom category already held (it only saw its own
// row), and a starred channel placed in a custom category was appended to
// Favorites on top of that.
func TestDefaultCategoryExcludesChannelsClaimedElsewhere(t *testing.T) {
	db := newSidebarTestDB(t)
	ctx := sidebarTestContext(t)
	seedSidebarFixture(t, ctx, db)
	service := New(db)

	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO preferences (user_id, category, name, value, update_at)
		VALUES ('user-a', 'favorite_channel', 'chan-alpha', 'true', 1)
	`); err != nil {
		t.Fatalf("seed favorites: %v", err)
	}
	if _, err := service.ListForTeam(ctx, "user-a", "team-main"); err != nil {
		t.Fatalf("bootstrap defaults: %v", err)
	}
	if _, err := service.Create(ctx, "user-a", "team-main", "Ops", []string{"chan-alpha"}); err != nil {
		t.Fatalf("create custom category: %v", err)
	}

	listed, err := service.ListForTeam(ctx, "user-a", "team-main")
	if err != nil {
		t.Fatalf("list categories: %v", err)
	}
	if got := categoryChannels(t, listed.Categories, TypeFavorites); !equalIDs(got, []string{}) {
		t.Fatalf("favorites = %v, want empty because the custom category claimed the star", got)
	}
	if got := categoryChannels(t, listed.Categories, TypeChannels); !equalIDs(got, []string{"chan-archived", "chan-beta"}) {
		t.Fatalf("channels category = %v, want the two unclaimed channels", got)
	}

	var defaultID string
	for _, c := range listed.Categories {
		if c.Type == TypeChannels {
			defaultID = c.ID
		}
	}
	one, err := service.Get(ctx, "user-a", "team-main", defaultID)
	if err != nil {
		t.Fatalf("get channels category: %v", err)
	}
	if !equalIDs(one.ChannelIDs, []string{"chan-archived", "chan-beta"}) {
		t.Fatalf("get channels category = %v, want the two unclaimed channels", one.ChannelIDs)
	}
}

func categoryChannels(t *testing.T, cats []Category, typ string) []string {
	t.Helper()
	for _, c := range cats {
		if c.Type == typ {
			return c.ChannelIDs
		}
	}
	t.Fatalf("no %q category in %#v", typ, cats)
	return nil
}

func equalIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func seedSidebarFixture(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES ('user-a', 'user-a', 'a@example.test', 'hash', 'system_user', 1, 1);
		INSERT INTO teams (id, name, display_name, type, create_at, update_at)
		VALUES
			('team-main', 'team-main', 'Main Team', 'O', 1, 1),
			('team-side', 'team-side', 'Side Team', 'O', 1, 1);
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES
			('team-main', 'user-a', 'team_user', 1),
			('team-side', 'user-a', 'team_user', 1);
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES
			('chan-alpha',    'team-main', 'O', 'Alpha',    'alpha',    1, 1),
			('chan-beta',     'team-main', 'O', 'Beta',     'beta',     1, 1),
			('chan-archived', 'team-main', 'O', 'Archived', 'archived', 1, 1),
			('chan-side',     'team-side', 'O', 'Side',     'side',     1, 1);
		INSERT INTO channel_members (channel_id, user_id, roles, create_at)
		VALUES
			('chan-alpha',    'user-a', 'channel_user', 1),
			('chan-beta',     'user-a', 'channel_user', 1),
			('chan-archived', 'user-a', 'channel_user', 1),
			('chan-side',     'user-a', 'channel_user', 1)
	`); err != nil {
		t.Fatalf("seed sidebar fixture: %v", err)
	}
}

func newSidebarTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(sidebarTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", sidebarTestPostgresDSN)
	}
	ctx := sidebarTestContext(t)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open sidebar test admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping sidebar test PostgreSQL: %v", err)
	}

	schemaName := "moyro_sidebar_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create sidebar test schema: %v", err)
	}

	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop sidebar test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse sidebar test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 4
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated sidebar test pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated sidebar test pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate sidebar test schema: %v", err)
	}
	return db
}

func sidebarTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	return ctx
}
