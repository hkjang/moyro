package posts

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

const postsTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

// TestSearchFilterCombinationsScopeToCallerMembership pins the search contract:
// every filter narrows correctly, filters compose, TotalHits stays unpaginated,
// and a non-member never sees a channel's posts regardless of filter shape.
func TestSearchFilterCombinationsScopeToCallerMembership(t *testing.T) {
	db := newPostsTestDB(t)
	ctx := postsTestContext(t)
	seedPostsFixture(t, ctx, db)
	service := New(db)

	alpha := createPostAt(t, ctx, db, service, "channel-general", "user-author", "", "release alpha notes", 1_000)
	beta := createPostAt(t, ctx, db, service, "channel-general", "user-other", "", "release beta notes", 2_000)
	linked := createPostAt(t, ctx, db, service, "channel-general", "user-author", "", "release notes at https://example.test/x", 3_000)
	withFile := createPostAt(t, ctx, db, service, "channel-other", "user-author", "", "release notes attachment", 4_000)
	if err := service.UpdateFileIDs(ctx, withFile.ID, []string{"file-1"}); err != nil {
		t.Fatalf("attach file: %v", err)
	}
	// A post the caller must never see: they are not a member of this channel.
	createPostAt(t, ctx, db, service, "channel-private", "user-other", "", "release notes secret", 5_000)

	all, err := service.Search(ctx, "user-reader", "team-main", "release notes", SearchFilters{}, 0, 20)
	if err != nil {
		t.Fatalf("search all: %v", err)
	}
	if got := searchIDs(all); len(got) != 4 || all.TotalHits != 4 {
		t.Fatalf("unfiltered search = %v (total %d)", got, all.TotalHits)
	}
	if containsMessage(all, "release notes secret") {
		t.Fatalf("search leaked a non-member channel post")
	}

	byUser, err := service.Search(ctx, "user-reader", "team-main", "release notes", SearchFilters{FromUserID: "user-other"}, 0, 20)
	if err != nil {
		t.Fatalf("search from-user: %v", err)
	}
	if got := searchIDs(byUser); len(got) != 1 || got[0] != beta.ID {
		t.Fatalf("from-user search = %v", got)
	}

	byChannel, err := service.Search(ctx, "user-reader", "team-main", "release notes", SearchFilters{InChannelID: "channel-other"}, 0, 20)
	if err != nil {
		t.Fatalf("search in-channel: %v", err)
	}
	if got := searchIDs(byChannel); len(got) != 1 || got[0] != withFile.ID {
		t.Fatalf("in-channel search = %v", got)
	}

	// After is inclusive, Before is exclusive — the boundary semantics callers
	// rely on to pass "next midnight" as Before without double-counting.
	window, err := service.Search(ctx, "user-reader", "team-main", "release notes", SearchFilters{After: 2_000, Before: 4_000}, 0, 20)
	if err != nil {
		t.Fatalf("search window: %v", err)
	}
	if got := searchIDs(window); len(got) != 2 || !containsString(got, beta.ID) || !containsString(got, linked.ID) {
		t.Fatalf("time-window search = %v", got)
	}

	hasFile, err := service.Search(ctx, "user-reader", "team-main", "release notes", SearchFilters{HasFile: true}, 0, 20)
	if err != nil {
		t.Fatalf("search has-file: %v", err)
	}
	if got := searchIDs(hasFile); len(got) != 1 || got[0] != withFile.ID {
		t.Fatalf("has-file search = %v", got)
	}

	hasLink, err := service.Search(ctx, "user-reader", "team-main", "release notes", SearchFilters{HasLink: true}, 0, 20)
	if err != nil {
		t.Fatalf("search has-link: %v", err)
	}
	if got := searchIDs(hasLink); len(got) != 1 || got[0] != linked.ID {
		t.Fatalf("has-link search = %v", got)
	}

	// Filters must AND together, not replace one another.
	composed, err := service.Search(ctx, "user-reader", "team-main", "release notes", SearchFilters{
		FromUserID: "user-author", InChannelID: "channel-general", HasLink: true,
	}, 0, 20)
	if err != nil {
		t.Fatalf("search composed: %v", err)
	}
	if got := searchIDs(composed); len(got) != 1 || got[0] != linked.ID {
		t.Fatalf("composed search = %v", got)
	}

	// TotalHits reports the unpaginated count so pagination UIs stay correct.
	paged, err := service.Search(ctx, "user-reader", "team-main", "release notes", SearchFilters{}, 0, 2)
	if err != nil {
		t.Fatalf("search paged: %v", err)
	}
	if len(paged.Order) != 2 || paged.TotalHits != 4 || paged.Page != 0 {
		t.Fatalf("first page = %d rows, total %d, page %d", len(paged.Order), paged.TotalHits, paged.Page)
	}
	secondPage, err := service.Search(ctx, "user-reader", "team-main", "release notes", SearchFilters{}, 1, 2)
	if err != nil {
		t.Fatalf("search second page: %v", err)
	}
	if len(secondPage.Order) != 2 || secondPage.TotalHits != 4 || secondPage.Page != 1 {
		t.Fatalf("second page = %d rows, total %d, page %d", len(secondPage.Order), secondPage.TotalHits, secondPage.Page)
	}
	for _, id := range secondPage.Order {
		if containsString(paged.Order, id) {
			t.Fatalf("page 1 and 2 overlap on %s", id)
		}
	}

	// A soft-deleted post leaves the index immediately.
	if _, err := service.Delete(ctx, alpha.ID, "user-author"); err != nil {
		t.Fatalf("delete alpha: %v", err)
	}
	afterDelete, err := service.Search(ctx, "user-reader", "team-main", "release notes", SearchFilters{}, 0, 20)
	if err != nil {
		t.Fatalf("search after delete: %v", err)
	}
	if afterDelete.TotalHits != 3 || containsString(searchIDs(afterDelete), alpha.ID) {
		t.Fatalf("deleted post still searchable: %v (total %d)", searchIDs(afterDelete), afterDelete.TotalHits)
	}
}

