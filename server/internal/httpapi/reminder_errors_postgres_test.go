package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/reminders"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/hkjang/moyro/server/internal/ws"
)

// Both reminder-create routes used to fold every posts.Get error into the
// 404 "post not found" answer, so a dead database told the client the post
// had been deleted — a permanent-looking answer no client retries. The two
// routes must agree: a row that is genuinely absent is 404 with the id and
// message clients key off, and a storage fault is 500.
func TestCreatePostReminderSeparatesMissingPostsFromStorageFaults(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	seedSidebarHandlerFixture(t, ctx, db)
	h, client := newReminderErrorHandlers(t, ctx, db)

	post, err := posts.New(db).Create(ctx, "chan-alpha", "user-a", "", "잊지 말 것", nil, nil)
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	remindAt := time.Now().UnixMilli() + 3_600_000

	// The happy path stays 201 with a reminder_created event addressed to
	// the reminder's owner, on both the native route and its alias.
	for _, route := range reminderCreateRoutes(h) {
		rr := httptest.NewRecorder()
		route.call(rr, reminderCreateRequest(t, post.ID, remindAt))
		if rr.Code != http.StatusCreated {
			t.Fatalf("%s: status = %d, want 201 (body %s)", route.name, rr.Code, rr.Body.String())
		}
		var created reminders.Reminder
		if err := json.NewDecoder(rr.Body).Decode(&created); err != nil {
			t.Fatalf("%s: decode reminder: %v", route.name, err)
		}
		if created.UserID != "user-a" || created.PostID != post.ID || created.RemindAt != remindAt {
			t.Fatalf("%s: reminder = %+v, want it owned by user-a for the seeded post", route.name, created)
		}
		event := awaitReminderCreatedEvent(t, client, route.name)
		if event["id"] != created.ID || event["post_id"] != post.ID {
			t.Fatalf("%s: reminder_created data = %#v, want the created reminder", route.name, event)
		}
	}

	// A post id with no row keeps the exact 404 contract, unchanged.
	for _, route := range reminderCreateRoutes(h) {
		rr := httptest.NewRecorder()
		route.call(rr, reminderCreateRequest(t, "post-that-never-existed", remindAt))
		assertReminderAPIError(t, rr, route.name+" missing post", http.StatusNotFound,
			"api.reminder.create.not_found", "post not found")
	}

	// Pull the table out from under posts.Get so the lookup fails for a
	// reason that is not "no such row".
	if _, err := db.Pool.Exec(ctx, `DROP TABLE posts CASCADE`); err != nil {
		t.Fatalf("drop posts: %v", err)
	}
	for _, route := range reminderCreateRoutes(h) {
		rr := httptest.NewRecorder()
		route.call(rr, reminderCreateRequest(t, post.ID, remindAt))
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("%s: storage fault status = %d, want 500 (body %s)", route.name, rr.Code, rr.Body.String())
		}
	}
}

// The alias route dropped the IsMember error and answered 403 "not a channel
// member" for it, while the native route already reported 500. Same resource,
// same input, two different answers — the alias now uses the native route's
// error id so a storage fault reads the same on both.
func TestCreateUserPostReminderSeparatesNonMembersFromMembershipFaults(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	seedSidebarHandlerFixture(t, ctx, db)
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES ('user-b', 'user-b', 'b@example.test', 'hash', 'system_user', 1, 1)
	`); err != nil {
		t.Fatalf("seed outsider: %v", err)
	}
	h, _ := newReminderErrorHandlers(t, ctx, db)

	post, err := posts.New(db).Create(ctx, "chan-alpha", "user-a", "", "잊지 말 것", nil, nil)
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	remindAt := time.Now().UnixMilli() + 3_600_000

	// A real outsider is still 403 with the unchanged id and message.
	rr := httptest.NewRecorder()
	h.createUserPostReminder(rr, reminderAliasRequestAs(t, "user-b", post.ID, remindAt))
	assertReminderAPIError(t, rr, "outsider", http.StatusForbidden,
		"api.reminder.create.forbidden", "not a channel member")

	// With channel_members unreadable the membership answer is unknown, not
	// "no" — both routes must say so with the same error id.
	if _, err := db.Pool.Exec(ctx, `DROP TABLE channel_members CASCADE`); err != nil {
		t.Fatalf("drop channel_members: %v", err)
	}
	for _, route := range reminderCreateRoutes(h) {
		rr := httptest.NewRecorder()
		route.call(rr, reminderCreateRequest(t, post.ID, remindAt))
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("%s: membership fault status = %d, want 500 (body %s)", route.name, rr.Code, rr.Body.String())
		}
		var body apiError
		if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
			t.Fatalf("%s: decode 500 body: %v", route.name, err)
		}
		if body.ID != "api.reminder.create.member_check" {
			t.Fatalf("%s: membership fault id = %q, want api.reminder.create.member_check", route.name, body.ID)
		}
	}
}

// reminderRoute names one of the two create paths so a single table can drive
// both through the real handler funcs.
type reminderRoute struct {
	name string
	call func(w http.ResponseWriter, r *http.Request)
}

func reminderCreateRoutes(h *handlers) []reminderRoute {
	return []reminderRoute{
		{name: "POST /posts/{postID}/remind_me", call: h.createPostReminder},
		{name: "POST /users/{userID}/posts/{postID}/reminder", call: h.createUserPostReminder},
	}
}

// newReminderErrorHandlers wires the real posts, channels and reminders
// services plus a running hub with the production audience resolver, and
// returns a websocket client registered for user-a.
func newReminderErrorHandlers(t *testing.T, ctx context.Context, db *store.DB) (*handlers, *ws.Client) {
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
	h := &handlers{
		posts:     posts.New(db),
		channels:  channels.New(db),
		reminders: reminders.New(db),
		hub:       hub,
	}
	return h, client
}

// reminderCreateRequest builds a self-scoped create for user-a carrying both
// URL params, so the same request shape drives either route (actor == target
// means requireUserParamAccess passes without an auth service).
func reminderCreateRequest(t *testing.T, postID string, remindAt int64) *http.Request {
	t.Helper()
	return reminderAliasRequestAs(t, "user-a", postID, remindAt)
}

func reminderAliasRequestAs(t *testing.T, uid, postID string, remindAt int64) *http.Request {
	t.Helper()
	body := strings.NewReader(`{"remind_at":` + strconv.FormatInt(remindAt, 10) + `}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v4/users/"+uid+"/posts/"+postID+"/reminder", body)
	reqCtx := context.WithValue(req.Context(), userIDKey, uid)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("userID", uid)
	routeCtx.URLParams.Add("postID", postID)
	return req.WithContext(context.WithValue(reqCtx, chi.RouteCtxKey, routeCtx))
}

func awaitReminderCreatedEvent(t *testing.T, client *ws.Client, route string) map[string]any {
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
		if envelope.Event != "reminder_created" {
			t.Fatalf("%s: websocket event = %q, want reminder_created", route, envelope.Event)
		}
		return envelope.Data
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: timed out waiting for reminder_created", route)
		return nil
	}
}

func assertReminderAPIError(t *testing.T, rr *httptest.ResponseRecorder, label string, status int, id, message string) {
	t.Helper()
	if rr.Code != status {
		t.Fatalf("%s: status = %d, want %d (body %s)", label, rr.Code, status, rr.Body.String())
	}
	var body apiError
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("%s: decode error body: %v", label, err)
	}
	if body.ID != id || body.Message != message {
		t.Fatalf("%s: error body = %+v, want id %q message %q", label, body, id, message)
	}
}
