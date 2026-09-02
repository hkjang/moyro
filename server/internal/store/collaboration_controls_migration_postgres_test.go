package store

import (
	"io/fs"
	"strconv"
	"testing"
	"testing/fstest"
	"time"
)

func TestCollaborationControlsMigrationBoundsExistingActiveGuestsPostgres(t *testing.T) {
	db := newMigrationTestDB(t)
	ctx := testContext(t)
	if err := migrate(ctx, db, migrationsBeforeCollaborationControls(t), "pre-collaboration-test"); err != nil {
		t.Fatalf("apply pre-collaboration migrations: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at, delete_at)
		VALUES
			('legacy-active-guest', 'legacy-active-guest', 'legacy-active-guest@example.test', 'unused', 'system_guest', 1, 1, 0),
			('legacy-deleted-guest', 'legacy-deleted-guest', 'legacy-deleted-guest@example.test', 'unused', 'system_guest', 1, 1, 10),
			('legacy-regular', 'legacy-regular', 'legacy-regular@example.test', 'unused', 'system_user', 1, 1, 0)
	`); err != nil {
		t.Fatalf("seed legacy guest accounts: %v", err)
	}

	started := time.Now()
	if err := migrate(ctx, db, embeddedMigrations, "collaboration-controls-test"); err != nil {
		t.Fatalf("apply collaboration controls migration: %v", err)
	}
	finished := time.Now()
	var activeExpiry, deletedExpiry, regularExpiry int64
	var activeDownload bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT
			(SELECT guest_expires_at FROM users WHERE id='legacy-active-guest'),
			(SELECT guest_file_download FROM users WHERE id='legacy-active-guest'),
			(SELECT guest_expires_at FROM users WHERE id='legacy-deleted-guest'),
			(SELECT guest_expires_at FROM users WHERE id='legacy-regular')
	`).Scan(&activeExpiry, &activeDownload, &deletedExpiry, &regularExpiry); err != nil {
		t.Fatalf("read migrated guest access: %v", err)
	}
	if activeExpiry < started.Add(30*24*time.Hour).Add(-time.Minute).UnixMilli() ||
		activeExpiry > finished.Add(30*24*time.Hour).Add(time.Minute).UnixMilli() {
		t.Fatalf("active legacy guest expiry = %s, want migration time + 30 days", time.UnixMilli(activeExpiry))
	}
	if !activeDownload {
		t.Fatal("active legacy guest did not receive the compatibility download policy")
	}
	if deletedExpiry != 0 || regularExpiry != 0 {
		t.Fatalf("unscoped expiry backfill: deleted=%d regular=%d", deletedExpiry, regularExpiry)
	}
	var name string
	if err := db.Pool.QueryRow(ctx, `SELECT name FROM schema_migrations WHERE version=15`).Scan(&name); err != nil || name != "collaboration_controls" {
		t.Fatalf("migration 15 ledger = (%q, %v)", name, err)
	}
}

// migrationsBeforeCollaborationControls returns every migration that precedes
// the collaboration-controls migration. It filters by version rather than by a
// single filename so migrations added after 000015 do not reintroduce a
// sequence gap in this fixture.
func migrationsBeforeCollaborationControls(t *testing.T) fstest.MapFS {
	t.Helper()
	const collaborationControlsVersion = 15
	files := fstest.MapFS{}
	err := fs.WalkDir(embeddedMigrations, "migrations", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		match := migrationFilenamePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return err
		}
		if version >= collaborationControlsVersion {
			return nil
		}
		data, err := fs.ReadFile(embeddedMigrations, path)
		if err != nil {
			return err
		}
		files[path] = &fstest.MapFile{Data: data}
		return nil
	})
	if err != nil {
		t.Fatalf("build pre-collaboration migration set: %v", err)
	}
	return files
}
