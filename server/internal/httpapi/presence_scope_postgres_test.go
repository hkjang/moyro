package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/audit"
	"github.com/hkjang/moyro/server/internal/auth"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/hkjang/moyro/server/internal/userstatus"
	"github.com/hkjang/moyro/server/internal/ws"
)

func TestGuestStatusAndAvatarVisibilityPostgres(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate presence scope schema: %v", err)
	}
	seedPresenceScopeFixture(t, ctx, db)

	h := &handlers{
		auth:   auth.New(db, []byte("presence-scope-test-signing-key"), time.Hour, nil),
		status: userstatus.New(db),
	}

	t.Run("single status hides unshared and expired users from guest", func(t *testing.T) {
		for _, target := range []string{"presence-hidden", "presence-expired-guest", "presence-missing"} {
			response := invokeUserResourceHandler(ctx, h.getUserStatus, http.MethodGet,
				"/api/v4/users/"+target+"/status", "presence-guest", target, "")
			assertNotFoundWithoutUserLeak(t, response, target)
		}

		shared := invokeUserResourceHandler(ctx, h.getUserStatus, http.MethodGet,
			"/api/v4/users/presence-shared/status", "presence-guest", "presence-shared", "")
		if shared.Code != http.StatusOK {
			t.Fatalf("shared status code = %d, body=%s", shared.Code, shared.Body.String())
		}
		var status userstatus.Status
		if err := json.NewDecoder(shared.Body).Decode(&status); err != nil {
			t.Fatal(err)
		}
		if status.UserID != "presence-shared" || status.Status != userstatus.Online {
			t.Fatalf("shared status = %#v", status)
		}

		regular := invokeUserResourceHandler(ctx, h.getUserStatus, http.MethodGet,
			"/api/v4/users/presence-hidden/status", "presence-regular", "presence-hidden", "")
		if regular.Code != http.StatusOK {
			t.Fatalf("regular status code = %d, body=%s", regular.Code, regular.Body.String())
		}
	})

	t.Run("bulk status intersects guest-visible ids in input order", func(t *testing.T) {
		const body = `["presence-hidden","presence-shared","presence-expired-guest","presence-guest"]`
		guest := invokeUserResourceHandler(ctx, h.getUserStatusesByIDs, http.MethodPost,
			"/api/v4/users/statuses/ids", "presence-guest", "", body)
		assertStatusIDs(t, guest, []string{"presence-shared", "presence-guest"})

		regular := invokeUserResourceHandler(ctx, h.getUserStatusesByIDs, http.MethodPost,
			"/api/v4/users/statuses/ids", "presence-regular", "", body)
		assertStatusIDs(t, regular, []string{
			"presence-hidden", "presence-shared", "presence-expired-guest", "presence-guest",
		})
	})

	t.Run("avatar endpoints hide unshared users from guest", func(t *testing.T) {
		for _, handler := range []func(http.ResponseWriter, *http.Request){h.getUserImage, h.getDefaultProfileImage} {
			hidden := invokeUserResourceHandler(ctx, handler, http.MethodGet,
				"/api/v4/users/presence-hidden/image", "presence-guest", "presence-hidden", "")
			missing := invokeUserResourceHandler(ctx, handler, http.MethodGet,
				"/api/v4/users/presence-missing/image", "presence-guest", "presence-missing", "")
			if hidden.Code != http.StatusNotFound || missing.Code != http.StatusNotFound {
				t.Fatalf("hidden/missing avatar codes = %d/%d", hidden.Code, missing.Code)
			}
			var hiddenErr, missingErr apiError
			if err := json.NewDecoder(hidden.Body).Decode(&hiddenErr); err != nil {
				t.Fatal(err)
			}
			if err := json.NewDecoder(missing.Body).Decode(&missingErr); err != nil {
				t.Fatal(err)
			}
			if hiddenErr.ID != missingErr.ID || hiddenErr.Message != "user not found" || missingErr.Message != "user not found" {
				t.Fatalf("hidden avatar response %#v differs from missing %#v", hiddenErr, missingErr)
			}
		}

		shared := invokeUserResourceHandler(ctx, h.getDefaultProfileImage, http.MethodGet,
			"/api/v4/users/presence-shared/image/default", "presence-guest", "presence-shared", "")
		if shared.Code != http.StatusOK || !strings.Contains(shared.Body.String(), "<svg") {
			t.Fatalf("shared default avatar = %d, body=%s", shared.Code, shared.Body.String())
		}
		regular := invokeUserResourceHandler(ctx, h.getDefaultProfileImage, http.MethodGet,
			"/api/v4/users/presence-hidden/image/default", "presence-regular", "presence-hidden", "")
		if regular.Code != http.StatusOK {
			t.Fatalf("regular default avatar = %d, body=%s", regular.Code, regular.Body.String())
		}
	})
}

