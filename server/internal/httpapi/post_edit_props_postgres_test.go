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
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/hkjang/moyro/server/internal/ws"
)

// Post creation refuses client-supplied provenance: every transport builds a
// postcommand.Command and the service drops the server-owned keys before the
// INSERT. Both edit routes handed posts.Update whatever props the client sent,
// so the author of any post could re-introduce on edit exactly the keys the
// create path had just removed — override_username to render the post under
// another name, from_plugin/plugin_id to forge plugin attribution,
// _moyro_post_type to pass an ordinary post off as a system message, and
// approval_request_id to squat the partial unique index the approval worker
// dedupes on. The two routes must agree, and must agree with create.
var reservedPropForgery = map[string]any{
	"approval_request_id": "req-forged",
	"scheduled_post_id":   "sched-forged",
	"from_mcp":            true,
	"from_webhook":        "true",
	"webhook_depth":       float64(9),
	"override_username":   "admin",
	"override_icon_url":   "http://attacker.test/icon.png",
	"from_me_command":     true,
	"from_plugin":         true,
	"plugin_id":           "evil-plugin",
	"_moyro_post_type":    "system_join_channel",
}

func TestPostEditRoutesDropClientSuppliedReservedProps(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	seedSidebarHandlerFixture(t, ctx, db)
	h, client := newPostEditHandlers(t, ctx, db)
	service := posts.New(db)

	for _, route := range postEditRoutes(h) {
		post, err := service.Create(ctx, "chan-alpha", "user-a", "", "원본", map[string]any{"color": "blue"}, nil)
		if err != nil {
			t.Fatalf("%s: create post: %v", route.name, err)
		}
		body := map[string]any{"message": "수정본", "props": map[string]any{"color": "red"}}
		for key, value := range reservedPropForgery {
			body["props"].(map[string]any)[key] = value
		}

		rr := httptest.NewRecorder()
		route.call(rr, postEditRequest(t, post.ID, body))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 (body %s)", route.name, rr.Code, rr.Body.String())
		}

		var answered posts.Post
		if err := json.NewDecoder(rr.Body).Decode(&answered); err != nil {
			t.Fatalf("%s: decode response: %v", route.name, err)
		}
		assertNoForgedProps(t, route.name+" response", answered.Props)

		stored, err := service.Get(ctx, post.ID)
		if err != nil {
			t.Fatalf("%s: re-read post: %v", route.name, err)
		}
		assertNoForgedProps(t, route.name+" stored row", stored.Props)
		if stored.Type != "" {
			t.Errorf("%s: stored post type = %q, want the forged _moyro_post_type to have no effect", route.name, stored.Type)
		}

		assertNoForgedProps(t, route.name+" post_edited event", awaitPostEditedProps(t, client, route.name))
	}
}

func TestPostEditRoutesKeepServerOwnedProps(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	seedSidebarHandlerFixture(t, ctx, db)
	h, client := newPostEditHandlers(t, ctx, db)
	service := posts.New(db)

	// Provenance a trusted adapter stamped on the row. An edit must not be
	// able to rewrite it either — dropping the client's copy of a reserved
	// key has to leave the stored one standing, not erase it.
	trusted := map[string]any{
		"from_webhook":      "true",
		"override_username": "bot",
		"_moyro_post_type":  "custom_webhook",
		"color":             "blue",
	}
	for _, route := range postEditRoutes(h) {
		post, err := service.Create(ctx, "chan-alpha", "user-a", "", "원본", trusted, nil)
		if err != nil {
			t.Fatalf("%s: create post: %v", route.name, err)
		}
		rr := httptest.NewRecorder()
		route.call(rr, postEditRequest(t, post.ID, map[string]any{
			"message": "수정본",
			"props":   map[string]any{"color": "red", "override_username": "admin"},
		}))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 (body %s)", route.name, rr.Code, rr.Body.String())
		}
		stored, err := service.Get(ctx, post.ID)
		if err != nil {
			t.Fatalf("%s: re-read post: %v", route.name, err)
		}
		for key, want := range map[string]any{
			"from_webhook":      "true",
			"override_username": "bot",
			"_moyro_post_type":  "custom_webhook",
			"color":             "red",
		} {
			if got := stored.Props[key]; got != want {
				t.Errorf("%s: stored props[%q] = %#v, want %#v", route.name, key, got, want)
			}
		}
		if stored.Type != "custom_webhook" {
			t.Errorf("%s: stored post type = %q, want custom_webhook", route.name, stored.Type)
		}
		if stored.Message != "수정본" {
			t.Errorf("%s: stored message = %q, want the edit to have applied", route.name, stored.Message)
		}
		awaitPostEditedProps(t, client, route.name)
	}
}

