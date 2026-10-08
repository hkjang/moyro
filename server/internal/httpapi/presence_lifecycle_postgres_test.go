package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hkjang/moyro/server/internal/auth"
	"github.com/hkjang/moyro/server/internal/config"
	"github.com/hkjang/moyro/server/internal/pluginhost"
	"github.com/hkjang/moyro/server/internal/secrets"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/hkjang/moyro/server/internal/userstatus"
	"github.com/hkjang/moyro/server/internal/ws"
)

// Unlike the audience tests, this exercises NewRouter's lifecycle callbacks
// through authenticated HTTP upgrades. Every transition waits for its own
// status_change before reading HTTP/DB state: an unchanged manual status alone
// would also pass if the callbacks never ran.
func TestPresenceSocketLifecyclePreservesManualStatus(t *testing.T) {
	f := newPresenceLifecycleFixture(t)
	observerID, observerToken := f.register(t, "presence-observer")
	observer := f.dial(t, observerToken)
	f.observe(t, observer, observerID, observerToken, userstatus.Online, false)

	// There is no remaining recipient for the final observer's disconnect.
	// Only teardown polls its stored offline state; scenario synchronization
	// always uses the actual WebSocket event, never socket counts or sleeps.
	t.Cleanup(func() {
		_ = observer.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			var status string
			var manual bool
			err := f.db.Pool.QueryRow(ctx,
				`SELECT status, manual FROM user_statuses WHERE user_id=$1`, observerID).Scan(&status, &manual)
			if err == nil && status == userstatus.Offline && !manual {
				return
			}
			select {
			case <-ctx.Done():
				t.Errorf("observer shutdown: status=%q manual=%t err=%v", status, manual, err)
				return
			case <-ticker.C:
			}
		}
	})

	for _, tc := range []struct {
		name   string
		status string
		manual bool
	}{
		{"automatic presence follows its only socket", userstatus.Offline, false},
		{"manual dnd survives reconnect", userstatus.DND, true},
		{"manual away survives reconnect", userstatus.Away, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, token := f.register(t, "presence-"+tc.status)
			body, err := json.Marshal(userstatus.Status{UserID: id, Status: tc.status, Manual: tc.manual})
			if err != nil {
				t.Fatal(err)
			}
			set := f.requestStatus(t, http.MethodPut, id, token, body)
			assertPresenceLifecycleStatus(t, "PUT", set, id, tc.status, tc.manual)
			// Drain the PUT event before opening a socket. Otherwise the same
			// manual status in that event could disguise a missing callback.
			f.observe(t, observer, id, token, tc.status, tc.manual)

			connected, disconnected := tc.status, tc.status
			if !tc.manual {
				connected, disconnected = userstatus.Online, userstatus.Offline
			}
			for _, phase := range []string{"first connection", "reconnection"} {
				t.Run(phase, func(t *testing.T) {
					socket := f.dial(t, token)
					f.observe(t, observer, id, token, connected, tc.manual)
					if err := socket.Close(); err != nil {
						t.Fatalf("close only target socket: %v", err)
					}
					// Complete the last-socket callback before reconnecting;
					// rapid reconnect races are intentionally a separate concern.
					f.observe(t, observer, id, token, disconnected, tc.manual)
				})
				if t.Failed() {
					return
				}
			}
		})
		if t.Failed() {
			break // A failed read deadline makes the observer unusable.
		}
	}
}

type presenceLifecycleFixture struct {
	ctx    context.Context
	db     *store.DB
	auth   *auth.Service
	server *httptest.Server
}

