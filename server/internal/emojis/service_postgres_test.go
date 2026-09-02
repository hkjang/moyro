package emojis

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

// TestSearchReachesEmojisOutsideTheNewestPage pins the fix for autocomplete:
// matching used to run over a fetched page of the newest rows, so an older
// emoji became unreachable once enough newer ones existed. The match now runs
// in the database over every live row.
func TestSearchReachesEmojisOutsideTheNewestPage(t *testing.T) {
	db := newEmojisTestDB(t)
	ctx := emojisTestContext(t)
	seedEmojisFixture(t, ctx, db)
	service := New(db, nil)

	// One old, distinctly named emoji buried under a page's worth of newer ones.
	insertEmoji(t, ctx, db, "party-parrot", 1_000)
	for i := 0; i < 220; i++ {
		insertEmoji(t, ctx, db, fmt.Sprintf("filler-%03d", i), int64(10_000+i))
	}

	found, err := service.Search(ctx, "parrot", 200)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found) != 1 || found[0].Name != "party-parrot" {
		t.Fatalf("search(parrot) = %#v, want only party-parrot", found)
	}

	// Substring matching still behaves like the old client-side Contains.
	if found, err = service.Search(ctx, "arty-par", 200); err != nil || len(found) != 1 {
		t.Fatalf("substring search = %#v, %v", found, err)
	}

	// The result cap is honored and the newest rows come first.
	capped, err := service.Search(ctx, "filler", 10)
	if err != nil {
		t.Fatalf("capped search: %v", err)
	}
	if len(capped) != 10 {
		t.Fatalf("capped search returned %d rows, want 10", len(capped))
	}
	if capped[0].Name != "filler-219" {
		t.Fatalf("capped search is not newest-first: %q", capped[0].Name)
	}

	// An empty term is the pre-typing autocomplete case: everything, capped.
	all, err := service.Search(ctx, "", 200)
	if err != nil {
		t.Fatalf("empty-term search: %v", err)
	}
	if len(all) != 200 {
		t.Fatalf("empty-term search returned %d rows, want the 200 cap", len(all))
	}
}

// TestSearchTreatsWildcardsAsLiteralCharacters keeps a user-typed `%` or `_`
// from turning autocomplete into a match-everything query.
func TestSearchTreatsWildcardsAsLiteralCharacters(t *testing.T) {
	db := newEmojisTestDB(t)
	ctx := emojisTestContext(t)
	seedEmojisFixture(t, ctx, db)
	service := New(db, nil)

	insertEmoji(t, ctx, db, "shipit", 1_000)
	insertEmoji(t, ctx, db, "tada", 2_000)

	for _, term := range []string{"%", "_", "%a%"} {
		found, err := service.Search(ctx, term, 200)
		if err != nil {
			t.Fatalf("search(%q): %v", term, err)
		}
		if len(found) != 0 {
			t.Fatalf("search(%q) = %#v, want no literal matches", term, found)
		}
	}
}

// TestSearchAndBatchLookupIgnoreDeletedEmojis keeps a soft-deleted shortcode
// out of both discovery surfaces.
func TestSearchAndBatchLookupIgnoreDeletedEmojis(t *testing.T) {
	db := newEmojisTestDB(t)
	ctx := emojisTestContext(t)
	seedEmojisFixture(t, ctx, db)
	service := New(db, nil)

	insertEmoji(t, ctx, db, "retired", 1_000)
	if err := service.Delete(ctx, "user-a", emojiIDFor(t, ctx, db, "retired"), false); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if found, err := service.Search(ctx, "retired", 200); err != nil || len(found) != 0 {
		t.Fatalf("deleted emoji still searchable: %#v, %v", found, err)
	}
	if found, err := service.GetManyByNames(ctx, []string{"retired"}); err != nil || len(found) != 0 {
		t.Fatalf("deleted emoji still resolvable by name: %#v, %v", found, err)
	}
}

