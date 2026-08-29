package store

import (
	"context"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const migrationTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

func TestMigratePostgresFreshInstallAndRestart(t *testing.T) {
	db := newMigrationTestDB(t)
	ctx := testContext(t)
	files := fstest.MapFS{
		"migrations/000001_core.up.sql": {
			Data: []byte("-- moyro:irreversible\nCREATE TABLE fresh_items (id INTEGER PRIMARY KEY, value TEXT NOT NULL);\n"),
		},
		"migrations/000002_add_note.up.sql": {
			Data: []byte("ALTER TABLE fresh_items ADD COLUMN note TEXT NOT NULL DEFAULT '';\n"),
		},
	}

	if err := migrate(ctx, db, files, "v0.1.1-test"); err != nil {
		t.Fatalf("fresh migration: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO fresh_items (id, value, note) VALUES (1, 'preserve-me', 'after-install')`); err != nil {
		t.Fatalf("insert fresh-install sentinel: %v", err)
	}

	var firstAppliedAt int64
	var firstAppVersion string
	var firstIrreversible bool
	if err := db.Pool.QueryRow(ctx, `
SELECT applied_at, app_version, irreversible
FROM schema_migrations
WHERE version = 1`).Scan(&firstAppliedAt, &firstAppVersion, &firstIrreversible); err != nil {
		t.Fatalf("read first migration record: %v", err)
	}
	if firstAppVersion != "v0.1.1-test" || !firstIrreversible {
		t.Fatalf("first migration metadata = app_version %q irreversible %v", firstAppVersion, firstIrreversible)
	}

	// The SQL deliberately lacks IF NOT EXISTS. A successful second call proves
	// the ledger made restart a no-op instead of replaying applied migrations.
	if err := migrate(ctx, db, files, "v0.2.1-test"); err != nil {
		t.Fatalf("restart migration: %v", err)
	}

	var migrationCount int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatalf("count migration records: %v", err)
	}
	if migrationCount != 2 {
		t.Fatalf("migration record count = %d, want 2", migrationCount)
	}

	var restartedAppliedAt int64
	var restartedAppVersion string
	if err := db.Pool.QueryRow(ctx, `
SELECT applied_at, app_version
FROM schema_migrations
WHERE version = 1`).Scan(&restartedAppliedAt, &restartedAppVersion); err != nil {
		t.Fatalf("read migration after restart: %v", err)
	}
	if restartedAppliedAt != firstAppliedAt || restartedAppVersion != firstAppVersion {
		t.Fatalf("restart changed applied record: before=(%d,%q) after=(%d,%q)", firstAppliedAt, firstAppVersion, restartedAppliedAt, restartedAppVersion)
	}

	var value, note string
	if err := db.Pool.QueryRow(ctx, `SELECT value, note FROM fresh_items WHERE id = 1`).Scan(&value, &note); err != nil {
		t.Fatalf("read fresh-install sentinel: %v", err)
	}
	if value != "preserve-me" || note != "after-install" {
		t.Fatalf("fresh-install sentinel = (%q, %q)", value, note)
	}
}

func TestMigratePostgresAdoptsLegacyBaselineAndPreservesData(t *testing.T) {
	db := newMigrationTestDB(t)
	ctx := testContext(t)

	baseline, err := fs.ReadFile(embeddedMigrations, "migrations/000001_v0_1_baseline.up.sql")
	if err != nil {
		t.Fatalf("read embedded v0.1 baseline: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, string(baseline)); err != nil {
		t.Fatalf("create legacy v0.1 schema: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
VALUES ('legacy-user', 'legacy', 'legacy@example.test', 'legacy-hash', 'system_user', 1, 1)`); err != nil {
		t.Fatalf("insert legacy user: %v", err)
	}

	// Administrators can customize built-in roles. Baseline adoption must retain
	// the revision and must not restore a deliberately removed permission.
	if _, err := db.Pool.Exec(ctx, `UPDATE roles SET revision = 2 WHERE id = 'system_user'`); err != nil {
		t.Fatalf("revise legacy role: %v", err)
	}
	tag, err := db.Pool.Exec(ctx, `
DELETE FROM role_permissions
WHERE role_id = 'system_user' AND permission_name = 'manage_own_api_keys'`)
	if err != nil {
		t.Fatalf("customize legacy role permissions: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("removed legacy role permissions = %d, want 1", tag.RowsAffected())
	}

	if err := migrate(ctx, db, embeddedMigrations, "v0.1.1-test"); err != nil {
		t.Fatalf("adopt legacy baseline: %v", err)
	}

	var email string
	if err := db.Pool.QueryRow(ctx, `SELECT email FROM users WHERE id = 'legacy-user'`).Scan(&email); err != nil {
		t.Fatalf("read preserved legacy user: %v", err)
	}
	if email != "legacy@example.test" {
		t.Fatalf("legacy user email = %q", email)
	}

	var roleRevision int64
	if err := db.Pool.QueryRow(ctx, `SELECT revision FROM roles WHERE id = 'system_user'`).Scan(&roleRevision); err != nil {
		t.Fatalf("read preserved role revision: %v", err)
	}
	if roleRevision != 2 {
		t.Fatalf("system_user revision = %d, want 2", roleRevision)
	}
	var permissionCount int
	if err := db.Pool.QueryRow(ctx, `
SELECT count(*)
FROM role_permissions
WHERE role_id = 'system_user' AND permission_name = 'manage_own_api_keys'`).Scan(&permissionCount); err != nil {
		t.Fatalf("count preserved role permissions: %v", err)
	}
	if permissionCount != 0 {
		t.Fatalf("removed permission was restored during baseline adoption")
	}

	var version int64
	var name, appVersion string
	var irreversible bool
	if err := db.Pool.QueryRow(ctx, `
SELECT version, name, app_version, irreversible
FROM schema_migrations
WHERE version = 1`).Scan(&version, &name, &appVersion, &irreversible); err != nil {
		t.Fatalf("read adopted baseline record: %v", err)
	}
	if version != 1 || name != "v0_1_baseline" || appVersion != "v0.1.1-test" || !irreversible {
		t.Fatalf("adopted baseline metadata = (%d, %q, %q, %v)", version, name, appVersion, irreversible)
	}
}

func TestMigratePostgresRollsBackFailedMigrationAndRetries(t *testing.T) {
	db := newMigrationTestDB(t)
	ctx := testContext(t)
	broken := fstest.MapFS{
		"migrations/000001_partial.up.sql": {
			Data: []byte("CREATE TABLE partial_probe (id INTEGER PRIMARY KEY);\nINSERT INTO partial_probe VALUES (1);\nSELECT missing_column FROM partial_probe;\n"),
		},
	}

	err := migrate(ctx, db, broken, "broken-test")
	if err == nil || !strings.Contains(err.Error(), "execute migration 000001_partial") {
		t.Fatalf("broken migration error = %v", err)
	}
	if relationExists(t, ctx, db, "partial_probe") {
		t.Fatal("partial migration table survived rollback")
	}
	var migrationCount int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatalf("count records after failed migration: %v", err)
	}
	if migrationCount != 0 {
		t.Fatalf("failed migration record count = %d, want 0", migrationCount)
	}

	fixed := fstest.MapFS{
		"migrations/000001_partial.up.sql": {
			Data: []byte("CREATE TABLE partial_probe (id INTEGER PRIMARY KEY);\nINSERT INTO partial_probe VALUES (1);\n"),
		},
	}
	if err := migrate(ctx, db, fixed, "fixed-test"); err != nil {
		t.Fatalf("retry fixed migration: %v", err)
	}
	var probeCount int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM partial_probe`).Scan(&probeCount); err != nil {
		t.Fatalf("read retry result: %v", err)
	}
	if probeCount != 1 {
		t.Fatalf("retry result row count = %d, want 1", probeCount)
	}
}

func TestMigratePostgresRefusesChecksumTamperAndFutureVersion(t *testing.T) {
	db := newMigrationTestDB(t)
	ctx := testContext(t)
	files := fstest.MapFS{
		"migrations/000001_core.up.sql": {
			Data: []byte("CREATE TABLE refusal_probe (id INTEGER PRIMARY KEY);\n"),
		},
	}

	if err := migrate(ctx, db, files, "v0.1.1-test"); err != nil {
		t.Fatalf("apply initial migration: %v", err)
	}
	loaded, err := loadMigrations(files)
	if err != nil {
		t.Fatalf("load test migration metadata: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE schema_migrations SET checksum = $1 WHERE version = 1`, strings.Repeat("0", 64)); err != nil {
		t.Fatalf("tamper migration checksum: %v", err)
	}
	if err := migrate(ctx, db, files, "v0.2.1-test"); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("checksum tamper error = %v", err)
	}

	if _, err := db.Pool.Exec(ctx, `UPDATE schema_migrations SET checksum = $1 WHERE version = 1`, loaded[0].Checksum); err != nil {
		t.Fatalf("restore migration checksum: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
INSERT INTO schema_migrations
    (version, name, checksum, applied_at, execution_ms, app_version, irreversible)
VALUES (999999, 'future', $1, 1, 0, 'future-test', FALSE)`, strings.Repeat("f", 64)); err != nil {
		t.Fatalf("insert future migration record: %v", err)
	}
	if err := migrate(ctx, db, files, "v0.2.1-test"); err == nil || !strings.Contains(err.Error(), "unknown migration version 999999") {
		t.Fatalf("future migration error = %v", err)
	}
}

func TestMigratePostgresSerializesConcurrentRunners(t *testing.T) {
	db := newMigrationTestDB(t)
	ctx := testContext(t)
	files := fstest.MapFS{
		"migrations/000001_concurrent.up.sql": {
			Data: []byte("SELECT pg_sleep(0.1);\nCREATE TABLE concurrent_probe (id INTEGER PRIMARY KEY);\n"),
		},
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- migrate(ctx, db, files, "concurrent-test")
		}()
	}
	close(start)

	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent migration: %v", err)
		}
	}

	var migrationCount int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatalf("count concurrent migration records: %v", err)
	}
	if migrationCount != 1 {
		t.Fatalf("concurrent migration record count = %d, want 1", migrationCount)
	}
	if !relationExists(t, ctx, db, "concurrent_probe") {
		t.Fatal("concurrent migration did not create probe table")
	}
}

func newMigrationTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(migrationTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", migrationTestPostgresDSN)
	}

	ctx := testContext(t)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open migration test admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping migration test PostgreSQL: %v", err)
	}

	schemaName := "moyro_migrations_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create isolated migration test schema: %v", err)
	}

	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop isolated migration test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse migration test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 4
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated migration test pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated migration test pool: %v", err)
	}

	var currentSchema string
	if err := testPool.QueryRow(ctx, `SELECT current_schema()`).Scan(&currentSchema); err != nil {
		t.Fatalf("read isolated migration test schema: %v", err)
	}
	if currentSchema != schemaName {
		t.Fatalf("current schema = %q, want %q", currentSchema, schemaName)
	}
	return &DB{Pool: testPool}
}

func relationExists(t *testing.T, ctx context.Context, db *DB, relation string) bool {
	t.Helper()
	var exists bool
	if err := db.Pool.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM pg_catalog.pg_class AS c
    JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
    WHERE n.nspname = current_schema() AND c.relname = $1
)`, relation).Scan(&exists); err != nil {
		t.Fatalf("check relation %q: %v", relation, err)
	}
	return exists
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}
