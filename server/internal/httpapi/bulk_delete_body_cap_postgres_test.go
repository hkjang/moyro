package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
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
)

// bulkDeleteUsers is the one *write* batch in this package that decoded its
// body with `_ = decodeCappedBody`. request_body.go:26-30 states the rule the
// batch endpoints are supposed to follow — "silently dropping half of a write
// would be worse than refusing it, so the write batches reject instead" — and
// this handler broke it in the worst direction: a body over
// collectionBodyMaxBytes, or a truncated one, left body.UserIDs nil and the
// handler answered 200 {"status":"OK","count":0}.
//
// An admin tool reads that 200 as "the bulk delete was processed". It was not:
// none of the promised per-id audit.ActionUserBulkDelete rows exist, and the
// sender has no reason to retry. The resource cap is only half a defense if
// tripping it is reported as success.
//
// So the refusal has to arrive as a status the caller can act on — 413 when the
// body outgrew the cap, 400 when it was malformed — and it has to arrive
// *before* the audit loop, the same ordering the 2026-09-27 upload-stub fix
// settled on.
//
// The one thing that must not move is the bodyless call. json.Decode answers
// io.EOF for an empty body, and today's `_ =` swallows it, so
// `DELETE /api/v4/users` with no body gets 200 count:0. Whether any caller
// relies on that is unknown, so it is preserved deliberately and pinned here —
// substituting decodeCollectionBody outright would have turned it into a 400.
func TestBulkDeleteUsersRefusesUndecodableBody(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	// user-admin holds system_admin, so it clears both the requireRole group
	// middleware and the handler's own callerIsSystemAdmin check. user-plain
	// holds neither and pins the untouched 403.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES ('user-admin', 'user-admin', 'admin@example.test', 'hash', 'system_user system_admin', 1, 1),
		       ('user-plain', 'user-plain', 'plain@example.test', 'hash', 'system_user', 1, 1)
	`); err != nil {
		t.Fatalf("seed bulk delete actors: %v", err)
	}

	h := &handlers{
		auth:  auth.New(db, []byte("bulk-delete-body-cap-signing-key"), time.Hour, nil),
		audit: audit.New(db, slog.Default()),
	}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor := r.Header.Get("X-Test-Actor")
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey, actor)))
		})
	})
	// Group + patterns copied character for character from router.go:997-1001,
	// so both registered methods run through the real requireRole middleware.
	router.Group(func(r chi.Router) {
		r.Use(h.requireRole("system_admin"))
		r.Post("/users/bulk_delete", h.bulkDeleteUsers)
		r.Delete("/users", h.bulkDeleteUsers)
	})

	// call issues one request through the production router wiring. body == nil
	// means a genuinely bodyless request (http.NoBody, Content-Length 0).
	call := func(t *testing.T, method, path, actor string, body *string) *httptest.ResponseRecorder {
		t.Helper()
		var req *http.Request
		if body == nil {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(*body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-Test-Actor", actor)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}
	assertError := func(t *testing.T, rr *httptest.ResponseRecorder, wantStatus int, wantID string) {
		t.Helper()
		if rr.Code != wantStatus {
			t.Fatalf("status = %d, want %d (body %s)", rr.Code, wantStatus, strings.TrimSpace(rr.Body.String()))
		}
		var got apiError
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode error envelope %q: %v", rr.Body.String(), err)
		}
		if got.ID != wantID {
			t.Fatalf("error id = %q, want %q (body %s)", got.ID, wantID, strings.TrimSpace(rr.Body.String()))
		}
		if got.StatusCode != wantStatus {
			t.Fatalf("envelope status_code = %d, want %d", got.StatusCode, wantStatus)
		}
	}
	countRefusedAuditRows := func(t *testing.T) int {
		t.Helper()
		var count int
		if err := db.Pool.QueryRow(ctx,
			`SELECT count(*) FROM audit_logs WHERE action=$1 AND target LIKE 'refused-%'`,
			audit.ActionUserBulkDelete).Scan(&count); err != nil {
			t.Fatalf("count refused bulk delete audit rows: %v", err)
		}
		return count
	}

	// oversizedBody carries 150 ids — under maxBulkItems, so the only reason to
	// refuse it is the byte cap, and the expected answer is unambiguously 413
	// rather than the pre-existing 400 too_many.
	oversizedBody := func(prefix string) string {
		var b strings.Builder
		b.WriteString(`{"user_ids":[`)
		for i := 0; i < 150; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, "%q", prefix+strings.Repeat("x", 8000)+fmt.Sprint(i))
		}
		b.WriteString(`]}`)
		if b.Len() <= collectionBodyMaxBytes {
			t.Fatalf("oversized fixture is %d bytes, want more than %d", b.Len(), collectionBodyMaxBytes)
		}
		return b.String()
	}

	t.Run("an oversized body on POST bulk_delete is refused as too large", func(t *testing.T) {
		body := oversizedBody("over-")
		rr := call(t, http.MethodPost, "/users/bulk_delete", "user-admin", &body)
		assertError(t, rr, http.StatusRequestEntityTooLarge, "api.user.bulk_delete.invalid_body")
	})

	t.Run("an oversized body on DELETE users is refused as too large", func(t *testing.T) {
		body := oversizedBody("over-")
		rr := call(t, http.MethodDelete, "/users", "user-admin", &body)
		assertError(t, rr, http.StatusRequestEntityTooLarge, "api.user.bulk_delete.invalid_body")
	})

	t.Run("a malformed body is refused as a bad request", func(t *testing.T) {
		body := `{"user_ids":[`
		rr := call(t, http.MethodPost, "/users/bulk_delete", "user-admin", &body)
		assertError(t, rr, http.StatusBadRequest, "api.user.bulk_delete.invalid_body")
	})

	t.Run("a bodyless request still reports an empty batch", func(t *testing.T) {
		rr := call(t, http.MethodDelete, "/users", "user-admin", nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", rr.Code, strings.TrimSpace(rr.Body.String()))
		}
		if got := rr.Body.String(); got != "{\"count\":0,\"status\":\"OK\"}\n" {
			t.Fatalf("body = %q, want the unchanged empty-batch envelope", got)
		}
	})

	t.Run("a valid batch still audits every id", func(t *testing.T) {
		body := `{"user_ids":["bulk-one","bulk-two"]}`
		rr := call(t, http.MethodPost, "/users/bulk_delete", "user-admin", &body)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", rr.Code, strings.TrimSpace(rr.Body.String()))
		}
		if got := rr.Body.String(); got != "{\"count\":2,\"status\":\"OK\"}\n" {
			t.Fatalf("body = %q, want the unchanged success envelope", got)
		}
		// Positive control for the next subtest: proves this fixture does
		// observe audit rows, so "no rows" there means the write never ran.
		waitForAuditRow(t, ctx, db, audit.ActionUserBulkDelete, "bulk-one")
		waitForAuditRow(t, ctx, db, audit.ActionUserBulkDelete, "bulk-two")
	})

	t.Run("a refused batch leaves no audit row", func(t *testing.T) {
		if before := countRefusedAuditRows(t); before != 0 {
			t.Fatalf("refused-prefixed audit rows before the refusals = %d, want 0", before)
		}
		oversized := oversizedBody("refused-")
		if rr := call(t, http.MethodPost, "/users/bulk_delete", "user-admin", &oversized); rr.Code == http.StatusOK {
			t.Fatalf("oversized body answered 200 %s", strings.TrimSpace(rr.Body.String()))
		}
		malformed := `{"user_ids":["refused-malformed"`
		if rr := call(t, http.MethodPost, "/users/bulk_delete", "user-admin", &malformed); rr.Code == http.StatusOK {
			t.Fatalf("malformed body answered 200 %s", strings.TrimSpace(rr.Body.String()))
		}
		// audit.LogAsync is a goroutine with a 3s context, so a row that is
		// coming would land inside this window.
		deadline := time.Now().Add(3200 * time.Millisecond)
		for time.Now().Before(deadline) {
			if count := countRefusedAuditRows(t); count != 0 {
				t.Fatalf("refused-request audit rows = %d, want none", count)
			}
			time.Sleep(50 * time.Millisecond)
		}
	})

	t.Run("an over-long batch is still too many items", func(t *testing.T) {
		ids := make([]string, maxBulkItems+1)
		for i := range ids {
			ids[i] = fmt.Sprintf("many-%d", i)
		}
		encoded, err := json.Marshal(map[string]any{"user_ids": ids})
		if err != nil {
			t.Fatalf("encode over-long batch: %v", err)
		}
		body := string(encoded)
		if len(body) > collectionBodyMaxBytes {
			t.Fatalf("over-long fixture is %d bytes, which the byte cap would refuse first", len(body))
		}
		rr := call(t, http.MethodPost, "/users/bulk_delete", "user-admin", &body)
		assertError(t, rr, http.StatusBadRequest, "api.user.bulk_delete.too_many")
	})

	t.Run("a non admin is still refused before the body is read", func(t *testing.T) {
		body := `{"user_ids":["bulk-three"]}`
		rr := call(t, http.MethodPost, "/users/bulk_delete", "user-plain", &body)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body %s)", rr.Code, strings.TrimSpace(rr.Body.String()))
		}
		var got apiError
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode error envelope %q: %v", rr.Body.String(), err)
		}
		if got.ID != "api.context.permissions.app_error" {
			t.Fatalf("error id = %q, want api.context.permissions.app_error", got.ID)
		}
	})
}