// TestThreadLifecycleKeepsRepliesWithTheirRoot covers the reply-integrity rules
// that channel-scoped authorization depends on: a reply must attach to a live
// root in the same channel, and moving any post in a thread moves the whole
// thread so a reply can never be split from its root.
func TestThreadLifecycleKeepsRepliesWithTheirRoot(t *testing.T) {
	db := newPostsTestDB(t)
	ctx := postsTestContext(t)
	seedPostsFixture(t, ctx, db)
	service := New(db)

	root, err := service.Create(ctx, "channel-general", "user-author", "", "thread root", nil, nil)
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	reply, err := service.Create(ctx, "channel-general", "user-other", root.ID, "thread reply", nil, nil)
	if err != nil {
		t.Fatalf("create reply: %v", err)
	}

	// A reply may not point at a root in a different channel.
	if _, err := service.Create(ctx, "channel-other", "user-author", root.ID, "cross-channel reply", nil, nil); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("cross-channel reply error = %v, want ErrInvalidRoot", err)
	}
	// A reply may not nest under another reply.
	if _, err := service.Create(ctx, "channel-general", "user-author", reply.ID, "nested reply", nil, nil); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("nested reply error = %v, want ErrInvalidRoot", err)
	}
	// A reply may not point at an unknown root.
	if _, err := service.Create(ctx, "channel-general", "user-author", uuid.NewString(), "orphan reply", nil, nil); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("orphan reply error = %v, want ErrInvalidRoot", err)
	}

	// Moving the thread by its *reply* id must still relocate the root too.
	moved, err := service.MoveThread(ctx, reply.ID, "channel-other")
	if err != nil {
		t.Fatalf("move thread: %v", err)
	}
	if moved != 2 {
		t.Fatalf("moved rows = %d, want 2", moved)
	}
	for _, id := range []string{root.ID, reply.ID} {
		p, err := service.Get(ctx, id)
		if err != nil || p == nil {
			t.Fatalf("get %s after move: %#v, %v", id, p, err)
		}
		if p.ChannelID != "channel-other" {
			t.Fatalf("post %s stayed in %s after move", id, p.ChannelID)
		}
	}
	// Moving to the channel the thread already occupies is a no-op.
	if again, err := service.MoveThread(ctx, root.ID, "channel-other"); err != nil || again != 0 {
		t.Fatalf("idempotent move = %d, %v", again, err)
	}
	// Create ordering is preserved so the destination renders the same thread.
	if after, err := service.Get(ctx, reply.ID); err != nil || after.CreateAt < root.CreateAt {
		t.Fatalf("reply create_at not preserved: %#v, %v", after, err)
	}

	// A deleted root must reject new replies while leaving existing ones intact.
	if ok, err := service.Delete(ctx, root.ID, "user-author"); err != nil || !ok {
		t.Fatalf("delete root = %v, %v", ok, err)
	}
	if _, err := service.Create(ctx, "channel-other", "user-author", root.ID, "reply to dead root", nil, nil); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("reply to deleted root error = %v, want ErrInvalidRoot", err)
	}
	survivor, err := service.Get(ctx, reply.ID)
	if err != nil || survivor == nil || survivor.DeleteAt != 0 {
		t.Fatalf("reply removed with its root: %#v, %v", survivor, err)
	}

	// Restore is admin-mediated: no ownership filter, and idempotent.
	if ok, err := service.Restore(ctx, root.ID); err != nil || !ok {
		t.Fatalf("restore root = %v, %v", ok, err)
	}
	if ok, err := service.Restore(ctx, root.ID); err != nil || ok {
		t.Fatalf("second restore = %v, %v (want false, nil)", ok, err)
	}
	if restored, err := service.Get(ctx, root.ID); err != nil || restored.DeleteAt != 0 {
		t.Fatalf("root not restored: %#v, %v", restored, err)
	}
}

