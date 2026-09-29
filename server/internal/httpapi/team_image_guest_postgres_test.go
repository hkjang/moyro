package httpapi

import (
	"context"
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
	"github.com/hkjang/moyro/server/internal/teams"
)

// Guest status and team administration live in different roles columns.
// Exercise their intersection through real services in an isolated schema.
func TestTeamImageGuestMutationPostgres(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	seedSidebarHandlerFixture(t, ctx, db)
	if _, err := db.Pool.Exec(ctx, `
		UPDATE team_members SET roles='team_user team_admin' WHERE team_id='team-main' AND user_id='user-a';
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at, delete_at)
		VALUES ('guest-admin', 'guest-admin', 'guest@example.test', 'hash', 'system_guest', 1, 1, 0),
		       ('sysadmin', 'sysadmin', 'admin@example.test', 'hash', 'system_user system_admin', 1, 1, 0),
		       ('member', 'member', 'member@example.test', 'hash', 'system_user', 1, 1, 0),
		       ('stranger', 'stranger', 'stranger@example.test', 'hash', 'system_user', 1, 1, 0),
		       ('deleted', 'deleted', 'deleted@example.test', 'hash', 'system_user', 1, 1, 7);
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES ('team-main', 'guest-admin', 'team_user team_admin', 1),
		       ('team-main', 'member', 'team_user', 1),
		       ('team-main', 'deleted', 'team_user', 1)
	`); err != nil {
		t.Fatalf("seed image actors: %v", err)
	}
	h := &handlers{
		auth:  auth.New(db, []byte("team-image-guest-signing-key!!!"), time.Hour, nil),
		teams: teams.New(db),
		audit: audit.New(db, slog.Default()),
	}
	guest, err := h.auth.UserByID(ctx, "guest-admin")
	if err != nil || !guest.IsGuest() {
		t.Fatalf("guest fixture = %v, error = %v", guest, err)
	}
	if admin, err := h.teams.IsTeamAdmin(ctx, "team-main", "guest-admin"); err != nil || !admin {
		t.Fatalf("guest team administration = %v, error = %v", admin, err)
	}

	// As in the existing handler regressions, inject the authenticated actor;
	// this tests authorization, not token/session middleware.
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey, r.Header.Get("X-Test-Actor"))))
		})
	})
	router.Route("/api/v4", func(r chi.Router) {
		r.Post("/teams/{teamID}/image", h.uploadTeamImage)
		r.Delete("/teams/{teamID}/image", h.deleteTeamImage)
	})
	const path = "/api/v4/teams/team-main/image"
	// Observe each success before submitting the next: waitForAuditRow expects
	// exactly one row for the action/target, and both admins use the same team.
	for _, actor := range []string{"user-a", "sysadmin"} {
		t.Run("accepts "+actor, func(t *testing.T) {
			rr := serveTeamAdminRequest(router, http.MethodPost, path, actor, "image")
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
			}
			assertStatusOKEnvelope(t, rr)
			waitForAuditRow(t, ctx, db, audit.ActionTeamImageUpload, "team-main")
			var count int
			if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs
				WHERE actor_id=$1 AND action=$2 AND target='team-main'`, actor, audit.ActionTeamImageUpload).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("successful actor audit rows = %d, want 1", count)
			}
			if _, err := db.Pool.Exec(ctx, `DELETE FROM audit_logs WHERE actor_id=$1`, actor); err != nil {
				t.Fatal(err)
			}
		})
	}

	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		t.Run(method+" refuses guest admin without reading body", func(t *testing.T) {
			body := &teamImageCountingBody{Reader: strings.NewReader("image bytes")}
			req := httptest.NewRequest(method, path, nil)
			req.Body = body
			req.Header.Set("X-Test-Actor", "guest-admin")
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if body.reads != 0 {
				t.Errorf("guest body reads = %d, want 0", body.reads)
			}
			assertTeamAdminError(t, rr, http.StatusForbidden, "api.team.image.guest_forbidden",
				"guest access is restricted to invited channels")
		})
	}
	t.Run("deleted user is rejected by the preceding guest guard", func(t *testing.T) {
		rr := serveTeamAdminRequest(router, http.MethodPost, path, "deleted", "image")
		assertTeamAdminError(t, rr, http.StatusUnauthorized, "api.team.image.guest_forbidden", "active user session required")
	})
	for _, actor := range []string{"member", "stranger"} {
		t.Run("refuses "+actor, func(t *testing.T) {
			rr := serveTeamAdminRequest(router, http.MethodPost, path, actor, "image")
			assertTeamAdminError(t, rr, http.StatusForbidden, "api.context.permissions.app_error", "team_admin required")
		})
	}

	t.Run("team admin still cannot exceed the cap", func(t *testing.T) {
		rr := serveUploadStub(router, http.MethodPost, path, "user-a", (10<<20)+1)
		if rr.Code != http.StatusRequestEntityTooLarge || decodeErrorID(t, rr) != "api.team.image.upload_too_large" {
			t.Fatalf("oversized response = %d %s", rr.Code, rr.Body.String())
		}
	})

	// Positive controls above establish that asynchronous auditing works.
	// Observe refusals beyond LogAsync's 3s timeout, as existing tests do.
	deadline := time.Now().Add(3200 * time.Millisecond)
	for {
		var count int
		if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("refused-request audit rows = %d, want none", count)
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type teamImageCountingBody struct {
	io.Reader
	reads int
}

func (b *teamImageCountingBody) Read(p []byte) (int, error) {
	b.reads++
	return b.Reader.Read(p)
}

func (*teamImageCountingBody) Close() error { return nil }
