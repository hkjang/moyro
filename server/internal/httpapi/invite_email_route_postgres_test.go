package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/auth"
	"github.com/hkjang/moyro/server/internal/config"
	"github.com/hkjang/moyro/server/internal/pluginhost"
	"github.com/hkjang/moyro/server/internal/secrets"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/hkjang/moyro/server/internal/ws"
)

// POST /api/v4/teams/{teamID}/invite/email is registered twice in router.go,
// inside the same authenticated group, with two different handlers:
//
//	router.go:756   h.inviteTeamMembersByEmail   — guest gate, requireTeamAdmin, audit row
//	router.go:1054  h.inviteTeamMembersFromBody  — guest gate only, then 200
//
// chi keeps one handler per method+pattern, so only one of them can ever run.
// Every other test in this package builds its own chi.NewRouter() and copies
// the pattern it cares about, which is exactly the shape of test that cannot
// see this: the collision lives in router.go's registration order, not in
// either handler. So this test drives the real NewRouter.
//
// What rides on the answer is authorization, not cosmetics. The invite
// endpoint is documented as team-admin-gated and audited; the sibling stub is
// neither. If the stub wins, a plain team member can call it and get 200, and
// no team.invite.email audit row is ever written.
//
// The three cases below pin the contract end to end through the production
// wiring, so a future re-registration cannot silently un-gate the route:
//   - a plain team member is refused
//   - a team admin succeeds AND leaves an audit row naming the team
//   - the stub's own path, /teams/members/invite, stays reachable — this one
//     guards against "fixing" the collision by deleting the wrong line
func TestInviteByEmailRouteKeepsTeamAdminGate(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}

	encryptionKey := bytes.Repeat([]byte{0x5c}, secrets.MasterKeySize)
	cfg := &config.Config{
		EncryptionKey:   encryptionKey,
		JWTSecret:       []byte("invite-email-route-signing-key!!"),
		TokenTTL:        time.Hour,
		PluginDir:       t.TempDir(),
		FileStorageRoot: t.TempDir(),
	}
	secretManager, err := secrets.New(encryptionKey)
	if err != nil {
		t.Fatalf("secret manager: %v", err)
	}

	// Users are registered through the real auth service so Login hands back
	// a token the production requireAuth middleware actually accepts.
	authSvc := auth.New(db, cfg.JWTSecret, cfg.TokenTTL, secretManager)
	admin, err := authSvc.Register(ctx, "invite-admin", "invite-admin@example.test", "long-test-password")
	if err != nil {
		t.Fatalf("register team admin: %v", err)
	}
	member, err := authSvc.Register(ctx, "invite-member", "invite-member@example.test", "long-test-password")
	if err != nil {
		t.Fatalf("register plain member: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO teams (id, name, display_name, type, create_at, update_at)
		VALUES ('team-invite', 'team-invite', 'Invite Team', 'O', 1, 1)
	`); err != nil {
		t.Fatalf("seed team: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES ('team-invite', $1, 'team_user team_admin', 1),
		       ('team-invite', $2, 'team_user', 1)
	`, admin.ID, member.ID); err != nil {
		t.Fatalf("seed memberships: %v", err)
	}

	adminToken := loginForInviteTest(t, ctx, authSvc, "invite-admin")
	memberToken := loginForInviteTest(t, ctx, authSvc, "invite-member")

	hub := ws.NewHub()
	runCtx, cancelHub := context.WithCancel(ctx)
	t.Cleanup(cancelHub)
	go hub.Run(runCtx)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	host, err := pluginhost.NewWithRuntime(cfg.PluginDir, db, secretManager, logger)
	if err != nil {
		t.Fatalf("plugin host: %v", err)
	}
	t.Cleanup(host.Shutdown)

	// The production router, not a hand-copied pattern. This is the only way
	// the duplicate registration is observable.
	router := NewRouter(cfg, db, hub, host, logger)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	post := func(t *testing.T, path, token, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+path, bytes.NewBufferString(body))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("issue request: %v", err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return resp.StatusCode, string(raw)
	}

	t.Run("a plain team member is refused", func(t *testing.T) {
		status, body := post(t, "/api/v4/teams/team-invite/invite/email", memberToken,
			`{"emails":["outsider@example.test"]}`)
		if status != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body %s)", status, body)
		}
		var payload struct {
			ID      string `json:"id"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatalf("decode refusal %q: %v", body, err)
		}
		if payload.ID != "api.context.permissions.app_error" {
			t.Fatalf("refusal id = %q, want api.context.permissions.app_error (body %s)", payload.ID, body)
		}
	})

	t.Run("a team admin invites and leaves an audit row", func(t *testing.T) {
		status, body := post(t, "/api/v4/teams/team-invite/invite/email", adminToken,
			`{"emails":["invitee@example.test"]}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", status, body)
		}
		// audit.LogAsync writes from a goroutine with a 3s timeout, so poll
		// rather than read once.
		deadline := time.Now().Add(6 * time.Second)
		for {
			var rows int
			if err := db.Pool.QueryRow(ctx,
				`SELECT COUNT(*) FROM audit_logs WHERE action='team.invite.email' AND target='team-invite' AND actor_id=$1`,
				admin.ID).Scan(&rows); err != nil {
				t.Fatalf("count audit rows: %v", err)
			}
			if rows == 1 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("team.invite.email audit row for team-invite = %d, want 1", rows)
			}
			time.Sleep(100 * time.Millisecond)
		}
	})

	t.Run("the stub path /teams/members/invite stays reachable", func(t *testing.T) {
		status, body := post(t, "/api/v4/teams/members/invite", memberToken,
			`{"emails":["outsider@example.test"]}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", status, body)
		}
	})
}

func loginForInviteTest(t *testing.T, ctx context.Context, authSvc *auth.Service, username string) string {
	t.Helper()
	_, token, err := authSvc.Login(ctx, username, "long-test-password")
	if err != nil {
		t.Fatalf("login %s: %v", username, err)
	}
	return token
}