// TestDeleteAndUpdateEnforceAuthorship pins the ownership boundary: only the
// author can edit or delete their own post, and a miss is reported as a clean
// no-op rather than an error so handlers can map it to 403/404 themselves.
func TestDeleteAndUpdateEnforceAuthorship(t *testing.T) {
	db := newPostsTestDB(t)
	ctx := postsTestContext(t)
	seedPostsFixture(t, ctx, db)
	service := New(db)

	post, err := service.Create(ctx, "channel-general", "user-author", "", "original body", map[string]any{"keep": "me"}, nil)
	if err != nil {
		t.Fatalf("create post: %v", err)
	}

	if updated, err := service.Update(ctx, post.ID, "user-other", "hijacked", nil); err != nil || updated != nil {
		t.Fatalf("non-author update = %#v, %v (want nil, nil)", updated, err)
	}
	if ok, err := service.Delete(ctx, post.ID, "user-other"); err != nil || ok {
		t.Fatalf("non-author delete = %v, %v (want false, nil)", ok, err)
	}
	if untouched, err := service.Get(ctx, post.ID); err != nil || untouched.Message != "original body" {
		t.Fatalf("post mutated by non-author: %#v, %v", untouched, err)
	}

	updated, err := service.Update(ctx, post.ID, "user-author", "edited body", map[string]any{"keep": "you"})
	if err != nil || updated == nil {
		t.Fatalf("author update = %#v, %v", updated, err)
	}
	if updated.Message != "edited body" || updated.Props["keep"] != "you" {
		t.Fatalf("update did not apply: %#v", updated)
	}
	if updated.UpdateAt < updated.CreateAt {
		t.Fatalf("update_at not advanced: %#v", updated)
	}

	if ok, err := service.Delete(ctx, post.ID, "user-author"); err != nil || !ok {
		t.Fatalf("author delete = %v, %v", ok, err)
	}
	// A second delete is a no-op, and an edit of a deleted post must not revive it.
	if ok, err := service.Delete(ctx, post.ID, "user-author"); err != nil || ok {
		t.Fatalf("double delete = %v, %v (want false, nil)", ok, err)
	}
	if revived, err := service.Update(ctx, post.ID, "user-author", "revived", nil); err != nil || revived != nil {
		t.Fatalf("update of deleted post = %#v, %v (want nil, nil)", revived, err)
	}
}

func searchIDs(result *SearchResult) []string {
	if result == nil {
		return nil
	}
	return append([]string(nil), result.Order...)
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func containsMessage(result *SearchResult, message string) bool {
	for _, p := range result.Posts {
		if p.Message == message {
			return true
		}
	}
	return false
}

// createPostAt writes a post through the service (so reply validation and the
// tsv trigger run) then rewrites create_at, which the search-window assertions
// need to be deterministic.
func createPostAt(t *testing.T, ctx context.Context, db *store.DB, service *Service, channelID, userID, rootID, message string, createAt int64) *Post {
	t.Helper()
	p, err := service.Create(ctx, channelID, userID, rootID, message, nil, nil)
	if err != nil {
		t.Fatalf("create post %q: %v", message, err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE posts SET create_at=$1, update_at=$1 WHERE id=$2`, createAt, p.ID); err != nil {
		t.Fatalf("pin create_at for %q: %v", message, err)
	}
	p.CreateAt = createAt
	p.UpdateAt = createAt
	return p
}

func seedPostsFixture(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES
			('user-author', 'user-author', 'author@example.test', 'hash', 'system_user', 1, 1),
			('user-other',  'user-other',  'other@example.test',  'hash', 'system_user', 1, 1),
			('user-reader', 'user-reader', 'reader@example.test', 'hash', 'system_user', 1, 1);
		INSERT INTO teams (id, name, display_name, type, create_at, update_at)
		VALUES ('team-main', 'team-main', 'Main Team', 'O', 1, 1);
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES
			('team-main', 'user-author', 'team_user', 1),
			('team-main', 'user-other', 'team_user', 1),
			('team-main', 'user-reader', 'team_user', 1);
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES
			('channel-general', 'team-main', 'O', 'General', 'general', 1, 1),
			('channel-other',   'team-main', 'O', 'Other',   'other',   1, 1),
			('channel-private', 'team-main', 'P', 'Private', 'private', 1, 1);
		INSERT INTO channel_members (channel_id, user_id, roles, create_at)
		VALUES
			('channel-general', 'user-author', 'channel_user', 1),
			('channel-general', 'user-other', 'channel_user', 1),
			('channel-general', 'user-reader', 'channel_user', 1),
			('channel-other',   'user-author', 'channel_user', 1),
			('channel-other',   'user-reader', 'channel_user', 1),
			('channel-private', 'user-other', 'channel_user', 1)
	`); err != nil {
		t.Fatalf("seed posts fixture: %v", err)
	}
}

func newPostsTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(postsTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", postsTestPostgresDSN)
	}
	ctx := postsTestContext(t)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open posts test admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping posts test PostgreSQL: %v", err)
	}

	schemaName := "moyro_posts_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create posts test schema: %v", err)
	}

	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop posts test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse posts test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 4
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated posts test pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated posts test pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate posts test schema: %v", err)
	}
	return db
}

func postsTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	return ctx
}
