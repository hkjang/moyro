package files

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const filesTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

func TestAssociateWithPostReturnsCanonicalRequestedSubsetOnReplay(t *testing.T) {
	db := newFilesTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, create_at, update_at) VALUES
			('file-owner', 'file-owner', 'owner@example.test', 'hash', 1, 1),
			('file-other', 'file-other', 'other@example.test', 'hash', 1, 1);
		INSERT INTO teams (id, name, display_name, create_at, update_at)
			VALUES ('file-team', 'file-team', 'File Team', 1, 1);
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
			VALUES ('file-channel', 'file-team', 'O', 'File Channel', 'file-channel', 1, 1);
		INSERT INTO posts (id, channel_id, user_id, message, create_at, update_at)
			VALUES ('file-post', 'file-channel', 'file-owner', 'post', 1, 1);
		INSERT INTO file_infos (id, user_id, post_id, channel_id, path, name, size, create_at, update_at, delete_at) VALUES
			('file-existing', 'file-owner', 'file-post', 'file-channel', 'existing', 'existing', 1, 1, 1, 0),
			('file-new', 'file-owner', NULL, NULL, 'new', 'new', 1, 1, 1, 0),
			('file-foreign', 'file-other', NULL, NULL, 'foreign', 'foreign', 1, 1, 1, 0),
			('file-deleted', 'file-owner', NULL, NULL, 'deleted', 'deleted', 1, 1, 1, 1);
	`); err != nil {
		t.Fatalf("seed file association test: %v", err)
	}

	service := New(db, nil)
	requested := []string{"file-existing", "file-new", "file-new", "file-foreign", "file-deleted"}
	want := []string{"file-existing", "file-new"}
	for attempt := 1; attempt <= 2; attempt++ {
		got, err := service.AssociateWithPost(ctx, "file-owner", requested, "file-post", "file-channel")
		if err != nil {
			t.Fatalf("associate attempt %d: %v", attempt, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("associate attempt %d = %#v, want %#v", attempt, got, want)
		}
	}
}

func newFilesTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(filesTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", filesTestPostgresDSN)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open file test PostgreSQL: %v", err)
	}
	schemaName := "moyro_files_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create file test schema: %v", err)
	}
	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop file test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse file test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated file test pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate file test schema: %v", err)
	}
	return db
}
