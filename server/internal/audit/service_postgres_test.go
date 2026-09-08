package audit

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const auditTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

// TestLogNeverBlocksOrFailsThePrimaryAction pins the package's central promise:
// audit writes are fire-and-forget, so a broken write must not surface to the
// caller that was doing the real work. It also pins the other half of that
// promise — the write really does happen when it can.
func TestLogNeverBlocksOrFailsThePrimaryAction(t *testing.T) {
	db := newAuditTestDB(t)
	ctx := auditTestContext(t)
	service := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))

	service.Log(ctx, "actor-1", ActionUserLogin, "target-1", map[string]any{"ip": "10.0.0.1"})

	// A caller whose request was already cancelled still must not be harmed by
	// the audit write, and Log has no error to return by design.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	service.Log(cancelled, "actor-1", ActionUserLogout, "target-1", nil)

	entries, err := service.List(ctx, 0, "", "")
	if err != nil {
		t.Fatalf("list audit entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("stored %d entries, want only the one whose context was live", len(entries))
	}
	entry := entries[0]
	if entry.ActorID != "actor-1" || entry.Action != ActionUserLogin || entry.Target != "target-1" {
		t.Fatalf("entry = %#v", entry)
	}
	var payload map[string]any
	if err := json.Unmarshal(entry.Payload, &payload); err != nil || payload["ip"] != "10.0.0.1" {
		t.Fatalf("payload = %s (%v)", entry.Payload, err)
	}
	if entry.CreateAt <= 0 {
		t.Fatalf("create_at = %d, want a stamp", entry.CreateAt)
	}
}

// TestLogStoresAbsentActorTargetAndPayloadReadably keeps system-originated
// records legible: an entry with no actor is a real case (a background worker),
// and it must come back as an empty string rather than tripping the scan.
func TestLogStoresAbsentActorTargetAndPayloadReadably(t *testing.T) {
	db := newAuditTestDB(t)
	ctx := auditTestContext(t)
	service := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))

	service.Log(ctx, "", ActionChannelArchive, "", nil)

	entries, err := service.List(ctx, 0, "", "")
	if err != nil || len(entries) != 1 {
		t.Fatalf("list = %#v, %v", entries, err)
	}
	if entries[0].ActorID != "" || entries[0].Target != "" {
		t.Fatalf("absent actor/target came back as %q/%q", entries[0].ActorID, entries[0].Target)
	}
	if string(entries[0].Payload) != "null" {
		t.Fatalf("absent payload = %s, want null", entries[0].Payload)
	}

	var storedActor, storedTarget *string
	if err := db.Pool.QueryRow(ctx, `SELECT actor_id, target FROM audit_logs`).Scan(&storedActor, &storedTarget); err != nil {
		t.Fatalf("read raw row: %v", err)
	}
	if storedActor != nil || storedTarget != nil {
		t.Fatal("empty actor/target were stored as empty strings rather than NULL")
	}
}

// TestLogAsyncEventuallyWrites covers the variant every handler uses, where the
// write outlives the request. A silent failure here loses the audit trail
// without anyone noticing.
func TestLogAsyncEventuallyWrites(t *testing.T) {
	db := newAuditTestDB(t)
	ctx := auditTestContext(t)
	service := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))

	service.LogAsync("actor-async", ActionPostDelete, "post-9", map[string]any{"reason": "spam"})

	deadline := time.Now().Add(10 * time.Second)
	for {
		entries, err := service.List(ctx, 0, "", "actor-async")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(entries) == 1 {
			if entries[0].Action != ActionPostDelete || entries[0].Target != "post-9" {
				t.Fatalf("async entry = %#v", entries[0])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("LogAsync never wrote its entry")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestListReturnsNewestFirstAndHonoursItsFilters covers what the admin audit
// page reads. Ordering matters because the page shows only the first screen:
// getting it backwards hides the most recent activity entirely.
func TestListReturnsNewestFirstAndHonoursItsFilters(t *testing.T) {
	db := newAuditTestDB(t)
	ctx := auditTestContext(t)
	service := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))

	service.Log(ctx, "actor-a", ActionUserLogin, "t1", nil)
	service.Log(ctx, "actor-b", ActionUserLoginFailed, "t2", nil)
	service.Log(ctx, "actor-a", ActionChannelCreate, "t3", nil)
	service.Log(ctx, "actor-b", ActionUserLogout, "t4", nil)

	all, err := service.List(ctx, 0, "", "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if got := actions(all); strings.Join(got, ",") != strings.Join([]string{
		ActionUserLogout, ActionChannelCreate, ActionUserLoginFailed, ActionUserLogin,
	}, ",") {
		t.Fatalf("ordering = %v, want newest first", got)
	}

	byActor, err := service.List(ctx, 0, "", "actor-a")
	if err != nil {
		t.Fatalf("list by actor: %v", err)
	}
	if got := actions(byActor); strings.Join(got, ",") != ActionChannelCreate+","+ActionUserLogin {
		t.Fatalf("actor filter = %v", got)
	}

	byPrefix, err := service.List(ctx, 0, "user.", "")
	if err != nil {
		t.Fatalf("list by prefix: %v", err)
	}
	if got := actions(byPrefix); strings.Join(got, ",") != strings.Join([]string{
		ActionUserLogout, ActionUserLoginFailed, ActionUserLogin,
	}, ",") {
		t.Fatalf("prefix filter = %v", got)
	}

	both, err := service.List(ctx, 0, "user.", "actor-b")
	if err != nil {
		t.Fatalf("list by prefix and actor: %v", err)
	}
	if got := actions(both); strings.Join(got, ",") != ActionUserLogout+","+ActionUserLoginFailed {
		t.Fatalf("combined filter = %v", got)
	}

	if entries, err := service.List(ctx, 0, "", "nobody"); err != nil || len(entries) != 0 {
		t.Fatalf("unknown actor = %#v, %v, want an empty list", entries, err)
	}
}

// TestListClampsItsPageSize keeps an admin-supplied limit from turning the
// audit page into an unbounded table scan, and keeps a missing limit from
// returning nothing.
func TestListClampsItsPageSize(t *testing.T) {
	db := newAuditTestDB(t)
	ctx := auditTestContext(t)
	service := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))

	for i := range 105 {
		service.Log(ctx, "actor-bulk", ActionPostCreate, "post", map[string]any{"n": i})
	}

	for name, limit := range map[string]int{"zero": 0, "negative": -5, "above the cap": 5000} {
		entries, err := service.List(ctx, limit, "", "")
		if err != nil {
			t.Fatalf("list with a %s limit: %v", name, err)
		}
		if len(entries) != 100 {
			t.Fatalf("a %s limit returned %d entries, want the 100-row default", name, len(entries))
		}
	}

	entries, err := service.List(ctx, 7, "", "")
	if err != nil || len(entries) != 7 {
		t.Fatalf("explicit limit returned %d entries (%v), want 7", len(entries), err)
	}
}

func actions(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Action)
	}
	return out
}

func newAuditTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(auditTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", auditTestPostgresDSN)
	}
	ctx := auditTestContext(t)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open audit test admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping audit test PostgreSQL: %v", err)
	}

	schemaName := "moyro_audit_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create audit test schema: %v", err)
	}

	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop audit test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse audit test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 4
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated audit test pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated audit test pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate audit test schema: %v", err)
	}
	return db
}

func auditTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}