// patchPost omits props entirely when the client sends no props key. That
// path feeds the stored props straight back into the update, so it must stay
// a no-op rather than laundering the stored reserved keys away.
func TestPatchPostWithoutPropsKeepsStoredProps(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	seedSidebarHandlerFixture(t, ctx, db)
	h, client := newPostEditHandlers(t, ctx, db)
	service := posts.New(db)

	post, err := service.Create(ctx, "chan-alpha", "user-a", "", "원본",
		map[string]any{"from_plugin": true, "plugin_id": "greeter", "color": "blue"}, nil)
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	rr := httptest.NewRecorder()
	h.patchPost(rr, postEditRequest(t, post.ID, map[string]any{"message": "수정본"}))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	stored, err := service.Get(ctx, post.ID)
	if err != nil {
		t.Fatalf("re-read post: %v", err)
	}
	for key, want := range map[string]any{"from_plugin": true, "plugin_id": "greeter", "color": "blue"} {
		if got := stored.Props[key]; got != want {
			t.Errorf("stored props[%q] = %#v, want %#v", key, got, want)
		}
	}
	awaitPostEditedProps(t, client, "patch without props")
}

func assertNoForgedProps(t *testing.T, where string, props map[string]any) {
	t.Helper()
	if got := props["color"]; got != "red" {
		t.Errorf("%s: props[\"color\"] = %#v, want the client's own key to survive as \"red\"", where, got)
	}
	for key := range reservedPropForgery {
		if got, ok := props[key]; ok {
			t.Errorf("%s: props[%q] = %#v, want the server-owned key to be dropped", where, key, got)
		}
	}
}

type postEditRoute struct {
	name string
	call func(http.ResponseWriter, *http.Request)
}

func postEditRoutes(h *handlers) []postEditRoute {
	return []postEditRoute{
		{name: "PUT /posts/{postID}", call: h.updatePost},
		{name: "PUT /posts/{postID}/patch", call: h.patchPost},
	}
}

// postEditRequest builds one request body that drives either edit route:
// updatePostReq and postPatchReq read the same message/props field names.
func postEditRequest(t *testing.T, postID string, body map[string]any) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v4/posts/"+postID, strings.NewReader(string(raw)))
	reqCtx := context.WithValue(req.Context(), userIDKey, "user-a")
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("postID", postID)
	return req.WithContext(context.WithValue(reqCtx, chi.RouteCtxKey, routeCtx))
}

// awaitPostEditedProps returns the props carried by the next post_edited
// event. The two routes disagree on whether data.post is a JSON string or an
// object, so read both shapes rather than pinning one route's encoding.
func awaitPostEditedProps(t *testing.T, client *ws.Client, route string) map[string]any {
	t.Helper()
	select {
	case raw := <-client.Send:
		var envelope struct {
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("%s: decode websocket event %s: %v", route, raw, err)
		}
		if envelope.Event != "post_edited" {
			t.Fatalf("%s: websocket event = %q, want post_edited", route, envelope.Event)
		}
		encoded, err := json.Marshal(envelope.Data["post"])
		if err != nil {
			t.Fatalf("%s: re-encode event post: %v", route, err)
		}
		var quoted string
		if json.Unmarshal(encoded, &quoted) == nil {
			encoded = []byte(quoted)
		}
		var post posts.Post
		if err := json.Unmarshal(encoded, &post); err != nil {
			t.Fatalf("%s: decode event post %s: %v", route, encoded, err)
		}
		return post.Props
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: timed out waiting for post_edited", route)
		return nil
	}
}

func newPostEditHandlers(t *testing.T, ctx context.Context, db *store.DB) (*handlers, *ws.Client) {
	t.Helper()
	hub := ws.NewHub()
	hub.SetAudienceResolver(ws.DatabaseAudienceResolver(db))
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go hub.Run(runCtx)

	client := &ws.Client{UserID: "user-a", Send: make(chan []byte, 8)}
	hub.Register(client)
	deadline := time.Now().Add(3 * time.Second)
	for hub.ClientCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if hub.ClientCount() != 1 {
		t.Fatalf("registered websocket clients = %d, want 1", hub.ClientCount())
	}
	return &handlers{posts: posts.New(db), hub: hub}, client
}
