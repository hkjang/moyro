package bookmarks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const bookmarksTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

// TestReorderPlacesTheBookmarkAtTheRequestedPosition pins the contract the
// Reorder doc comment promises: the channel's positions come out dense and
// conflict-free, and the moved row actually lands where the caller asked.
//
// Reorder used to be a single UPDATE of the target row's sort_order, which
// left duplicate sort_order values inside one channel. List breaks those ties
// on `create_at ASC`, so the older bookmark always stayed in front and a drag
// landed one slot off — or did nothing at all. Every assertion below is read
// back through List, because List's order is what a client actually sees.
func TestReorderPlacesTheBookmarkAtTheRequestedPosition(t *testing.T) {
	db := newBookmarksTestDB(t)
	ctx := bookmarksTestContext(t)
	service := New(db)

	// The fixed sample is the one Create itself produces: three bookmarks
	// appended in order, so create_at ascends with sort_order 1,2,3.
	cases := []struct {
		name    string
		channel string
		move    string
		newPos  int
		want    []string
	}{
		{
			name:    "moving a bookmark to the front puts it first",
			channel: "chan-to-front",
			move:    "c",
			newPos:  0,
			want:    []string{"c", "a", "b"},
		},
		{
			name:    "moving a bookmark to the end puts it last",
			channel: "chan-to-back",
			move:    "a",
			newPos:  2,
			want:    []string{"b", "c", "a"},
		},
		{
			name:    "moving a bookmark into the middle puts it there",
			channel: "chan-to-middle",
			move:    "c",
			newPos:  1,
			want:    []string{"a", "c", "b"},
		},
		{
			name:    "reordering a bookmark onto its own position keeps the order",
			channel: "chan-in-place",
			move:    "b",
			newPos:  1,
			want:    []string{"a", "b", "c"},
		},
		{
			name:    "a negative position clamps to the front",
			channel: "chan-clamp-low",
			move:    "c",
			newPos:  -5,
			want:    []string{"c", "a", "b"},
		},
		{
			name:    "a position past the end clamps to the back",
			channel: "chan-clamp-high",
			move:    "a",
			newPos:  99,
			want:    []string{"b", "c", "a"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seedBookmarkChannel(t, ctx, db, tc.channel)

			if err := service.Reorder(ctx, tc.channel, tc.channel+"-"+tc.move, tc.newPos); err != nil {
				t.Fatalf("reorder %s: %v", tc.move, err)
			}

			got := listBookmarkSuffixes(t, ctx, service, tc.channel)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("List order = %v, want %v", got, tc.want)
			}

			// Criterion 1: the surviving positions are dense and unique.
			orders := activeSortOrders(t, ctx, db, tc.channel)
			if want := []int{0, 1, 2}; strings.Join(intsToStrings(orders), ",") != strings.Join(intsToStrings(want), ",") {
				t.Errorf("sorted sort_order values = %v, want %v (dense 0..n-1, no duplicates)", orders, want)
			}
		})
	}
}

// TestReorderLeavesRowsOutsideItsScopeAlone keeps the dense rewrite inside one
// channel's active rows. The rewrite touches every row whose position moved,
// so the blast radius is the part worth pinning: a sibling channel in the same
// table and a soft-deleted row in the target channel must come out with the
// same sort_order and the same update_at, to the millisecond.
func TestReorderLeavesRowsOutsideItsScopeAlone(t *testing.T) {
	db := newBookmarksTestDB(t)
	ctx := bookmarksTestContext(t)
	service := New(db)

	seedBookmarkChannel(t, ctx, db, "chan-target")
	seedBookmarkChannel(t, ctx, db, "chan-bystander")

	// A soft-deleted row in the target channel, parked on a sort_order that
	// the dense rewrite would otherwise want to hand out.
	insertBookmark(t, ctx, db, "chan-target", "chan-target-gone", 1, 1500, 1500, 4242)

	before := bookmarkRowSnapshot(t, ctx, db)

	if err := service.Reorder(ctx, "chan-target", "chan-target-c", 0); err != nil {
		t.Fatalf("reorder: %v", err)
	}

	after := bookmarkRowSnapshot(t, ctx, db)
	for _, id := range []string{
		"chan-bystander-a", "chan-bystander-b", "chan-bystander-c",
		"chan-target-gone",
	} {
		if before[id] != after[id] {
			t.Errorf("row %s = %s, want unchanged %s", id, after[id], before[id])
		}
	}

	// Positive control: the target channel really was rewritten, so the
	// "unchanged" assertions above are not passing because nothing happened.
	if got, want := listBookmarkSuffixes(t, ctx, service, "chan-target"), "c,a,b"; strings.Join(got, ",") != want {
		t.Errorf("target channel order = %v, want %s", got, want)
	}
	if got, want := listBookmarkSuffixes(t, ctx, service, "chan-bystander"), "a,b,c"; strings.Join(got, ",") != want {
		t.Errorf("bystander channel order = %v, want %s", got, want)
	}
}

