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
)

// searchFiles and searchTeamFiles walk their result set with a bare
// `for rows.Next()` and never consult rows.Err(). pgx reports a failure that
// arrives *during* iteration — a dropped connection, a server-side error
// raised while the rows are being produced — by ending the loop early and
// parking the error on the Rows; Next() itself just answers false. Both
// handlers therefore answered 200 {"order":[],"file_infos":{}} for an
// outage, which a user reads as "that PDF is not here". The 500 the same
// functions already own (api.file.search.app_error /
// api.file.search.team.app_error) was unreachable for anything but a failure
// to *start* the query.
//
// The `continue` on a Scan error had the same shape one row down: a single
// unreadable row vanished from the answer and the 200 said nothing about it.
//
// This file is the only place in the repository that needed the check: of the
// 43 production files using rows.Next(), 42 already return rows.Err()
// (files/service.go:328, reactions/service.go:62, ...).
//
// The contrast that matters is pinned alongside: a search that genuinely
// matches nothing must still be 200 with an empty result, and a successful
// search must answer byte-for-byte what it answered before. A refused request
// must also leave no file.search audit row, the "refusal precedes the ledger"
// ordering the 2026-09-27 upload-stub fix settled on.
func TestFileSearchRowFaultIsNotAnEmptyResult(t *testing.T) {
	// A dedicated database: sibling tests in this package drop and rename
	// tables to inject faults, so sharing a schema would cross-contaminate.
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	seedSidebarHandlerFixture(t, ctx, db)
	// Two visible files in user-a's channel. "boom" is the row the mid-flight
	// fault below detonates on; both names match the term "report" so the
	// faulty row is inside the filtered set rather than planned away.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO file_infos (id, user_id, post_id, channel_id, path, name, extension, size, mime_type, create_at, update_at, delete_at)
		VALUES ('file-quiet', 'user-a', NULL, 'chan-alpha', '/data/quiet', 'quarterly-report.pdf', 'pdf', 2048, 'application/pdf', 2000, 2000, 0),
		       ('file-boom',  'user-a', NULL, 'chan-alpha', '/data/boom',  'boom-report.pdf',      'pdf', 4096, 'application/pdf', 1000, 1000, 0)
	`); err != nil {
		t.Fatalf("seed file fixture: %v", err)
	}

	h := &handlers{
		auth:  auth.New(db, []byte("file-search-rows-fault-signing-key"), time.Hour, nil),
		audit: audit.New(db, slog.Default()),
	}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor := r.Header.Get("X-Test-Actor")
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey, actor)))
		})
	})
	// Patterns copied verbatim from router.go:970-971.
	router.Post("/files/search", h.searchFiles)
	router.Post("/teams/{teamID}/files/search", h.searchTeamFiles)

	call := func(t *testing.T, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-Actor", "user-a")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}
	// assertFailed wants the status, the error id, and — the part that
	// separates this defect from a miscoded status — an error envelope rather
	// than the empty result set a genuine miss returns.
	assertFailed := func(t *testing.T, rr *httptest.ResponseRecorder, wantID string) {
		t.Helper()
		if rr.Code != 500 {
			t.Fatalf("status = %d, want 500 (body %s)", rr.Code, strings.TrimSpace(rr.Body.String()))
		}
		var got apiError
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode error envelope %q: %v", rr.Body.String(), err)
		}
		if got.ID != wantID {
			t.Fatalf("error id = %q, want %q (body %s)", got.ID, wantID, strings.TrimSpace(rr.Body.String()))
		}
	}
	countAuditRows := func(t *testing.T) int {
		t.Helper()
		var n int
		if err := db.Pool.QueryRow(ctx,
			`SELECT count(*) FROM audit_logs WHERE action=$1`, audit.ActionFileSearch).Scan(&n); err != nil {
			t.Fatalf("count file.search audit rows: %v", err)
		}
		return n
	}

	const globalPath = "/files/search"
	const teamPath = "/teams/team-main/files/search"

	// ---- preserved behaviour -------------------------------------------
	// The successful answer is pinned as a literal so a later change to the
	// error handling cannot quietly reshape the happy path.
	t.Run("a matching search answers with the order and file_infos it always did", func(t *testing.T) {
		const want = `{"file_infos":{"file-boom":{"id":"file-boom","user_id":"user-a","post_id":"","channel_id":"chan-alpha","has_thumbnail":false,"width":0,"height":0,"name":"boom-report.pdf","extension":"","size":4096,"mime_type":"application/pdf","create_at":1000,"update_at":0,"delete_at":0},"file-quiet":{"id":"file-quiet","user_id":"user-a","post_id":"","channel_id":"chan-alpha","has_thumbnail":false,"width":0,"height":0,"name":"quarterly-report.pdf","extension":"","size":2048,"mime_type":"application/pdf","create_at":2000,"update_at":0,"delete_at":0}},"order":["file-quiet","file-boom"]}`
		for _, path := range []string{globalPath, teamPath} {
			rr := call(t, path, `{"terms":"report"}`)
			if rr.Code != 200 {
				t.Fatalf("%s status = %d, want 200 (body %s)", path, rr.Code, strings.TrimSpace(rr.Body.String()))
			}
			if got := strings.TrimSpace(rr.Body.String()); got != want {
				t.Fatalf("%s body = %s\nwant %s", path, got, want)
			}
		}
	})

	t.Run("a search that genuinely matches nothing stays 200 with an empty result", func(t *testing.T) {
		for _, path := range []string{globalPath, teamPath} {
			rr := call(t, path, `{"terms":"no-such-file"}`)
			if rr.Code != 200 {
				t.Fatalf("%s status = %d, want 200 (body %s)", path, rr.Code, strings.TrimSpace(rr.Body.String()))
			}
			if got := strings.TrimSpace(rr.Body.String()); got != `{"file_infos":{},"order":[]}` {
				t.Fatalf("%s body = %s, want {\"file_infos\":{},\"order\":[]}", path, got)
			}
		}
	})

	t.Run("an empty terms string still short-circuits to 200 before the query", func(t *testing.T) {
		for _, path := range []string{globalPath, teamPath} {
			rr := call(t, path, `{"terms":"   "}`)
			if rr.Code != 200 {
				t.Fatalf("%s status = %d, want 200 (body %s)", path, rr.Code, strings.TrimSpace(rr.Body.String()))
			}
			if got := strings.TrimSpace(rr.Body.String()); got != `{"file_infos":{},"order":[]}` {
				t.Fatalf("%s body = %s, want {\"file_infos\":{},\"order\":[]}", path, got)
			}
		}
	})

	// The successful calls above are the positive control for the audit
	// assertion that follows the faults: LogAsync really does reach this
	// schema, so a later count of zero new rows means "refused before the
	// ledger" rather than "audit was never wired".
	waitForAuditCount := func(t *testing.T, want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if got := countAuditRows(t); got == want {
				return
			} else if time.Now().After(deadline) {
				t.Fatalf("file.search audit rows = %d, want %d", got, want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	// Six searches ran above; only the four that reached the query log.
	waitForAuditCount(t, 4)
	auditBaseline := countAuditRows(t)

	// ---- fault 1: the server raises while the rows are being produced ----
	// file_infos becomes a view that divides by zero on the "boom" row. The
	// query starts cleanly — Pool.Query returns no error — and the failure
	// lands on the Rows, which is exactly the shape rows.Err() exists for.
	if _, err := db.Pool.Exec(ctx, `
		ALTER TABLE file_infos RENAME TO file_infos_real;
		CREATE VIEW file_infos AS
			SELECT id, user_id, post_id, channel_id, path, name, extension,
			       size / (CASE WHEN name LIKE '%boom%' THEN 0 ELSE 1 END) AS size,
			       mime_type, create_at, update_at, delete_at, thumbnail_path, width, height
			FROM file_infos_real
	`); err != nil {
		t.Fatalf("install row-iteration fault: %v", err)
	}

	t.Run("a fault raised during iteration is a server error, not an empty global search", func(t *testing.T) {
		assertFailed(t, call(t, globalPath, `{"terms":"report"}`), "api.file.search.app_error")
	})
	t.Run("a fault raised during iteration is a server error, not an empty team search", func(t *testing.T) {
		assertFailed(t, call(t, teamPath, `{"terms":"report"}`), "api.file.search.team.app_error")
	})

	if _, err := db.Pool.Exec(ctx, `
		DROP VIEW file_infos;
		ALTER TABLE file_infos_real RENAME TO file_infos
	`); err != nil {
		t.Fatalf("remove row-iteration fault: %v", err)
	}

	// ---- fault 2: a row that cannot be scanned --------------------------
	// size becomes unparseable text, so rows.Scan fails on the first row.
	// Before the fix that was a `continue`, so the handler answered 200 with
	// the surviving rows — here, none of them.
	//
	// Worth knowing for anyone editing the handlers: this fault is *not*
	// proof that the Scan branch itself returns. pgx marks a Scan failure
	// fatal on the Rows, so replacing the new `return` with the old
	// `continue` leaves these two subtests green — the rows.Err() guard
	// catches it one line later. What these subtests pin is the contract the
	// caller sees, which is what must not regress; the explicit return is
	// there so the handler owns that contract rather than inheriting it from
	// the driver.
	if _, err := db.Pool.Exec(ctx,
		`ALTER TABLE file_infos ALTER COLUMN size TYPE text USING 'not-a-number'`); err != nil {
		t.Fatalf("install scan fault: %v", err)
	}

	t.Run("an unscannable row is a server error, not a silently shortened global search", func(t *testing.T) {
		assertFailed(t, call(t, globalPath, `{"terms":"report"}`), "api.file.search.app_error")
	})
	t.Run("an unscannable row is a server error, not a silently shortened team search", func(t *testing.T) {
		assertFailed(t, call(t, teamPath, `{"terms":"report"}`), "api.file.search.team.app_error")
	})

	t.Run("a refused search leaves no file.search audit row", func(t *testing.T) {
		// LogAsync runs on a goroutine with a 3s timeout; outlast it before
		// concluding that nothing was written.
		time.Sleep(3200 * time.Millisecond)
		if got := countAuditRows(t); got != auditBaseline {
			t.Fatalf("file.search audit rows = %d, want %d (a refused search recorded one)", got, auditBaseline)
		}
	})
}
