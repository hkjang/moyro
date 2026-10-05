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
	"github.com/hkjang/moyro/server/internal/customprofile"
	"github.com/hkjang/moyro/server/internal/store"
)

// The two custom-profile-value PATCH handlers decoded their map-shaped body
// with `_ = decodeCappedBody`, throwing the decode error away. A body over
// collectionBodyMaxBytes, or one truncated mid-JSON, therefore left `values`
// nil, and customprofile.Service.PatchUserValues returns before it opens a
// transaction when the map is empty — so nothing was written at all.
//
// What made this worse than the 2026-10-04 bulkDeleteUsers case is the
// read-back: both handlers finish by re-reading the stored map and answering
// 200 with it. The caller got 200 plus a plausibly populated profile map —
// byte-for-byte what a successful save returns — while the attributes it just
// submitted were silently dropped. The `{"count":0}` audit row recorded that
// false success, and patchUserCustomProfileValues is the admin-backfills-
// someone-else's-profile path, so the ledger lied about an operator action too.
//
// The refusal has to be a status the caller can act on — 413 over the cap, 400
// malformed — and it has to land *before* h.audit.LogAsync, the ordering the
// 2026-09-27 upload-stub fix settled on.
//
// Three behaviors must not move, and are pinned below: a bodyless PATCH stays
// 200 with the current map (json.Decode answers io.EOF there, which
// decodeOptionalCollectionBody tolerates and decodeCollectionBody would have
// turned into a 400), a literal `null` or `{}` body stays a 200 no-op, and a
// `null` *value* still deletes that field's row.
func TestCustomProfileValuesPatchRefusesUndecodableBody(t *testing.T) {
	// A dedicated database: sibling tests in this package drop and rename
	// tables to inject faults, so sharing a schema would cross-contaminate.
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	seedSidebarHandlerFixture(t, ctx, db)
	// user-audit-ok is the positive control for the audit assertions; it needs
	// a real users row because a successful patch inserts against a FK.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES ('user-audit-ok', 'user-audit-ok', 'audit-ok@example.test', 'hash', 'system_user', 1, 1);
		INSERT INTO custom_profile_fields (id, name, type, target_id, target_type, attrs, sort_order, create_at, update_at, delete_at)
		VALUES ('field-dept', 'Department', 'text', '', 'user', '{}'::jsonb, 1, 1, 1, 0)
	`); err != nil {
		t.Fatalf("seed custom profile fixture: %v", err)
	}

	h := &handlers{
		customProf: customprofile.New(db),
		audit:      audit.New(db, slog.Default()),
	}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor := r.Header.Get("X-Test-Actor")
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey, actor)))
		})
	})
	// Patterns copied from router.go:1014-1016. Neither route sits in an admin
	// group; actor == target clears requireUserParamAccess without an auth
	// service, which is how the sibling error test drives the same handlers.
	router.Patch("/custom_profile_attributes/values", h.patchCustomProfileValuesGlobal)
	router.Patch("/users/{userID}/custom_profile_attributes", h.patchUserCustomProfileValues)

	// call issues one PATCH through the router. body == nil means a genuinely
	// bodyless request, which is the case json.Decode answers io.EOF for.
	call := func(t *testing.T, path, actor string, body *string) *httptest.ResponseRecorder {
		t.Helper()
		var req *http.Request
		if body == nil {
			req = httptest.NewRequest(http.MethodPatch, path, http.NoBody)
		} else {
			req = httptest.NewRequest(http.MethodPatch, path, strings.NewReader(*body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-Test-Actor", actor)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}
	// assertRefused checks the status, the error id, and — the part that
	// separates this defect from an ordinary miscoded status — that the body is
	// an error envelope rather than the profile map a real save echoes back.
	assertRefused := func(t *testing.T, rr *httptest.ResponseRecorder, wantStatus int) {
		t.Helper()
		if rr.Code != wantStatus {
			t.Fatalf("status = %d, want %d (body %s)", rr.Code, wantStatus, strings.TrimSpace(rr.Body.String()))
		}
		var got apiError
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode error envelope %q: %v", rr.Body.String(), err)
		}
		if got.ID != "api.custom_profile.values.patch.invalid_body" {
			t.Fatalf("error id = %q, want api.custom_profile.values.patch.invalid_body (body %s)",
				got.ID, strings.TrimSpace(rr.Body.String()))
		}
		if got.StatusCode != wantStatus {
			t.Fatalf("envelope status_code = %d, want %d", got.StatusCode, wantStatus)
		}
		if strings.Contains(rr.Body.String(), "field-dept") {
			t.Fatalf("refusal body %q looks like a profile values map", strings.TrimSpace(rr.Body.String()))
		}
	}
	storedDept := func(t *testing.T, user string) string {
		t.Helper()
		var raw []byte
		switch err := db.Pool.QueryRow(ctx,
			`SELECT value FROM custom_profile_values WHERE user_id=$1 AND field_id='field-dept'`,
			user).Scan(&raw); {
		case err == nil:
			return string(raw)
		case strings.Contains(err.Error(), "no rows"):
			return "<no row>"
		default:
			t.Fatalf("read stored value for %s: %v", user, err)
			return ""
		}
	}

	// One long value rather than many keys: these two routes have no element
	// count guard (tooManyBatchItems has a single caller, bulkDeleteUsers), so
	// the byte cap is the only reason to refuse and 413 is the only right
	// answer.
	oversizedBody := `{"field-dept":"` + strings.Repeat("x", collectionBodyMaxBytes+4096) + `"}`
	if len(oversizedBody) <= collectionBodyMaxBytes {
		t.Fatalf("oversized fixture is %d bytes, want more than %d", len(oversizedBody), collectionBodyMaxBytes)
	}
	truncatedBody := `{"field-dept":"Support`

	endpoints := []struct {
		name  string
		path  string
		actor string
	}{
		// /users/me/... exercises the alias requireUserParamAccess resolves to
		// the caller, which is the path the webapp uses.
		{"global", "/custom_profile_attributes/values", "user-a"},
		{"user", "/users/me/custom_profile_attributes", "user-a"},
	}

	for _, ep := range endpoints {
		t.Run(ep.name+" patch refuses an oversized body", func(t *testing.T) {
			body := oversizedBody
			assertRefused(t, call(t, ep.path, ep.actor, &body), http.StatusRequestEntityTooLarge)
			if got := storedDept(t, "user-a"); got != "<no row>" {
				t.Fatalf("stored value = %s, want nothing written", got)
			}
		})
		t.Run(ep.name+" patch refuses a truncated body", func(t *testing.T) {
			body := truncatedBody
			assertRefused(t, call(t, ep.path, ep.actor, &body), http.StatusBadRequest)
			if got := storedDept(t, "user-a"); got != "<no row>" {
				t.Fatalf("stored value = %s, want nothing written", got)
			}
		})
	}

	// A real save, to establish the response shape the refusals above must not
	// be able to imitate.
	t.Run("a valid patch writes the value and echoes it back", func(t *testing.T) {
		body := `{"field-dept":"Support"}`
		rr := call(t, "/custom_profile_attributes/values", "user-a", &body)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", rr.Code, strings.TrimSpace(rr.Body.String()))
		}
		if got := rr.Body.String(); got != "{\"field-dept\":\"Support\"}\n" {
			t.Fatalf("body = %q, want the unchanged success envelope", got)
		}
		if got := storedDept(t, "user-a"); got != `"Support"` {
			t.Fatalf("stored value = %s, want \"Support\"", got)
		}
	})

	// With a value stored, a swallowed refusal is byte-identical to the save
	// above — this is the subtest that shows why the old behavior could not be
	// detected by a client.
	for _, ep := range endpoints {
		t.Run(ep.name+" patch refuses an oversized body without clobbering the stored map", func(t *testing.T) {
			body := oversizedBody
			assertRefused(t, call(t, ep.path, ep.actor, &body), http.StatusRequestEntityTooLarge)
			if got := storedDept(t, "user-a"); got != `"Support"` {
				t.Fatalf("stored value = %s, want the untouched \"Support\"", got)
			}
		})

		// The three tolerated shapes. All of them reach the read-back and
		// answer with the current map exactly as they do today.
		t.Run(ep.name+" patch with no body still answers with the current map", func(t *testing.T) {
			rr := call(t, ep.path, ep.actor, nil)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rr.Code, strings.TrimSpace(rr.Body.String()))
			}
			if got := rr.Body.String(); got != "{\"field-dept\":\"Support\"}\n" {
				t.Fatalf("body = %q, want the current map", got)
			}
		})
		for _, shape := range []struct {
			label string
			body  string
		}{{"a null body", "null"}, {"an empty object body", "{}"}} {
			t.Run(ep.name+" patch with "+shape.label+" is still a no-op", func(t *testing.T) {
				body := shape.body
				rr := call(t, ep.path, ep.actor, &body)
				if rr.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200 (body %s)", rr.Code, strings.TrimSpace(rr.Body.String()))
				}
				if got := rr.Body.String(); got != "{\"field-dept\":\"Support\"}\n" {
					t.Fatalf("body = %q, want the unchanged map", got)
				}
				if got := storedDept(t, "user-a"); got != `"Support"` {
					t.Fatalf("stored value = %s, want \"Support\"", got)
				}
			})
		}
	}

	// PatchUserValues treats a null *value* as a delete. That contract runs
	// through the same decode path, so it is pinned here too.
	t.Run("a null value still deletes the field row", func(t *testing.T) {
		body := `{"field-dept":null}`
		rr := call(t, "/users/me/custom_profile_attributes", "user-a", &body)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", rr.Code, strings.TrimSpace(rr.Body.String()))
		}
		if got := rr.Body.String(); got != "{}\n" {
			t.Fatalf("body = %q, want an empty map after the delete", got)
		}
		if got := storedDept(t, "user-a"); got != "<no row>" {
			t.Fatalf("stored value = %s, want the row deleted", got)
		}
	})

	// The audit assertions. h.audit is a real service here — leaving it nil, as
	// the sibling tests in this package do, would skip LogAsync entirely and
	// make "no audit row" pass for the wrong reason.
	countAuditRowsFor := func(t *testing.T, actorPattern string) int {
		t.Helper()
		var count int
		if err := db.Pool.QueryRow(ctx,
			`SELECT count(*) FROM audit_logs WHERE action=$1 AND actor_id LIKE $2`,
			audit.ActionCustomValuesPatch, actorPattern).Scan(&count); err != nil {
			t.Fatalf("count custom profile patch audit rows: %v", err)
		}
		return count
	}

	t.Run("a valid patch leaves an audit row", func(t *testing.T) {
		body := `{"field-dept":"Billing"}`
		rr := call(t, "/users/user-audit-ok/custom_profile_attributes", "user-audit-ok", &body)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", rr.Code, strings.TrimSpace(rr.Body.String()))
		}
		// Positive control for the next subtest: proves this fixture does
		// observe audit rows, so zero rows there means the log never ran.
		waitForAuditRow(t, ctx, db, audit.ActionCustomValuesPatch, "user-audit-ok")
	})

	t.Run("a refused patch leaves no audit row", func(t *testing.T) {
		if before := countAuditRowsFor(t, "user-refused-%"); before != 0 {
			t.Fatalf("refused-actor audit rows before the refusals = %d, want 0", before)
		}
		for _, tc := range []struct {
			path  string
			actor string
			body  string
		}{
			{"/custom_profile_attributes/values", "user-refused-global", oversizedBody},
			{"/custom_profile_attributes/values", "user-refused-global", truncatedBody},
			{"/users/user-refused-user/custom_profile_attributes", "user-refused-user", oversizedBody},
			{"/users/user-refused-user/custom_profile_attributes", "user-refused-user", truncatedBody},
		} {
			body := tc.body
			if rr := call(t, tc.path, tc.actor, &body); rr.Code == http.StatusOK {
				t.Fatalf("%s answered 200 %s", tc.path, strings.TrimSpace(rr.Body.String()))
			}
		}
		// audit.LogAsync is a goroutine with a 3s context, so a row that was
		// coming would land inside this window.
		deadline := time.Now().Add(3200 * time.Millisecond)
		for time.Now().Before(deadline) {
			if count := countAuditRowsFor(t, "user-refused-%"); count != 0 {
				t.Fatalf("refused-request audit rows = %d, want none", count)
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
}
