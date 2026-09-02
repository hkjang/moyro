package threads

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

// TestThreadReadStateRoundTrip pins follow/read semantics: mark-read on an
// unfollowed thread creates the row without following it, set-unread rewinds
// to just before the post, and team-wide read touches only that team.
func TestThreadReadStateRoundTrip(t *testing.T) {
	db := newThreadsTestDB(t)
	ctx := threadsTestContext(t)
	seedThreadsFixture(t, ctx, db)
	service := New(db)

	if _, err := service.Get(ctx, "user-a", "post-root"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing membership error = %v, want ErrNotFound", err)
	}

	// Mark-read on a thread the user only browsed creates the row, unfollowed.
	viewed, err := service.MarkRead(ctx, "user-a", "team-1", "post-root", 5_000)
	if err != nil || viewed != 5_000 {
		t.Fatalf("mark read = %d, %v", viewed, err)
	}
	m, err := service.Get(ctx, "user-a", "post-root")
	if err != nil {
		t.Fatalf("get after mark read: %v", err)
	}
	if m.Following || m.LastViewedAt != 5_000 || m.TeamID != "team-1" {
		t.Fatalf("membership after mark read = %#v", m)
	}

	// Following toggles without disturbing the read marker.
	if err := service.SetFollowing(ctx, "user-a", "team-1", "post-root", true); err != nil {
		t.Fatalf("follow: %v", err)
	}
	m, _ = service.Get(ctx, "user-a", "post-root")
	if !m.Following || m.LastViewedAt != 5_000 {
		t.Fatalf("membership after follow = %#v", m)
	}

	// Set-unread rewinds to just before the reply so it becomes the first
	// unread row, matching the channel-level contract.
	boundary, err := service.MarkUnreadFromPost(ctx, "user-a", "team-1", "post-root", 2_000)
	if err != nil || boundary != 1_999 {
		t.Fatalf("set unread = %d, %v", boundary, err)
	}
	m, _ = service.Get(ctx, "user-a", "post-root")
	if m.LastViewedAt != 1_999 || !m.Following {
		t.Fatalf("membership after set unread = %#v", m)
	}

	// Seed unread counters and a membership in another team, then read all
	// in team-1: only team-1 rows change.
	if _, err := db.Pool.Exec(ctx, `
		UPDATE thread_memberships SET unread_replies=2, unread_mentions=1 WHERE user_id='user-a' AND root_id='post-root';
		INSERT INTO thread_memberships (user_id, team_id, root_id, last_viewed_at, unread_replies)
		VALUES ('user-a', 'team-2', 'root-elsewhere', 10, 4)
	`); err != nil {
		t.Fatalf("seed counters: %v", err)
	}
	now, err := service.MarkAllReadInTeam(ctx, "user-a", "team-1")
	if err != nil || now <= 0 {
		t.Fatalf("mark all read = %d, %v", now, err)
	}
	m, _ = service.Get(ctx, "user-a", "post-root")
	if m.LastViewedAt != now || m.UnreadReplies != 0 || m.UnreadMentions != 0 {
		t.Fatalf("team-1 membership after read-all = %#v", m)
	}
	other, _ := service.Get(ctx, "user-a", "root-elsewhere")
	if other.LastViewedAt != 10 || other.UnreadReplies != 4 {
		t.Fatalf("team-2 membership touched by team-1 read-all: %#v", other)
	}

	// A mark-read for a stale timestamp is not "later" than now; the service
	// applies the caller's value verbatim, so a zero requests the clock.
	clock, err := service.MarkRead(ctx, "user-b", "team-1", "post-root", 0)
	if err != nil || clock < now {
		t.Fatalf("mark read with zero = %d, %v (now %d)", clock, err, now)
	}
}

func seedThreadsFixture(t *testing.T, ctx context.Context, db *store.DB) {
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
		t.Fatalf("seed threads fixture: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE posts SET delete_at=4000 WHERE id='post-gone'`); err != nil {
		t.Fatalf("soft-delete fixture post: %v", err)
	}
}

func newThreadsTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("MOYRO_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("MOYRO_TEST_POSTGRES_DSN is not set")
	}
	ctx := threadsTestContext(t)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open threads test admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping threads test PostgreSQL: %v", err)
	}
	schemaName := "moyro_threads_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create threads test schema: %v", err)
	}
	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop threads test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse threads test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 4
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated threads test pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated threads test pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate threads test schema: %v", err)
	}
	return db
}

func threadsTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	return ctx
}
