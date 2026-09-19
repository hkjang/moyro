package sidebar

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

// TestWriteKeepsOnlyChannelsTheUserCanSee pins the membership gate on the
// write side. Create/Update used to insert whatever ids the client sent: an
// id the user was never a member of (or of another team, or archived) was
// stored and merely hidden on read, and an id that did not exist at all came
// back as a foreign-key error — so a caller could both park foreign channel
// ids and probe which ids exist. Repeated ids in one payload must also
// collapse rather than break the single-statement insert.
func TestWriteKeepsOnlyChannelsTheUserCanSee(t *testing.T) {
	db := newSidebarTestDB(t)
	ctx := sidebarTestContext(t)
	seedSidebarFixture(t, ctx, db)
	service := New(db)

	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES ('user-b', 'user-b', 'b@example.test', 'hash', 'system_user', 1, 1);
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES
			('chan-private', 'team-main', 'P', 'Private', 'private', 1, 1),
			('dm-ab',        NULL,        'D', 'user-a__user-b', 'user-a__user-b', 1, 1);
		INSERT INTO channel_members (channel_id, user_id, roles, create_at)
		VALUES
			('chan-private', 'user-b', 'channel_user', 1),
			('dm-ab',        'user-a', 'channel_user', 1),
			('dm-ab',        'user-b', 'channel_user', 1);
		UPDATE channels SET delete_at=99 WHERE id='chan-archived'
	`); err != nil {
		t.Fatalf("seed write fixture: %v", err)
	}
	if _, err := service.ListForTeam(ctx, "user-a", "team-main"); err != nil {
		t.Fatalf("bootstrap defaults: %v", err)
	}

	// chan-private: exists, user-a is not a member. chan-side: member, but of
	// the other team. chan-archived: member, archived. chan-ghost: no such
	// row. dm-ab: a DM, so it belongs to every team's sidebar.
	custom, err := service.Create(ctx, "user-a", "team-main", "Ops", []string{
		"chan-beta", "chan-private", "chan-side", "chan-archived", "chan-ghost", "dm-ab", "chan-beta", "",
	})
	if err != nil {
		t.Fatalf("create custom category: %v", err)
	}
	if !equalIDs(custom.ChannelIDs, []string{"chan-beta", "dm-ab"}) {
		t.Fatalf("created category = %v, want only the visible channels in payload order", custom.ChannelIDs)
	}
	var stored int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM sidebar_category_channels WHERE category_id=$1
	`, custom.ID).Scan(&stored); err != nil {
		t.Fatalf("count stored rows: %v", err)
	}
	if stored != 2 {
		t.Fatalf("stored %d rows for the custom category, want 2 (the invisible ids must not be written)", stored)
	}

	// Update replaces the list wholesale and applies the same gate; the order
	// of the survivors follows the payload, not the previous state.
	custom.ChannelIDs = []string{"chan-ghost", "dm-ab", "chan-private", "chan-alpha", "dm-ab"}
	updated, err := service.Update(ctx, "user-a", "team-main", *custom)
	if err != nil {
		t.Fatalf("update custom category: %v", err)
	}
	if !equalIDs(updated.ChannelIDs, []string{"dm-ab", "chan-alpha"}) {
		t.Fatalf("updated category = %v, want dm-ab then chan-alpha", updated.ChannelIDs)
	}

	// The channel the update dropped falls back to its default category.
	listed, err := service.ListForTeam(ctx, "user-a", "team-main")
	if err != nil {
		t.Fatalf("list categories: %v", err)
	}
	if got := categoryChannels(t, listed.Categories, TypeChannels); !equalIDs(got, []string{"chan-beta"}) {
		t.Fatalf("channels category = %v, want chan-beta back in the default", got)
	}
	if got := categoryChannels(t, listed.Categories, TypeDirectMessages); !equalIDs(got, []string{}) {
		t.Fatalf("direct_messages category = %v, want empty because the custom category holds the DM", got)
	}
}

