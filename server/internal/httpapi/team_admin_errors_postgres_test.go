package httpapi

import (
	"context"
	"encoding/json"
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
	"github.com/hkjang/moyro/server/internal/teams"
	"github.com/hkjang/moyro/server/internal/ws"
)

// Ten team-administration endpoints gate on callerCanAdminTeam, which asks
// auth.HasRole and teams.IsTeamAdmin. Both of those read a roles column out of
// PostgreSQL, so both can fail for reasons that have nothing to do with the
// caller's standing: a dropped connection, a broken team_members table, a
// cancelled context.
//
// Folding those faults into the same 403 the endpoints give a non-admin is a
// lie the client cannot see through. 403 is permanent — no official client
// retries it — so a team admin whose database hiccuped is told, durably, that
// they are not a team admin. The sibling channel path already makes the
// opposite call: listChannelViews turns an IsMember fault into
// 500 api.context.permissions.app_error rather than a denial.
//
// This pins both halves of the split at once, through the real auth/teams/
// audit services and the chi patterns copied from router.go:
//   - a missing row is still an answer — a non-member and a deleted user get
//     the same 403 they always did, because pgx.ErrNoRows means "not an admin"
//     (unless a preceding guest guard rejects the deleted session with 401)
//   - anything else is a fault and must surface as 500, before the audit row,
//     the write and the broadcast
//
// Both denial strings are represented: compat_wave_handlers.go says "team
// admin required" and compat_wave_handlers_final.go says "team_admin
// required". They stay as they are; only the fault branch is new.
func TestTeamAdminCheckFaultIsNotForbidden(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	seedSidebarHandlerFixture(t, ctx, db)
	// user-a administers team-main. user-b is a plain member, user-c is not on
	// the team at all, and user-gone is soft-deleted (HasRole's query filters
	// on delete_at=0, so it hands back pgx.ErrNoRows for them). user-victim,
	// user-target and user-leaver are removal/roles subjects.
	if _, err := db.Pool.Exec(ctx, `
		UPDATE team_members SET roles='team_user team_admin' WHERE team_id='team-main' AND user_id='user-a';
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES ('user-b', 'user-b', 'b@example.test', 'hash', 'system_user', 1, 1),
		       ('user-c', 'user-c', 'c@example.test', 'hash', 'system_user', 1, 1),
		       ('user-victim', 'user-victim', 'v@example.test', 'hash', 'system_user', 1, 1),
		       ('user-target', 'user-target', 't@example.test', 'hash', 'system_user', 1, 1),
		       ('user-leaver', 'user-leaver', 'l@example.test', 'hash', 'system_user', 1, 1);
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at, delete_at)
		VALUES ('user-gone', 'user-gone', 'g@example.test', 'hash', 'system_user', 1, 1, 7);
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES ('team-main', 'user-b', 'team_user', 1),
		       ('team-main', 'user-victim', 'team_user', 1),
		       ('team-main', 'user-target', 'team_user', 1),
		       ('team-main', 'user-leaver', 'team_user', 1),
		       ('team-main', 'user-gone', 'team_user', 1)
	`); err != nil {
		t.Fatalf("seed team admin actors: %v", err)
	}

	hub := ws.NewHub()
	hub.SetAudienceResolver(ws.DatabaseAudienceResolver(db))
	runCtx, cancelHub := context.WithCancel(ctx)
	t.Cleanup(cancelHub)
	go hub.Run(runCtx)

	h := &handlers{
		auth:  auth.New(db, []byte("team-admin-errors-signing-key!!!"), time.Hour, nil),
		teams: teams.New(db),
		audit: audit.New(db, slog.Default()),
		hub:   hub,
	}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor := r.Header.Get("X-Test-Actor")
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey, actor)))
		})
	})
	// Patterns copied character for character from router.go:717/902/904/995.
	router.Put("/teams/{teamID}/members/{userID}/roles", h.setTeamMemberRoles)
	router.Delete("/teams/{teamID}/members/{userID}", h.removeTeamMember)
	router.Post("/teams/{teamID}/image", h.uploadTeamImage)
	router.Post("/teams/{teamID}/invite-guests/email", h.inviteGuestsByEmail)

	type teamAdminRoute struct {
		name   string
		method string
		// okPath is exercised by the admin and mutates; denyPath is the one
		// every refused actor uses, so a denial never disturbs the fixture.
		okPath    string
		denyPath  string
		body      string
		forbidden string
		// guestGated routes run denyGuestMutation first, which 401s a
		// soft-deleted actor before the admin check is ever reached.
		guestGated bool
	}
	routes := []teamAdminRoute{
		{
			name: "set member roles", method: http.MethodPut,
			okPath: "/teams/team-main/members/user-target/roles", denyPath: "/teams/team-main/members/user-target/roles",
			body: `{"roles":"team_user team_admin"}`, forbidden: "team admin required", guestGated: true,
		},
		{
			name: "remove member", method: http.MethodDelete,
			okPath: "/teams/team-main/members/user-victim", denyPath: "/teams/team-main/members/user-target",
			forbidden: "team_admin required", guestGated: true,
		},
		{
			name: "upload team image", method: http.MethodPost,
			okPath: "/teams/team-main/image", denyPath: "/teams/team-main/image",
			forbidden: "team_admin required", guestGated: true,
		},
		{
			name: "invite guests by email", method: http.MethodPost,
			okPath: "/teams/team-main/invite-guests/email", denyPath: "/teams/team-main/invite-guests/email",
			forbidden: "team_admin required",
		},
	}

	// The admin's answer is the baseline every refusal below is measured
	// against; it has to keep working unchanged.
	for _, rt := range routes {
		t.Run(rt.name+" answers the team admin", func(t *testing.T) {
			rr := serveTeamAdminRequest(router, rt.method, rt.okPath, "user-a", rt.body)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
			}
		})
	}

	// A plain member and a stranger are both "not an admin" — pgx.ErrNoRows on
	// team_members must not be promoted to a fault.
	for _, rt := range routes {
		for _, actor := range []string{"user-b", "user-c"} {
			t.Run(rt.name+" refuses "+actor, func(t *testing.T) {
				rr := serveTeamAdminRequest(router, rt.method, rt.denyPath, actor, rt.body)
				assertTeamAdminError(t, rr, http.StatusForbidden, "api.context.permissions.app_error", rt.forbidden)
			})
		}
		if rt.guestGated {
			continue
		}
		// A soft-deleted actor makes HasRole itself return pgx.ErrNoRows. That
		// is the other half of the guard: still 403, never 500.
		t.Run(rt.name+" refuses a deleted user", func(t *testing.T) {
			rr := serveTeamAdminRequest(router, rt.method, rt.denyPath, "user-gone", rt.body)
			assertTeamAdminError(t, rr, http.StatusForbidden, "api.context.permissions.app_error", rt.forbidden)
		})
	}

	// Leaving a team of your own accord never consults the admin check at all
	// (removeTeamMember short-circuits on uid == caller). Pin that here so the
	// fault assertion below can tell the two paths apart.
	t.Run("a member may still remove themselves", func(t *testing.T) {
		rr := serveTeamAdminRequest(router, http.MethodDelete, "/teams/team-main/members/user-leaver", "user-leaver", "")
		if rr.Code != http.StatusOK {
			t.Fatalf("self-removal status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
		}
	})

	// Observe the successful audit rows before breaking anything, so the
	// "no row for a refused caller" assertion is not just a race we won.
	for _, want := range []struct{ action, target string }{
		{audit.ActionTeamMemberRoles, "user-target"},
		{audit.ActionTeamImageUpload, "team-main"},
		{audit.ActionTeamInviteGuests, "team-main"},
	} {
		waitForAuditRow(t, ctx, db, want.action, want.target)
	}

	// Now the fault. Dropping team_members inside this test's private schema
	// leaves users intact, so HasRole still answers normally and the only
	// broken step in the request is teams.IsTeamAdmin — one fault, one
	// observable.
	if _, err := db.Pool.Exec(ctx, `DROP TABLE team_members CASCADE`); err != nil {
		t.Fatalf("drop team_members: %v", err)
	}
	for _, rt := range routes {
		t.Run(rt.name+" reports the team admin lookup fault", func(t *testing.T) {
			rr := serveTeamAdminRequest(router, rt.method, rt.denyPath, "user-c", rt.body)
			assertTeamAdminError(t, rr, http.StatusInternalServerError,
				"api.context.permissions.app_error", "failed to check team admin permissions")
		})
	}

	// Self-removal still does not ask about team administration, so it fails
	// further down with its own error id rather than the permissions one.
	t.Run("self-removal still skips the team admin lookup", func(t *testing.T) {
		rr := serveTeamAdminRequest(router, http.MethodDelete, "/teams/team-main/members/user-c", "user-c", "")
		if id := decodeErrorID(t, rr); id != "api.team.member.remove.app_error" {
			t.Fatalf("error id = %q, want the removal error rather than a permissions verdict (body %s)",
				id, rr.Body.String())
		}
	})

	// A request that never got past the permission check has nothing to
	// record. Watch past LogAsync's three-second database timeout.
	deadline := time.Now().Add(3200 * time.Millisecond)
	for {
		var count int
		if err := db.Pool.QueryRow(ctx,
			`SELECT count(*) FROM audit_logs WHERE actor_id='user-c'`).Scan(&count); err != nil {
			t.Fatalf("count refused-caller audit rows: %v", err)
		}
		if count != 0 {
			t.Fatalf("audit rows for the refused caller = %d, want none", count)
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func serveTeamAdminRequest(router http.Handler, method, path, actor, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-Test-Actor", actor)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, r)
	return rr
}

func assertTeamAdminError(t *testing.T, rr *httptest.ResponseRecorder, status int, id, message string) {
	t.Helper()
	if rr.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", rr.Code, status, rr.Body.String())
	}
	var body apiError
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rr.Body.String(), err)
	}
	if body.ID != id || body.Message != message {
		t.Fatalf("error = %q/%q, want %q/%q", body.ID, body.Message, id, message)
	}
}