// TestGetManyByNamesResolvesABatchInRequestOrder pins the batch contract the
// `/emoji/names` endpoint relies on now that it no longer issues one query per
// requested name: requested order is preserved and misses are simply absent.
func TestGetManyByNamesResolvesABatchInRequestOrder(t *testing.T) {
	db := newEmojisTestDB(t)
	ctx := emojisTestContext(t)
	seedEmojisFixture(t, ctx, db)
	service := New(db, nil)

	insertEmoji(t, ctx, db, "alpha", 1_000)
	insertEmoji(t, ctx, db, "bravo", 2_000)
	insertEmoji(t, ctx, db, "charlie", 3_000)

	found, err := service.GetManyByNames(ctx, []string{"charlie", "nope", "alpha"})
	if err != nil {
		t.Fatalf("batch lookup: %v", err)
	}
	if len(found) != 2 || found[0].Name != "charlie" || found[1].Name != "alpha" {
		t.Fatalf("batch lookup = %#v, want charlie then alpha", found)
	}

	// An empty request is an empty slice, never nil, so handlers emit `[]`.
	empty, err := service.GetManyByNames(ctx, nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty batch = %#v, %v", empty, err)
	}
}

// TestDeletedEmojiNameCanBeReused pins the migration that narrowed the name
// uniqueness constraint to live rows. Before it, deleting an emoji burned its
// shortcode forever: the live-only collision probe passed but the INSERT hit
// the table-wide UNIQUE and reported the name as taken.
func TestDeletedEmojiNameCanBeReused(t *testing.T) {
	db := newEmojisTestDB(t)
	ctx := emojisTestContext(t)
	seedEmojisFixture(t, ctx, db)
	service := New(db, nil)

	insertEmoji(t, ctx, db, "shipit", 1_000)
	original := emojiIDFor(t, ctx, db, "shipit")
	if err := service.Delete(ctx, "user-a", original, false); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Re-registering the freed name is the exact INSERT the service performs
	// after a successful upload.
	replacement := uuid.NewString()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO emojis (id, name, creator_id, file_id, create_at)
		VALUES ($1, 'shipit', 'user-a', 'file-a', 5000)
	`, replacement); err != nil {
		t.Fatalf("reuse deleted emoji name: %v", err)
	}

	live, err := service.GetByName(ctx, "shipit")
	if err != nil || live.ID != replacement {
		t.Fatalf("GetByName after reuse = %#v, %v", live, err)
	}

	// Two live rows for one name are still rejected.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO emojis (id, name, creator_id, file_id, create_at)
		VALUES ($1, 'shipit', 'user-a', 'file-a', 6000)
	`, uuid.NewString()); err == nil {
		t.Fatal("a second live emoji shares the name; live uniqueness was lost")
	}

	// The soft-deleted row survives so historical posts still resolve it.
	if _, err := service.Get(ctx, original); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted row lookup = %v, want ErrNotFound", err)
	}
	var deletedAt int64
	if err := db.Pool.QueryRow(ctx, `SELECT delete_at FROM emojis WHERE id=$1`, original).Scan(&deletedAt); err != nil {
		t.Fatalf("original row disappeared: %v", err)
	}
	if deletedAt == 0 {
		t.Fatal("original row is not soft-deleted")
	}
}

func insertEmoji(t *testing.T, ctx context.Context, db *store.DB, name string, createAt int64) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO emojis (id, name, creator_id, file_id, create_at)
		VALUES ($1, $2, 'user-a', 'file-a', $3)
	`, uuid.NewString(), name, createAt); err != nil {
		t.Fatalf("insert emoji %q: %v", name, err)
	}
}

func emojiIDFor(t *testing.T, ctx context.Context, db *store.DB, name string) string {
	t.Helper()
	var id string
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM emojis WHERE name=$1 AND delete_at=0`, name).Scan(&id); err != nil {
		t.Fatalf("resolve emoji %q: %v", name, err)
	}
	return id
}

func seedEmojisFixture(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES ('user-a', 'user-a', 'a@example.test', 'hash', 'system_user', 1, 1);
		INSERT INTO file_infos (id, user_id, path, name, size, mime_type, create_at, update_at)
		VALUES ('file-a', 'user-a', 'emoji/file-a.png', 'emoji.png', 128, 'image/png', 1, 1)
	`); err != nil {
		t.Fatalf("seed emojis fixture: %v", err)
	}
}

func newEmojisTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("MOYRO_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("MOYRO_TEST_POSTGRES_DSN is not set")
	}
	ctx := emojisTestContext(t)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open emojis test admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping emojis test PostgreSQL: %v", err)
	}
	schemaName := "moyro_emojis_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create emojis test schema: %v", err)
	}
	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop emojis test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse emojis test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 4
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated emojis test pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated emojis test pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate emojis test schema: %v", err)
	}
	return db
}

func emojisTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	return ctx
}
