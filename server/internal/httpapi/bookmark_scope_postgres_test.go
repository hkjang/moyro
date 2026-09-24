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
	"github.com/hkjang/moyro/server/internal/bookmarks"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/store"
)

// The bookmark write handlers authorize the caller against the channel in the
// URL but used to address the row by id alone, so a member of any channel
// could patch or delete a bookmark belonging to a channel they cannot see. The
// path has to resolve inside the channel it names.
func TestBookmarkWritesRefuseABookmarkFromAnotherChannel(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	seedSidebarHandlerFixture(t, ctx, db)
	seedForeignBookmarkChannel(t, ctx, db)
	h := &handlers{bookmarks: bookmarks.New(db), channels: channels.New(db)}

	// user-a owns this row, but it lives in chan-beta, which user-a is not a
	// member of. Ownership is what the delete handler checks once it has the
	// row, so this is the strongest form of the hole.
	foreign := createTestBookmark(t, ctx, db, "bookmark-beta", "chan-beta", "user-a", "Beta bookmark")
	own := createTestBookmark(t, ctx, db, "bookmark-alpha", "chan-alpha", "user-a", "Alpha bookmark")

	rr := httptest.NewRecorder()
	h.patchChannelBookmark(rr, bookmarkRequest(t, http.MethodPut, "chan-alpha", foreign, `{"display_name":"stolen"}`))
	assertBookmarkNotFound(t, rr, "cross-channel patch")
	assertBookmarkRow(t, ctx, db, foreign, "Beta bookmark", 0)

	rr = httptest.NewRecorder()
	h.deleteChannelBookmark(rr, bookmarkRequest(t, http.MethodDelete, "chan-alpha", foreign, ""))
	assertBookmarkNotFound(t, rr, "cross-channel delete")
	assertBookmarkRow(t, ctx, db, foreign, "Beta bookmark", 0)

	// A bookmark addressed through its own channel keeps working unchanged.
	rr = httptest.NewRecorder()
	h.patchChannelBookmark(rr, bookmarkRequest(t, http.MethodPut, "chan-alpha", own, `{"display_name":"renamed"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("same-channel patch: status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	var patched bookmarks.Bookmark
	if err := json.NewDecoder(rr.Body).Decode(&patched); err != nil {
		t.Fatalf("decode patched bookmark: %v", err)
	}
	if patched.ID != own || patched.DisplayName != "renamed" || patched.ChannelID != "chan-alpha" {
		t.Fatalf("patched bookmark = %+v", patched)
	}

	// An id that exists nowhere keeps the same 404 contract.
	rr = httptest.NewRecorder()
	h.patchChannelBookmark(rr, bookmarkRequest(t, http.MethodPut, "chan-alpha", "no-such-bookmark", `{"display_name":"x"}`))
	assertBookmarkNotFound(t, rr, "unknown id patch")

	rr = httptest.NewRecorder()
	h.deleteChannelBookmark(rr, bookmarkRequest(t, http.MethodDelete, "chan-alpha", own, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("same-channel delete: status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	var deletedAt int64
	if err := db.Pool.QueryRow(ctx, `SELECT delete_at FROM channel_bookmarks WHERE id=$1`, own).Scan(&deletedAt); err != nil {
		t.Fatalf("read deleted bookmark: %v", err)
	}
	if deletedAt == 0 {
		t.Fatal("same-channel delete left delete_at at 0")
	}
}

func assertBookmarkNotFound(t *testing.T, rr *httptest.ResponseRecorder, label string) {
	t.Helper()
	if rr.Code != http.StatusNotFound {
		t.Fatalf("%s: status = %d, want 404 (body %s)", label, rr.Code, rr.Body.String())
	}
	var body apiError
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("%s: decode 404 body: %v", label, err)
	}
	if body.ID != "api.bookmark.not_found" {
		t.Fatalf("%s: 404 body = %+v, want the unchanged not_found id", label, body)
	}
}

func assertBookmarkRow(t *testing.T, ctx context.Context, db *store.DB, id, wantDisplayName string, wantDeleteAt int64) {
	t.Helper()
	var displayName string
	var deleteAt, updateAt, createAt int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT display_name, delete_at, update_at, create_at FROM channel_bookmarks WHERE id=$1
	`, id).Scan(&displayName, &deleteAt, &updateAt, &createAt); err != nil {
		t.Fatalf("read bookmark row %s: %v", id, err)
	}
	if displayName != wantDisplayName || deleteAt != wantDeleteAt || updateAt != createAt {
		t.Fatalf("bookmark row %s = (%q, delete_at %d, update_at %d, create_at %d), want (%q, %d, untouched update_at)",
			id, displayName, deleteAt, updateAt, createAt, wantDisplayName, wantDeleteAt)
	}
}

func seedForeignBookmarkChannel(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES ('chan-beta', 'team-main', 'P', 'Beta', 'beta', 1, 1)
	`); err != nil {
		t.Fatalf("seed foreign bookmark channel: %v", err)
	}
}

func createTestBookmark(t *testing.T, ctx context.Context, db *store.DB, id, channelID, ownerID, displayName string) string {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO channel_bookmarks
			(id, channel_id, owner_id, display_name, sort_order, link_url, type, create_at, update_at)
		VALUES ($1, $2, $3, $4, 1, 'https://example.test', 'link', 1, 1)
	`, id, channelID, ownerID, displayName); err != nil {
		t.Fatalf("seed bookmark %s: %v", id, err)
	}
	return id
}

func bookmarkRequest(t *testing.T, method, channelID, bookmarkID, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v4/channels/"+channelID+"/bookmarks/"+bookmarkID, strings.NewReader(body))
	ctx := context.WithValue(req.Context(), userIDKey, "user-a")
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("channelID", channelID)
	routeCtx.URLParams.Add("bookmarkID", bookmarkID)
	return req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, routeCtx))
}