// TestUpdateOrderOnlyTouchesTheCallersCategories pins the single-statement
// reorder: every id in the list lands at its index, ids left out keep their
// place, and an id that belongs to another user (or a duplicate) is ignored
// rather than rewritten.
func TestUpdateOrderOnlyTouchesTheCallersCategories(t *testing.T) {
	db := newSidebarTestDB(t)
	ctx := sidebarTestContext(t)
	seedSidebarFixture(t, ctx, db)
	service := New(db)

	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES ('user-b', 'user-b', 'b@example.test', 'hash', 'system_user', 1, 1);
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES ('team-main', 'user-b', 'team_user', 1)
	`); err != nil {
		t.Fatalf("seed second user: %v", err)
	}
	mine, err := service.ListForTeam(ctx, "user-a", "team-main")
	if err != nil {
		t.Fatalf("bootstrap user-a: %v", err)
	}
	theirs, err := service.ListForTeam(ctx, "user-b", "team-main")
	if err != nil {
		t.Fatalf("bootstrap user-b: %v", err)
	}
	custom, err := service.Create(ctx, "user-a", "team-main", "Ops", nil)
	if err != nil {
		t.Fatalf("create custom category: %v", err)
	}
	byType := map[string]string{}
	for _, c := range mine.Categories {
		byType[c.Type] = c.ID
	}
	foreign := theirs.Order[0]
	var foreignBefore int
	if err := db.Pool.QueryRow(ctx, `SELECT sort_order FROM sidebar_categories WHERE id=$1`, foreign).Scan(&foreignBefore); err != nil {
		t.Fatalf("read foreign sort_order: %v", err)
	}

	// Custom first, then direct messages, then favorites; "channels" is left
	// out; user-b's favorites and a repeated id ride along.
	order := []string{custom.ID, foreign, byType[TypeDirectMessages], byType[TypeFavorites], custom.ID}
	if err := service.UpdateOrder(ctx, "user-a", "team-main", order); err != nil {
		t.Fatalf("update order: %v", err)
	}

	got, err := service.Order(ctx, "user-a", "team-main")
	if err != nil {
		t.Fatalf("read order: %v", err)
	}
	// channels kept sort_order 10 from bootstrap, so it now sits between the
	// custom category (0) and direct messages (20).
	want := []string{custom.ID, byType[TypeChannels], byType[TypeDirectMessages], byType[TypeFavorites]}
	if !equalIDs(got, want) {
		t.Fatalf("order after update = %v, want %v", got, want)
	}
	var foreignAfter int
	if err := db.Pool.QueryRow(ctx, `SELECT sort_order FROM sidebar_categories WHERE id=$1`, foreign).Scan(&foreignAfter); err != nil {
		t.Fatalf("re-read foreign sort_order: %v", err)
	}
	if foreignAfter != foreignBefore {
		t.Fatalf("user-b's category sort_order changed %d -> %d through user-a's reorder", foreignBefore, foreignAfter)
	}
}

// TestUpdateRejectsBadFieldsAndKeepsDefaultNames pins the validation Update
// used to lack: a blank display_name on a custom category and a sorting value
// outside the known set were stored as sent (the webapp reads sorting as a
// closed union), and the three default categories could be renamed even
// though Mattermost keeps their display_name for every non-custom type. An
// empty sorting means "leave it alone", the way Mattermost's "" sorting is
// accepted rather than rejected.
func TestUpdateRejectsBadFieldsAndKeepsDefaultNames(t *testing.T) {
	db := newSidebarTestDB(t)
	ctx := sidebarTestContext(t)
	seedSidebarFixture(t, ctx, db)
	service := New(db)

	listed, err := service.ListForTeam(ctx, "user-a", "team-main")
	if err != nil {
		t.Fatalf("bootstrap defaults: %v", err)
	}
	custom, err := service.Create(ctx, "user-a", "team-main", "Ops", []string{"chan-alpha"})
	if err != nil {
		t.Fatalf("create custom category: %v", err)
	}
	before := categoryRow(t, ctx, db, custom.ID)

	for _, tc := range []struct {
		name string
		edit func(c *Category)
	}{
		{"blank display_name", func(c *Category) { c.DisplayName = "   " }},
		{"unknown sorting", func(c *Category) { c.Sorting = "bogus" }},
	} {
		bad := *custom
		bad.Muted = true
		bad.ChannelIDs = []string{"chan-beta"}
		tc.edit(&bad)
		if _, err := service.Update(ctx, "user-a", "team-main", bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: Update error = %v, want ErrInvalid", tc.name, err)
		}
		if after := categoryRow(t, ctx, db, custom.ID); after != before {
			t.Fatalf("%s: row changed although the update was rejected: %+v -> %+v", tc.name, before, after)
		}
		if got := storedChannels(t, ctx, db, custom.ID); !equalIDs(got, []string{"chan-alpha"}) {
			t.Fatalf("%s: channel list changed to %v although the update was rejected", tc.name, got)
		}
	}

	// An empty sorting keeps the stored value (manual for a fresh custom
	// category) while the other fields still apply; the name is trimmed.
	edited := *custom
	edited.DisplayName = "  Ops Team  "
	edited.Sorting = ""
	edited.Collapsed = true
	updated, err := service.Update(ctx, "user-a", "team-main", edited)
	if err != nil {
		t.Fatalf("update with empty sorting: %v", err)
	}
	if updated.DisplayName != "Ops Team" || updated.Sorting != SortingManual || !updated.Collapsed {
		t.Fatalf("updated custom = %+v, want display_name=Ops Team sorting=manual collapsed=true", *updated)
	}

	// A default category ignores the display_name it is sent — even a blank
	// one — and applies everything else.
	var favorites Category
	for _, c := range listed.Categories {
		if c.Type == TypeFavorites {
			favorites = c
		}
	}
	for _, name := range []string{"Renamed", ""} {
		edited := favorites
		edited.DisplayName = name
		edited.Sorting = SortingRecent
		edited.Muted = true
		edited.ChannelIDs = []string{"chan-beta"}
		updated, err := service.Update(ctx, "user-a", "team-main", edited)
		if err != nil {
			t.Fatalf("update favorites with display_name %q: %v", name, err)
		}
		if updated.DisplayName != "Favorites" {
			t.Fatalf("favorites display_name = %q after sending %q, want the stored name kept", updated.DisplayName, name)
		}
		if updated.Sorting != SortingRecent || !updated.Muted || !equalIDs(updated.ChannelIDs, []string{"chan-beta"}) {
			t.Fatalf("favorites after update = %+v, want sorting=recent muted=true channels=[chan-beta]", *updated)
		}
	}
}

// TestUpdateAndDeleteReportMissingCategoriesAsNotFound pins the error
// identity the handlers branch on: a category id that does not exist, or
// belongs to another user or team, is ErrNotFound from Update, Delete and Get
// alike (it used to surface as a raw pgx.ErrNoRows that the handlers turned
// into a 400), while deleting a default category is ErrInvalid.
func TestUpdateAndDeleteReportMissingCategoriesAsNotFound(t *testing.T) {
	db := newSidebarTestDB(t)
	ctx := sidebarTestContext(t)
	seedSidebarFixture(t, ctx, db)
	service := New(db)

	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES ('user-b', 'user-b', 'b@example.test', 'hash', 'system_user', 1, 1);
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES ('team-main', 'user-b', 'team_user', 1)
	`); err != nil {
		t.Fatalf("seed second user: %v", err)
	}
	mine, err := service.ListForTeam(ctx, "user-a", "team-main")
	if err != nil {
		t.Fatalf("bootstrap user-a: %v", err)
	}
	theirs, err := service.Create(ctx, "user-b", "team-main", "Theirs", nil)
	if err != nil {
		t.Fatalf("create user-b category: %v", err)
	}
	sideTeam, err := service.Create(ctx, "user-a", "team-side", "Side", nil)
	if err != nil {
		t.Fatalf("create side-team category: %v", err)
	}

	for _, tc := range []struct{ name, id string }{
		{"no such category", "no-such-category"},
		{"another user's category", theirs.ID},
		{"another team's category", sideTeam.ID},
	} {
		if _, err := service.Update(ctx, "user-a", "team-main", Category{ID: tc.id, DisplayName: "X"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s: Update error = %v, want ErrNotFound", tc.name, err)
		}
		if err := service.Delete(ctx, "user-a", "team-main", tc.id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s: Delete error = %v, want ErrNotFound", tc.name, err)
		}
		if _, err := service.Get(ctx, "user-a", "team-main", tc.id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s: Get error = %v, want ErrNotFound", tc.name, err)
		}
	}
	// The foreign rows are still there — nothing was touched through the
	// wrong (user, team).
	if row := categoryRow(t, ctx, db, theirs.ID); row.DisplayName != "Theirs" {
		t.Fatalf("user-b's category = %+v, want untouched", row)
	}

	for _, c := range mine.Categories {
		if err := service.Delete(ctx, "user-a", "team-main", c.ID); !errors.Is(err, ErrInvalid) {
			t.Fatalf("delete default %s: error = %v, want ErrInvalid", c.Type, err)
		}
	}
	if err := service.Delete(ctx, "user-a", "team-side", sideTeam.ID); err != nil {
		t.Fatalf("delete own custom category: %v", err)
	}
}

// sidebarRow is the stored shape a rejected write must leave alone.
type sidebarRow struct {
	Type, DisplayName, Sorting string
	Muted, Collapsed           bool
	UpdateAt                   int64
}

func categoryRow(t *testing.T, ctx context.Context, db *store.DB, id string) sidebarRow {
	t.Helper()
	var row sidebarRow
	if err := db.Pool.QueryRow(ctx, `
		SELECT type, display_name, sorting, muted, collapsed, update_at
		FROM sidebar_categories WHERE id=$1
	`, id).Scan(&row.Type, &row.DisplayName, &row.Sorting, &row.Muted, &row.Collapsed, &row.UpdateAt); err != nil {
		t.Fatalf("read category %s: %v", id, err)
	}
	return row
}

func storedChannels(t *testing.T, ctx context.Context, db *store.DB, categoryID string) []string {
	t.Helper()
	rows, err := db.Pool.Query(ctx, `
		SELECT channel_id FROM sidebar_category_channels WHERE category_id=$1 ORDER BY sort_order
	`, categoryID)
	if err != nil {
		t.Fatalf("read stored channels: %v", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan stored channel: %v", err)
		}
		out = append(out, id)
	}
	return out
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
