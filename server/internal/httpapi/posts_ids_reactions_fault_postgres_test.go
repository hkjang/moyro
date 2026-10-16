package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/reactions"
	"github.com/hkjang/moyro/server/internal/store"
)

// postsByIDsReactions hydrates a batch of posts' reactions and deliberately
// drops the posts the caller may not see, so that one unreadable post does not
// 403 the whole batch. That filter was implemented with three bare
// `continue`s, and the last of them covered something entirely different: a
// failure of reactions.ListForPost — a dropped connection, a server-side error
// while the rows are produced — left the post out of the map and still
// answered 200. An absent key is how this endpoint says "nothing to show
// here", so an outage arrived at the client as "that post has no reactions"
// and the emoji a user had just added disappeared from the thread.
//
// Only the ListForPost branch is changed. The other two `continue`s are the
// visibility filter itself (an invisible or deleted post is genuinely not an
// error) and are pinned below as preserved behaviour; separating a DB fault
// from a legitimate miss in *those* branches needs a policy decision about
// what a batch endpoint owes a caller whose membership lookup failed, which is
// a different change from this one.
//
// The 500 is the one listReactions already owns for the identical call
// (handlers.go:2501-2504, api.reaction.list.app_error): no new error id and no
// new status code enter the API surface.
func TestPostsByIDsReactionsFaultIsNotAnEmptyReactionSet(t *testing.T) {
	// A dedicated database: this test renames the reactions table to inject a
	// fault, so sharing a schema would cross-contaminate sibling tests.
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	seedSidebarHandlerFixture(t, ctx, db)
	// chan-hidden exists and holds a reacted post, but user-a is not a member:
	// that post must stay filtered out rather than 403 the batch.
	//
	// post-boom is the post the fault below detonates on. post-bare carries no
	// reactions at all, which is the answer a faulting post was previously
	// indistinguishable from.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES ('chan-hidden', 'team-main', 'O', 'Hidden', 'hidden', 1, 1);
		INSERT INTO posts (id, channel_id, user_id, message, create_at, update_at, delete_at)
		VALUES ('post-quiet',  'chan-alpha',  'user-a', 'quiet',  1000, 1000, 0),
		       ('post-boom',   'chan-alpha',  'user-a', 'boom',   2000, 2000, 0),
		       ('post-bare',   'chan-alpha',  'user-a', 'bare',   3000, 3000, 0),
		       ('post-hidden', 'chan-hidden', 'user-a', 'hidden', 4000, 4000, 0);
		INSERT INTO reactions (user_id, post_id, emoji_name, create_at)
		VALUES ('user-a', 'post-quiet',  'thumbsup', 1100),
		       ('user-a', 'post-boom',   'tada',     2100),
		       ('user-a', 'post-hidden', 'eyes',     4100)
	`); err != nil {
		t.Fatalf("seed reaction fixture: %v", err)
	}

	h := &handlers{reactions: reactions.New(db), channels: channels.New(db)}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor := r.Header.Get("X-Test-Actor")
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey, actor)))
		})
	})
	// Pattern copied verbatim from router.go:773.
	router.Post("/posts/ids/reactions", h.postsByIDsReactions)

	call := func(t *testing.T, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/posts/ids/reactions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-Actor", "user-a")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}
	assertOK := func(t *testing.T, rr *httptest.ResponseRecorder, want string) {
		t.Helper()
		if rr.Code != 200 {
			t.Fatalf("status = %d, want 200 (body %s)", rr.Code, strings.TrimSpace(rr.Body.String()))
		}
		if got := strings.TrimSpace(rr.Body.String()); got != want {
			t.Fatalf("body = %s\nwant %s", got, want)
		}
	}
	// assertFailed wants the status, the error id, and — the part that
	// separates this defect from a miscoded status — an error envelope rather
	// than the reaction map a legitimately empty post returns.
	assertFailed := func(t *testing.T, rr *httptest.ResponseRecorder) {
		t.Helper()
		if rr.Code != 500 {
			t.Fatalf("status = %d, want 500 (body %s)", rr.Code, strings.TrimSpace(rr.Body.String()))
		}
		var got apiError
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode error envelope %q: %v", rr.Body.String(), err)
		}
		if got.ID != "api.reaction.list.app_error" {
			t.Fatalf("error id = %q, want %q (body %s)", got.ID, "api.reaction.list.app_error",
				strings.TrimSpace(rr.Body.String()))
		}
	}

	// ---- preserved behaviour -------------------------------------------
	// Pinned as literals so the new error path cannot quietly reshape them.
	// Note post-bare: a visible post with no reactions answers with an empty
	// array, and that is the answer a faulting post used to be confused with.
	t.Run("a batch answers with the reactions and shape it always did", func(t *testing.T) {
		assertOK(t, call(t, `{"ids":["post-quiet","post-bare"]}`),
			`{"post-bare":[],"post-quiet":[{"user_id":"user-a","post_id":"post-quiet","emoji_name":"thumbsup","create_at":1100}]}`)
	})

	t.Run("a bare array body is accepted like the object form", func(t *testing.T) {
		assertOK(t, call(t, `["post-quiet"]`),
			`{"post-quiet":[{"user_id":"user-a","post_id":"post-quiet","emoji_name":"thumbsup","create_at":1100}]}`)
	})

	t.Run("a post in a channel the caller cannot see is filtered out, not refused", func(t *testing.T) {
		assertOK(t, call(t, `{"ids":["post-quiet","post-hidden"]}`),
			`{"post-quiet":[{"user_id":"user-a","post_id":"post-quiet","emoji_name":"thumbsup","create_at":1100}]}`)
	})

	t.Run("an unknown post id is filtered out, not refused", func(t *testing.T) {
		assertOK(t, call(t, `{"ids":["post-quiet","post-does-not-exist"]}`),
			`{"post-quiet":[{"user_id":"user-a","post_id":"post-quiet","emoji_name":"thumbsup","create_at":1100}]}`)
	})

	t.Run("an empty id list short-circuits to 200 before any query", func(t *testing.T) {
		assertOK(t, call(t, `{"ids":[]}`), `{}`)
	})

	t.Run("an undecodable body is still a 400", func(t *testing.T) {
		rr := call(t, `{"ids":`)
		if rr.Code != 400 {
			t.Fatalf("status = %d, want 400 (body %s)", rr.Code, strings.TrimSpace(rr.Body.String()))
		}
	})

	// ---- the fault: the server raises while the reaction rows are read ----
	// reactions becomes a view that divides by zero on post-boom's row. posts
	// and channel_members are untouched, so ChannelForPost and IsMember still
	// succeed and ListForPost is the only call that fails — which is what
	// makes this a test of that branch alone.
	if _, err := db.Pool.Exec(ctx, `
		ALTER TABLE reactions RENAME TO reactions_real;
		CREATE VIEW reactions AS
			SELECT user_id, post_id, emoji_name,
			       create_at / (CASE WHEN post_id = 'post-boom' THEN 0 ELSE 1 END) AS create_at
			FROM reactions_real
	`); err != nil {
		t.Fatalf("install reaction read fault: %v", err)
	}

	t.Run("a failed reaction read is a server error, not a post without reactions", func(t *testing.T) {
		assertFailed(t, call(t, `{"ids":["post-boom"]}`))
	})

	// The sharpest form of the defect: the faulting post simply went missing
	// from an otherwise successful batch, so the response was byte-identical
	// to one where post-boom had never been reacted to.
	t.Run("a failed reaction read refuses the batch instead of silently dropping the post", func(t *testing.T) {
		assertFailed(t, call(t, `{"ids":["post-quiet","post-boom"]}`))
	})

	// A healthy post in the same schema still answers, so the two subtests
	// above fail because of the fault rather than because the view broke every
	// read.
	t.Run("an unaffected post still answers while the fault is installed", func(t *testing.T) {
		assertOK(t, call(t, `{"ids":["post-quiet"]}`),
			`{"post-quiet":[{"user_id":"user-a","post_id":"post-quiet","emoji_name":"thumbsup","create_at":1100}]}`)
	})
}
