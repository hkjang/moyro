package httpapi

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

func TestPostgresOperationsSnapshotUsesDurableEvidence(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate operations schema: %v", err)
	}
	now := time.Now().UnixMilli()
	seedStatements := []string{
		`INSERT INTO users (id, username, email, password_hash, create_at, update_at)
		 VALUES ('ops-user', 'ops-user', 'ops@example.test', 'unused', $1, $1)`,
		`INSERT INTO teams (id, name, display_name, create_at, update_at)
		 VALUES ('ops-team', 'ops-team', 'Operations', $1, $1)`,
		`INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		 VALUES ('ops-channel', 'ops-team', 'O', 'Operations', 'operations', $1, $1)`,
		`INSERT INTO scheduled_posts
			(id, user_id, channel_id, message, send_at, create_at, sent_at, status, next_attempt_at)
		 VALUES ('ops-scheduled-dead', 'ops-user', 'ops-channel', 'failed', $1, $1, -2, 'dead', 0)`,
		`INSERT INTO file_infos
			(id, user_id, path, name, size, create_at, update_at)
		 VALUES ('ops-file', 'ops-user', 'opaque/path', 'status.txt', 1536, $1, $1)`,
		`INSERT INTO integration_deliveries
			(id, event_id, integration_id, callback_url, payload, status, next_attempt_at, create_at, update_at)
		 VALUES
			('ops-retry', 'event-retry', 'hook-1', 'https://example.test/retry', '{}'::jsonb, 'retry', $1, $1, $1),
			('ops-dead', 'event-dead', 'hook-1', 'https://example.test/dead', '{}'::jsonb, 'dead', 0, $1, $1)`,
	}
	for _, statement := range seedStatements {
		if _, err := db.Pool.Exec(ctx, statement, now); err != nil {
			t.Fatalf("seed operations state: %v", err)
		}
	}

	reader := newPostgresOperationsReader(db, fileStorageRuntime{
		ConfiguredBackend: "fs",
		ActiveBackend:     "fs",
		FilesystemRoot:    t.TempDir(),
	})
	snapshot := reader.Snapshot(ctx)

	if snapshot.Database.State != operationalReady || snapshot.Database.Migration.AppliedVersion != snapshot.Database.Migration.TargetVersion {
		t.Fatalf("database status = %#v", snapshot.Database)
	}
	if snapshot.Workers.State != operationalWarning || snapshot.Workers.Scheduled.Dead != 1 || snapshot.Workers.RuntimeObservable {
		t.Fatalf("worker status = %#v", snapshot.Workers)
	}
	if snapshot.Webhooks.State != operationalWarning || snapshot.Webhooks.Retry != 1 || snapshot.Webhooks.Dead != 1 || snapshot.Webhooks.RuntimeObservable {
		t.Fatalf("webhook status = %#v", snapshot.Webhooks)
	}
	if snapshot.Storage.State != operationalReady || snapshot.Storage.FileCount != 1 || snapshot.Storage.Bytes != 1536 {
		t.Fatalf("storage status = %#v", snapshot.Storage)
	}
}

func TestStorageFallbackIsWarningEvenWhenFilesystemExists(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	reader := newPostgresOperationsReader(db, fileStorageRuntime{
		ConfiguredBackend: "s3", ActiveBackend: "fs", Fallback: true, FilesystemRoot: t.TempDir(),
	})
	storageStatus := reader.Snapshot(ctx).Storage
	if storageStatus.State != operationalWarning || !storageStatus.Fallback || storageStatus.ActiveBackend != "fs" {
		t.Fatalf("fallback status = %#v", storageStatus)
	}
}

func newOperationsTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("MOYRO_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("MOYRO_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatal(err)
	}
	schemaName := "moyro_operations_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		adminPool.Close()
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 4
	testPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		adminPool.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop operations schema: %v", err)
		}
		adminPool.Close()
	})
	return &store.DB{Pool: testPool}
}