func newPresenceLifecycleFixture(t *testing.T) *presenceLifecycleFixture {
	t.Helper()
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	t.Cleanup(cancel)
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate presence schema: %v", err)
	}
	cfg := &config.Config{
		EncryptionKey:   bytes.Repeat([]byte{0x5c}, secrets.MasterKeySize),
		JWTSecret:       []byte("presence-lifecycle-test-signing-key"),
		TokenTTL:        time.Hour,
		PluginDir:       t.TempDir(),
		FileStorageRoot: t.TempDir(),
	}
	secretManager, err := secrets.New(cfg.EncryptionKey)
	if err != nil {
		t.Fatalf("secret manager: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	host, err := pluginhost.NewWithRuntime(cfg.PluginDir, db, secretManager, logger)
	if err != nil {
		t.Fatalf("plugin host: %v", err)
	}
	t.Cleanup(host.Shutdown)
	hub := ws.NewHub()
	// Install the real audience resolver and callbacks before Run reads them.
	router := NewRouter(cfg, db, hub, host, logger)
	hubCtx, cancelHub := context.WithCancel(ctx)
	hubDone := make(chan struct{})
	go func() {
		defer close(hubDone)
		hub.Run(hubCtx)
	}()
	t.Cleanup(func() {
		cancelHub()
		<-hubDone
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return &presenceLifecycleFixture{
		ctx: ctx, db: db, server: server,
		auth: auth.New(db, cfg.JWTSecret, cfg.TokenTTL, secretManager),
	}
}

func (f *presenceLifecycleFixture) register(t *testing.T, name string) (string, string) {
	t.Helper()
	user, err := f.auth.Register(f.ctx, name, name+"@example.test", "long-test-password")
	if err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	_, token, err := f.auth.Login(f.ctx, name, "long-test-password")
	if err != nil {
		t.Fatalf("login %s: %v", name, err)
	}
	return user.ID, token
}

func (f *presenceLifecycleFixture) dial(t *testing.T, token string) *websocket.Conn {
	t.Helper()
	header := http.Header{"Authorization": {"Bearer " + token}}
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	socket, response, err := dialer.DialContext(f.ctx,
		"ws"+strings.TrimPrefix(f.server.URL, "http")+"/api/v4/websocket", header)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial authenticated WebSocket: %v", err)
	}
	t.Cleanup(func() { _ = socket.Close() })
	return socket
}

func (f *presenceLifecycleFixture) requestStatus(t *testing.T, method, id, token string, body []byte) userstatus.Status {
	t.Helper()
	req, err := http.NewRequestWithContext(f.ctx, method,
		f.server.URL+"/api/v4/users/"+id+"/status", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build status request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s status: %v", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s status returned HTTP %d, want 200", method, resp.StatusCode)
	}
	var status userstatus.Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode %s status: %v", method, err)
	}
	return status
}

func (f *presenceLifecycleFixture) observe(t *testing.T, observer *websocket.Conn, id, token, want string, manual bool) {
	t.Helper()
	// This test goroutine is the sole reader; the deadline covers the whole
	// search, including unrelated events, rather than resetting per frame.
	if err := observer.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set observer deadline: %v", err)
	}
	for {
		var event ws.Event
		if err := observer.ReadJSON(&event); err != nil {
			t.Fatalf("await status_change for %s (%s/manual=%t): %v", id, want, manual, err)
		}
		if event.Event != "status_change" || event.Data["user_id"] != id {
			continue
		}
		if event.Data["status"] != want {
			t.Fatalf("event status = %v, want %s", event.Data["status"], want)
		}
		payload, ok := event.Data["payload"].(string)
		if !ok {
			t.Fatalf("event payload type = %T, want JSON string", event.Data["payload"])
		}
		var status userstatus.Status
		if err := json.Unmarshal([]byte(payload), &status); err != nil {
			t.Fatalf("decode event payload: %v", err)
		}
		assertPresenceLifecycleStatus(t, "event payload", status, id, want, manual)
		break
	}
	// Use the subject's own real login, not the observer's credentials.
	got := f.requestStatus(t, http.MethodGet, id, token, nil)
	assertPresenceLifecycleStatus(t, "GET", got, id, want, manual)
	var stored userstatus.Status
	if err := f.db.Pool.QueryRow(f.ctx,
		`SELECT user_id, status, manual FROM user_statuses WHERE user_id=$1`, id).
		Scan(&stored.UserID, &stored.Status, &stored.Manual); err != nil {
		t.Fatalf("read persisted presence: %v", err)
	}
	assertPresenceLifecycleStatus(t, "DB", stored, id, want, manual)
}

func assertPresenceLifecycleStatus(t *testing.T, source string, got userstatus.Status, id, status string, manual bool) {
	t.Helper()
	if got.UserID != id || got.Status != status || got.Manual != manual {
		t.Fatalf("%s = {user_id:%s status:%s manual:%t}, want {user_id:%s status:%s manual:%t}",
			source, got.UserID, got.Status, got.Manual, id, status, manual)
	}
}