func TestPresenceAndCustomStatusWebSocketAudiencePostgres(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate presence WebSocket schema: %v", err)
	}
	seedPresenceScopeFixture(t, ctx, db)

	hub := ws.NewHub()
	hub.SetAudienceResolver(ws.DatabaseAudienceResolver(db))
	runCtx, stopHub := context.WithCancel(context.Background())
	defer stopHub()
	go hub.Run(runCtx)

	clients := map[string]*ws.Client{}
	for _, id := range []string{
		"presence-subject", "presence-regular", "presence-shared-guest",
		"presence-unshared-guest", "presence-expired-guest",
	} {
		clients[id] = &ws.Client{UserID: id, Send: make(chan []byte, 4)}
		hub.Register(clients[id])
	}
	deadline := time.Now().Add(2 * time.Second)
	for hub.ClientCount() != len(clients) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if hub.ClientCount() != len(clients) {
		t.Fatalf("registered WebSocket clients = %d, want %d", hub.ClientCount(), len(clients))
	}

	h := &handlers{
		auth:   auth.New(db, []byte("presence-ws-test-signing-key"), time.Hour, nil),
		status: userstatus.New(db),
		audit:  audit.New(db, slog.New(slog.NewTextHandler(io.Discard, nil))),
		hub:    hub,
	}
	presence := invokeUserResourceHandler(ctx, h.updateUserStatus, http.MethodPut,
		"/api/v4/users/presence-subject/status", "presence-subject", "presence-subject",
		`{"status":"away","manual":true}`)
	if presence.Code != http.StatusOK {
		t.Fatalf("update presence = %d, body=%s", presence.Code, presence.Body.String())
	}
	assertScopedStatusFanout(t, hub, clients, "status_change")

	custom := invokeUserResourceHandler(ctx, h.setCustomStatus, http.MethodPut,
		"/api/v4/users/presence-subject/status/custom", "presence-subject", "presence-subject",
		`{"emoji":"lock","text":"private presence"}`)
	if custom.Code != http.StatusOK {
		t.Fatalf("update custom status = %d, body=%s", custom.Code, custom.Body.String())
	}
	assertScopedStatusFanout(t, hub, clients, "custom_status_changed")
}

