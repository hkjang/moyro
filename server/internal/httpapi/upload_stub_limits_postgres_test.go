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
	"github.com/hkjang/moyro/server/internal/teams"
)

// The three multipart upload stubs — POST /teams/{teamID}/image, POST
// /bots/{botUserID}/icon and POST /uploads/{uploadID} — cap their request body
// with http.MaxBytesReader and then drain it. Draining is the whole point:
// none of them persists bytes yet, they just have to not hang the official
// client's upload form on EOF.
//
// What they must not do is swallow the cap. An io.Copy that stops because the
// reader refused to hand over byte N+1 has not received the upload, so a 200
// plus an audit row is two lies at once: the client believes its icon landed,
// and the operator reads an upload in the audit log that never happened.
// uploadChunk adds a third — it reports the truncated byte count as
// file_offset, which is exactly the number a resuming client uses as its next
// offset.
//
// This exercises the production chi patterns copied verbatim from
// router.go:904/957-959/977 rather than calling the handlers directly: the
// bot-icon audit target is read out of a URL parameter, so a test that
// registered a convenient pattern name would hide that half of the defect.
func TestUploadStubsRefuseOversizedBodies(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	seedSidebarHandlerFixture(t, ctx, db)
	// user-a administers team-main; sysadmin carries the global role the two
	// bot-icon routes gate on. Both go through the real auth/teams services.
	if _, err := db.Pool.Exec(ctx, `
		UPDATE team_members SET roles='team_user team_admin' WHERE team_id='team-main' AND user_id='user-a';
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES ('sysadmin', 'sysadmin', 'sysadmin@example.test', 'hash', 'system_user system_admin', 1, 1)
	`); err != nil {
		t.Fatalf("seed upload stub actors: %v", err)
	}

	h := &handlers{
		auth:  auth.New(db, []byte("upload-stub-limits-signing-key!!"), time.Hour, nil),
		teams: teams.New(db),
		audit: audit.New(db, slog.Default()),
	}
	// Patterns are copied character for character from router.go so the
	// handlers read the parameter names production gives them.
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor := r.Header.Get("X-Test-Actor")
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey, actor)))
		})
	})
	router.Post("/teams/{teamID}/image", h.uploadTeamImage)
	router.Post("/bots/{botUserID}/icon", h.uploadBotIcon)
	router.Delete("/bots/{botUserID}/icon", h.deleteBotIcon)
	router.Post("/uploads/{uploadID}", h.uploadChunk)

	const smallBody = 512

	type stub struct {
		name     string
		method   string
		path     string
		actor    string
		limit    int64
		errID    string
		action   string
		target   string
		wantBody func(t *testing.T, rr *httptest.ResponseRecorder)
	}
	stubs := []stub{
		{
			name: "team image", method: http.MethodPost, path: "/teams/team-main/image",
			actor: "user-a", limit: 10 << 20,
			errID:  "api.team.image.upload_too_large",
			action: audit.ActionTeamImageUpload, target: "team-main",
			wantBody: assertStatusOKEnvelope,
		},
		{
			name: "bot icon", method: http.MethodPost, path: "/bots/bot-alpha/icon",
			actor: "sysadmin", limit: 256 << 10,
			errID:  "api.bot.icon.upload_too_large",
			action: audit.ActionBotIconUpload, target: "bot-alpha",
			wantBody: assertStatusOKEnvelope,
		},
		{
			name: "upload chunk", method: http.MethodPost, path: "/uploads/upload-7",
			actor: "user-a", limit: 50 << 20,
			errID:  "api.upload.chunk.too_large",
			action: audit.ActionUploadChunk, target: "upload-7",
			wantBody: func(t *testing.T, rr *httptest.ResponseRecorder) {
				t.Helper()
				var body struct {
					ID         string `json:"id"`
					FileOffset int64  `json:"file_offset"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode chunk body %q: %v", rr.Body.String(), err)
				}
				if body.ID != "upload-7" || body.FileOffset != smallBody {
					t.Fatalf("chunk body = %+v, want id upload-7 and file_offset %d", body, smallBody)
				}
			},
		},
	}
	// A body inside the cap keeps its existing contract: 200, the documented
	// envelope, and one audit row. These are the rows the oversized assertions
	// below are measured against, so they have to land first.
	for _, s := range stubs {
		t.Run(s.name+" accepts a body inside the cap", func(t *testing.T) {
			rr := serveUploadStub(router, s.method, s.path, s.actor, smallBody)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
			}
			s.wantBody(t, rr)
		})
	}

	// DELETE /bots/{botUserID}/icon never had a body to drain, but it reads the
	// same URL parameter for its audit target, so it pins the parameter name
	// alongside the upload path.
	deleteRecorder := serveUploadStub(router, http.MethodDelete, "/bots/bot-alpha/icon", "sysadmin", 0)
	if deleteRecorder.Code != http.StatusOK {
		t.Fatalf("delete bot icon: status = %d, want 200 (body %s)", deleteRecorder.Code, deleteRecorder.Body.String())
	}
	assertStatusOKEnvelope(t, deleteRecorder)

	// LogAsync writes on its own goroutine; observe every success row (and the
	// target it recorded) before asserting that the refusals add none. Each
	// wait is its own subtest so one wrong target doesn't hide the rest.
	wantRows := []struct{ action, target string }{
		{audit.ActionTeamImageUpload, "team-main"},
		{audit.ActionBotIconUpload, "bot-alpha"},
		{audit.ActionBotIconDelete, "bot-alpha"},
		{audit.ActionUploadChunk, "upload-7"},
	}
	for _, want := range wantRows {
		t.Run(want.action+" audit row names its subject", func(t *testing.T) {
			waitForAuditRow(t, ctx, db, want.action, want.target)
		})
	}

	// Now overrun each cap by a single byte. Nothing is stored, so the only
	// observable difference between "received" and "refused" is the answer.
	for _, s := range stubs {
		t.Run(s.name+" refuses a body past the cap", func(t *testing.T) {
			rr := serveUploadStub(router, s.method, s.path, s.actor, s.limit+1)
			if rr.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413 (body %s)", rr.Code, rr.Body.String())
			}
			if id := decodeErrorID(t, rr); id != s.errID {
				t.Fatalf("error id = %q, want %q", id, s.errID)
			}
			if strings.Contains(rr.Body.String(), "file_offset") {
				t.Fatalf("refusal body %s must not hand back a resume offset", rr.Body.String())
			}
		})
	}

	// Watch past LogAsync's three-second database timeout: a refused request
	// must leave the audit log holding only the successful one.
	deadline := time.Now().Add(3200 * time.Millisecond)
	for {
		for _, want := range wantRows {
			var count int
			if err := db.Pool.QueryRow(ctx,
				`SELECT count(*) FROM audit_logs WHERE action=$1`, want.action).Scan(&count); err != nil {
				t.Fatalf("count %s audit rows: %v", want.action, err)
			}
			if count != 1 {
				t.Fatalf("%s audit rows = %d, want only the accepted request", want.action, count)
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// serveUploadStub drives the router with a body of exactly size bytes. The
// body streams rather than materializing, so the 50MB chunk cap costs one
// buffer instead of 50MB of heap under -race.
func serveUploadStub(router http.Handler, method, path, actor string, size int64) *httptest.ResponseRecorder {
	var body io.Reader
	if size > 0 {
		body = io.LimitReader(uploadStubFiller{}, size)
	}
	r := httptest.NewRequest(method, path, body)
	r.Header.Set("X-Test-Actor", actor)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, r)
	return rr
}

// uploadStubFiller is an endless source of a single repeated byte — enough to
// push a drain past its cap without holding the payload in memory.
type uploadStubFiller struct{}

func (uploadStubFiller) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func assertStatusOKEnvelope(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode envelope %q: %v", rr.Body.String(), err)
	}
	if body.Status != "OK" {
		t.Fatalf("body = %s, want {\"status\":\"OK\"}", rr.Body.String())
	}
}

func waitForAuditRow(t *testing.T, ctx context.Context, db *store.DB, action, target string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var count int
		if err := db.Pool.QueryRow(ctx,
			`SELECT count(*) FROM audit_logs WHERE action=$1 AND target=$2`, action, target).Scan(&count); err != nil {
			t.Fatalf("count %s audit rows: %v", action, err)
		}
		if count == 1 {
			return
		}
		if time.Now().After(deadline) {
			var observed string
			_ = db.Pool.QueryRow(ctx,
				`SELECT coalesce(string_agg(coalesce(quote_literal(target), 'NULL'), ','), '<no rows>')
				 FROM audit_logs WHERE action=$1`,
				action).Scan(&observed)
			t.Fatalf("%s audit row with target %q never arrived (observed targets: %s)", action, target, observed)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
