package reactions

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

// TestReactionsAreIdempotentPerUserAndEmoji pins the contract the reaction
// chips depend on: adding the same reaction twice is one row with its
// original timestamp, removal is exact, and listing keeps insertion order.
func TestReactionsAreIdempotentPerUserAndEmoji(t *testing.T) {
	db := newReactionsTestDB(t)
	ctx := reactionsTestContext(t)
	seedReactionsFixture(t, ctx, db)
	service := New(db)

	first, err := service.Add(ctx, "user-a", "post-root", "+1")
	if err != nil {
		t.Fatalf("add reaction: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	again, err := service.Add(ctx, "user-a", "post-root", "+1")
	if err != nil {
		t.Fatalf("re-add reaction: %v", err)
	}
	if again.CreateAt != first.CreateAt {
		t.Fatalf("duplicate add changed create_at: %d → %d", first.CreateAt, again.CreateAt)
	}
	if _, err := service.Add(ctx, "user-b", "post-root", "+1"); err != nil {
		t.Fatalf("second user reaction: %v", err)
	}
	if _, err := service.Add(ctx, "user-a", "post-root", "eyes"); err != nil {
		t.Fatalf("second emoji: %v", err)
	}

	list, err := service.ListForPost(ctx, "post-root")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("reactions = %#v, want 3 distinct rows", list)
	}
	if list[0].UserID != "user-a" || list[0].EmojiName != "+1" {
		t.Fatalf("list order lost the first reaction: %#v", list)
	}

	// Removal is exact: another user's identical emoji survives.
	if err := service.Remove(ctx, "user-a", "post-root", "+1"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	list, err = service.ListForPost(ctx, "post-root")
	if err != nil || len(list) != 2 {
		t.Fatalf("after remove = %#v, %v", list, err)
	}
	for _, r := range list {
		if r.UserID == "user-a" && r.EmojiName == "+1" {
			t.Fatalf("removed reaction still listed: %#v", list)
		}
	}
	// Removing a reaction that does not exist is a no-op, not an error.
	if err := service.Remove(ctx, "user-c", "post-root", "+1"); err != nil {
		t.Fatalf("remove missing: %v", err)
	}

	// Empty list is a slice, never nil, so the JSON is [] not null.
	empty, err := service.ListForPost(ctx, "post-reply")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty list = %#v, %v", empty, err)
	}
}

// TestChannelForPostIgnoresDeletedPosts keeps reaction authorization from
// resolving a channel for a post that no longer exists to readers.
func TestChannelForPostIgnoresDeletedPosts(t *testing.T) {
	db := newReactionsTestDB(t)
	ctx := reactionsTestContext(t)
	seedReactionsFixture(t, ctx, db)
	service := New(db)

	if channel, err := service.ChannelForPost(ctx, "post-root"); err != nil || channel != "channel-1" {
		t.Fatalf("channel for live post = %q, %v", channel, err)
	}
	if _, err := service.ChannelForPost(ctx, "post-gone"); err == nil {
		t.Fatal("deleted post resolved to a channel")
	}
}

func seedReactionsFixture(t *testing.T, ctx context.Context, db *store.DB) {
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
		t.Fatalf("seed reactions fixture: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE posts SET delete_at=4000 WHERE id='post-gone'`); err != nil {
		t.Fatalf("soft-delete fixture post: %v", err)
	}
}

func newReactionsTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("MOYRO_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("MOYRO_TEST_POSTGRES_DSN is not set")
	}
	ctx := reactionsTestContext(t)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open reactions test admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping reactions test PostgreSQL: %v", err)
	}
	schemaName := "moyro_reactions_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create reactions test schema: %v", err)
	}
	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop reactions test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse reactions test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 4
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated reactions test pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated reactions test pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate reactions test schema: %v", err)
	}
	return db
}

func reactionsTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	return ctx
}