func seedPresenceScopeFixture(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	now := time.Now().UnixMilli()
	statements := []struct {
		query string
		args  []any
	}{
		{query: `
			INSERT INTO users
				(id, username, email, password_hash, roles, create_at, update_at, guest_expires_at)
			VALUES
				('presence-subject','presence-subject','presence-subject@example.test','hash','system_user',$1,$1,0),
				('presence-regular','presence-regular','presence-regular@example.test','hash','system_user',$1,$1,0),
				('presence-hidden','presence-hidden','presence-hidden@example.test','hash','system_user',$1,$1,0),
				('presence-shared','presence-shared','presence-shared@example.test','hash','system_user',$1,$1,0),
				('presence-guest','presence-guest','presence-guest@example.test','hash','system_guest',$1,$1,$2),
				('presence-shared-guest','presence-shared-guest','presence-shared-guest@example.test','hash','system_guest',$1,$1,$2),
				('presence-unshared-guest','presence-unshared-guest','presence-unshared-guest@example.test','hash','system_guest',$1,$1,$2),
				('presence-expired-guest','presence-expired-guest','presence-expired-guest@example.test','hash','system_guest',$1,$1,$3)`,
			args: []any{now, now + int64(time.Hour/time.Millisecond), now - 1}},
		{query: `INSERT INTO teams (id, display_name, name, type, create_at, update_at)
			VALUES ('presence-scope-team','Presence Scope','presence-scope-team','O',$1,$1)`, args: []any{now}},
		{query: `INSERT INTO team_members (team_id, user_id, roles, create_at)
			VALUES
				('presence-scope-team','presence-subject','team_user',$1),
				('presence-scope-team','presence-shared','team_user',$1),
				('presence-scope-team','presence-guest','team_user',$1),
				('presence-scope-team','presence-shared-guest','team_user',$1),
				('presence-scope-team','presence-expired-guest','team_user',$1)`, args: []any{now}},
		{query: `INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
			VALUES ('presence-scope-channel','presence-scope-team','P','Presence Scope','presence-scope-channel',$1,$1)`, args: []any{now}},
		{query: `INSERT INTO channel_members (channel_id, user_id, roles, create_at)
			VALUES
				('presence-scope-channel','presence-subject','channel_user',$1),
				('presence-scope-channel','presence-shared','channel_user',$1),
				('presence-scope-channel','presence-guest','channel_user',$1),
				('presence-scope-channel','presence-shared-guest','channel_user',$1),
				('presence-scope-channel','presence-expired-guest','channel_user',$1)`, args: []any{now}},
		{query: `INSERT INTO user_statuses (user_id, status, manual, last_activity_at, update_at)
			VALUES
				('presence-hidden','dnd',TRUE,$1,$1),
				('presence-shared','online',FALSE,$1,$1),
				('presence-expired-guest','away',FALSE,$1,$1)`, args: []any{now}},
	}
	for _, statement := range statements {
		if _, err := db.Pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed presence scope fixture: %v", err)
		}
	}
}

func invokeUserResourceHandler(
	ctx context.Context,
	handler func(http.ResponseWriter, *http.Request),
	method, path, actorID, targetID, body string,
) *httptest.ResponseRecorder {
	routeContext := chi.NewRouteContext()
	if targetID != "" {
		routeContext.URLParams.Add("userID", targetID)
	}
	requestContext := context.WithValue(ctx, chi.RouteCtxKey, routeContext)
	requestContext = context.WithValue(requestContext, userIDKey, actorID)
	request := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(requestContext)
	response := httptest.NewRecorder()
	handler(response, request)
	return response
}

func assertNotFoundWithoutUserLeak(t *testing.T, response *httptest.ResponseRecorder, hiddenID string) {
	t.Helper()
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), hiddenID) {
		t.Fatalf("hidden user id %q leaked in response: %s", hiddenID, response.Body.String())
	}
}

func assertStatusIDs(t *testing.T, response *httptest.ResponseRecorder, want []string) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	var statuses []userstatus.Status
	if err := json.NewDecoder(response.Body).Decode(&statuses); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(statuses))
	for _, status := range statuses {
		got = append(got, status.UserID)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("status user ids = %v, want %v", got, want)
	}
}

func assertScopedStatusFanout(t *testing.T, hub *ws.Hub, clients map[string]*ws.Client, eventName string) {
	t.Helper()
	for _, id := range []string{"presence-subject", "presence-regular", "presence-shared-guest"} {
		select {
		case raw := <-clients[id].Send:
			var event ws.Event
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if event.Event != eventName || event.Broadcast.SubjectUserID != "presence-subject" {
				t.Fatalf("recipient %q event = %#v", id, event)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("recipient %q did not receive %s", id, eventName)
		}
	}

	// A subsequent private marker proves the preceding fanout has completed
	// before we inspect denied recipients' queues.
	hub.Broadcast(ws.Event{Event: "test_barrier", Broadcast: ws.Broadcast{UserID: "presence-subject"}})
	select {
	case <-clients["presence-subject"].Send:
	case <-time.After(2 * time.Second):
		t.Fatal("WebSocket fanout barrier timed out")
	}
	for _, id := range []string{"presence-unshared-guest", "presence-expired-guest"} {
		select {
		case raw := <-clients[id].Send:
			t.Fatalf("denied guest %q received %s", id, raw)
		default:
		}
	}
}