// TestReorderRejectsIDsOutsideTheChannel keeps the channel-scoped ErrNotFound
// the handler turns into a 404. The dense rewrite reads the channel's rows
// before writing, so an id it cannot find there has to fail before any write.
func TestReorderRejectsIDsOutsideTheChannel(t *testing.T) {
	db := newBookmarksTestDB(t)
	ctx := bookmarksTestContext(t)
	service := New(db)

	seedBookmarkChannel(t, ctx, db, "chan-main")
	seedBookmarkChannel(t, ctx, db, "chan-elsewhere")
	insertBookmark(t, ctx, db, "chan-main", "chan-main-deleted", 9, 1500, 1500, 777)

	cases := []struct {
		name string
		id   string
	}{
		{name: "an unknown id is not found", id: "no-such-bookmark"},
		{name: "a bookmark from another channel is not found", id: "chan-elsewhere-a"},
		{name: "a soft-deleted bookmark is not found", id: "chan-main-deleted"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := bookmarkRowSnapshot(t, ctx, db)

			err := service.Reorder(ctx, "chan-main", tc.id, 0)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("Reorder(%q) error = %v, want ErrNotFound", tc.id, err)
			}

			// A refused reorder writes nothing at all.
			after := bookmarkRowSnapshot(t, ctx, db)
			for id, row := range before {
				if after[id] != row {
					t.Errorf("row %s = %s, want unchanged %s after a refused reorder", id, after[id], row)
				}
			}
		})
	}
}

// seedBookmarkChannel writes the three-bookmark fixture into one channel:
// suffixes a, b, c with ascending create_at and the 1-based sort_order values
// Create hands out (COALESCE(MAX(sort_order),0)+1).
func seedBookmarkChannel(t *testing.T, ctx context.Context, db *store.DB, channelID string) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO channels (id, type, display_name, name, create_at, update_at)
		VALUES ($1, 'O', $1, $1, 1000, 1000)
		ON CONFLICT (id) DO NOTHING
	`, channelID); err != nil {
		t.Fatalf("seed channel %s: %v", channelID, err)
	}
	for i, suffix := range []string{"a", "b", "c"} {
		at := int64(1000 * (i + 1))
		insertBookmark(t, ctx, db, channelID, channelID+"-"+suffix, i+1, at, at, 0)
	}
}

func insertBookmark(t *testing.T, ctx context.Context, db *store.DB, channelID, id string, sortOrder int, createAt, updateAt, deleteAt int64) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, create_at, update_at)
		VALUES ('bookmark-owner', 'bookmark-owner', 'bookmark-owner@example.test', 'x', 1000, 1000)
		ON CONFLICT (id) DO NOTHING
	`); err != nil {
		t.Fatalf("seed bookmark owner: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO channel_bookmarks
		  (id, channel_id, owner_id, display_name, sort_order, type, create_at, update_at, delete_at)
		VALUES ($1, $2, 'bookmark-owner', $1, $3, 'link', $4, $5, $6)
	`, id, channelID, sortOrder, createAt, updateAt, deleteAt); err != nil {
		t.Fatalf("seed bookmark %s: %v", id, err)
	}
}

// listBookmarkSuffixes reads the channel back through the production List and
// returns each bookmark's fixture suffix, so an expectation reads as "c,a,b".
func listBookmarkSuffixes(t *testing.T, ctx context.Context, service *Service, channelID string) []string {
	t.Helper()
	list, err := service.List(ctx, channelID)
	if err != nil {
		t.Fatalf("list %s: %v", channelID, err)
	}
	out := make([]string, 0, len(list))
	for _, b := range list {
		out = append(out, strings.TrimPrefix(b.ID, channelID+"-"))
	}
	return out
}

// activeSortOrders returns the channel's active sort_order values in ascending
// order, duplicates included, so a conflict shows up in the comparison.
func activeSortOrders(t *testing.T, ctx context.Context, db *store.DB, channelID string) []int {
	t.Helper()
	rows, err := db.Pool.Query(ctx, `
		SELECT sort_order FROM channel_bookmarks
		WHERE channel_id=$1 AND delete_at=0
		ORDER BY sort_order ASC
	`, channelID)
	if err != nil {
		t.Fatalf("read sort orders for %s: %v", channelID, err)
	}
	defer rows.Close()
	out := []int{}
	for rows.Next() {
		var order int
		if err := rows.Scan(&order); err != nil {
			t.Fatalf("scan sort order: %v", err)
		}
		out = append(out, order)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sort orders: %v", err)
	}
	return out
}

// bookmarkRowSnapshot captures every row in the table keyed by id, so an
// "untouched" assertion covers sort_order and update_at together.
func bookmarkRowSnapshot(t *testing.T, ctx context.Context, db *store.DB) map[string]string {
	t.Helper()
	rows, err := db.Pool.Query(ctx, `
		SELECT id, channel_id, sort_order, update_at, delete_at
		FROM channel_bookmarks
	`)
	if err != nil {
		t.Fatalf("snapshot bookmarks: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, channelID string
		var sortOrder int
		var updateAt, deleteAt int64
		if err := rows.Scan(&id, &channelID, &sortOrder, &updateAt, &deleteAt); err != nil {
			t.Fatalf("scan snapshot row: %v", err)
		}
		out[id] = fmt.Sprintf("channel=%s sort_order=%d update_at=%d delete_at=%d", channelID, sortOrder, updateAt, deleteAt)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate snapshot: %v", err)
	}
	return out
}

func intsToStrings(values []int) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, fmt.Sprintf("%d", v))
	}
	return out
}

// newBookmarksTestDB follows the sidebar package's harness: a unique schema on
// the shared test PostgreSQL, reached through search_path and dropped on
// cleanup, so these tests never see another package's rows.
func newBookmarksTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(bookmarksTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", bookmarksTestPostgresDSN)
	}
	ctx := bookmarksTestContext(t)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open bookmarks test admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping bookmarks test PostgreSQL: %v", err)
	}

	schemaName := "moyro_bookmarks_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create bookmarks test schema: %v", err)
	}

	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop bookmarks test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse bookmarks test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 4
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated bookmarks test pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated bookmarks test pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate bookmarks test schema: %v", err)
	}
	return db
}

func bookmarksTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	return ctx
}
