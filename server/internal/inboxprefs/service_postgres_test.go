package inboxprefs

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const inboxPreferencesTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

func TestInboxPreferencesPostgresConcurrentPartialPatchesPreserveBothChanges(t *testing.T) {
	db := newInboxPreferencesTestDB(t)
	ctx := inboxPreferencesTestContext(t)
	userID := "inbox-user-" + uuid.NewString()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES ($1, $1, $2, 'hash', 'system_user', 1, 1)
	`, userID, userID+"@example.test"); err != nil {
		t.Fatalf("seed inbox user: %v", err)
	}

	// Hold the same advisory key used by Patch until both goroutines have
	// started. This forces the absent-row path to queue concurrent partial
	// updates before either can derive a replacement document.
	blocker, err := db.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire advisory-lock connection: %v", err)
	}
	defer blocker.Release()
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, $2))`, userID, inboxPreferenceLockSeed); err != nil {
		t.Fatalf("hold preference advisory lock: %v", err)
	}
	lockHeld := true
	defer func() {
		if !lockHeld {
			return
		}
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = blocker.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1, $2))`, userID, inboxPreferenceLockSeed)
	}()

	service := New(db)
	bundle := BundleType
	workHoursEnabled := true
	start := make(chan struct{})
	started := make(chan struct{}, 2)
	errs := make(chan error, 2)
	for _, patch := range []Patch{
		{BundleBy: &bundle},
		{WorkHoursEnabled: &workHoursEnabled},
	} {
		patch := patch
		go func() {
			<-start
			started <- struct{}{}
			_, patchErr := service.Patch(ctx, userID, patch)
			errs <- patchErr
		}()
	}
	close(start)
	<-started
	<-started
	var lockKey int64
	if err := blocker.QueryRow(ctx, `SELECT hashtextextended($1, $2)`, userID, inboxPreferenceLockSeed).Scan(&lockKey); err != nil {
		t.Fatalf("resolve preference advisory key: %v", err)
	}
	classID := int64(uint32(uint64(lockKey) >> 32))
	objectID := int64(uint32(uint64(lockKey)))
	waitDeadline := time.NewTimer(5 * time.Second)
	waitPoll := time.NewTicker(10 * time.Millisecond)
	defer waitDeadline.Stop()
	defer waitPoll.Stop()
	for {
		var waiters int
		if err := blocker.QueryRow(ctx, `
			SELECT count(*) FROM pg_locks
			WHERE locktype='advisory' AND classid::bigint=$1 AND objid::bigint=$2
			  AND objsubid=1 AND granted=FALSE
		`, classID, objectID).Scan(&waiters); err != nil {
			t.Fatalf("count preference lock waiters: %v", err)
		}
		if waiters >= 2 {
			break
		}
		select {
		case <-waitDeadline.C:
			t.Fatal("concurrent preference patches did not both wait on the per-user lock")
		case <-waitPoll.C:
		}
	}
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended($1, $2))`, userID, inboxPreferenceLockSeed); err != nil {
		t.Fatalf("release preference advisory lock: %v", err)
	}
	lockHeld = false
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent partial patch: %v", err)
		}
	}

	got, err := service.Get(ctx, userID)
	if err != nil {
		t.Fatalf("get patched preferences: %v", err)
	}
	if got.BundleBy != BundleType || !got.WorkHoursEnabled {
		t.Fatalf("concurrent patches lost a field: %#v", got)
	}
}

func newInboxPreferencesTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(inboxPreferencesTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", inboxPreferencesTestPostgresDSN)
	}
	ctx := inboxPreferencesTestContext(t)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open inbox-preference test admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping inbox-preference test PostgreSQL: %v", err)
	}
	schemaName := "moyro_inbox_preferences_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create inbox-preference test schema: %v", err)
	}
	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop inbox-preference test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse inbox-preference test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 5
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated inbox-preference test pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated inbox-preference test pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate inbox-preference test schema: %v", err)
	}
	return db
}

func inboxPreferencesTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	return ctx
}
