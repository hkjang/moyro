package store

import (
	"io/fs"
	"strconv"
	"testing"
	"testing/fstest"
)

// TestCustomEmojiNameReuseMigrationFreesDeletedNames proves the upgrade path,
// not just the fresh install: an existing database carrying the v0.1 table-wide
// UNIQUE on emojis.name refuses to re-register a soft-deleted shortcode, and
// after the migration the same INSERT succeeds while two live rows sharing a
// name are still rejected.
func TestCustomEmojiNameReuseMigrationFreesDeletedNames(t *testing.T) {
	db := newMigrationTestDB(t)
	ctx := testContext(t)
	if err := migrate(ctx, db, migrationsBeforeCustomEmojiNameReuse(t), "pre-emoji-name-reuse-test"); err != nil {
		t.Fatalf("apply pre-reuse migrations: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES ('emoji-author', 'emoji-author', 'author@example.test', 'unused', 'system_user', 1, 1);
		INSERT INTO file_infos (id, user_id, path, name, size, mime_type, create_at, update_at)
		VALUES ('emoji-file', 'emoji-author', 'emoji/shipit.png', 'shipit.png', 128, 'image/png', 1, 1);
		INSERT INTO emojis (id, name, creator_id, file_id, create_at, delete_at)
		VALUES ('emoji-old', 'shipit', 'emoji-author', 'emoji-file', 1000, 2000)
	`); err != nil {
		t.Fatalf("seed soft-deleted emoji: %v", err)
	}

	// The pre-migration schema is what made the name unrecoverable.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO emojis (id, name, creator_id, file_id, create_at)
		VALUES ('emoji-new', 'shipit', 'emoji-author', 'emoji-file', 3000)
	`); err == nil {
		t.Fatal("pre-migration schema already allowed reuse; the fixture no longer reproduces the bug")
	}

	if err := migrate(ctx, db, embeddedMigrations, "emoji-name-reuse-test"); err != nil {
		t.Fatalf("apply emoji name reuse migration: %v", err)
	}

	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO emojis (id, name, creator_id, file_id, create_at)
		VALUES ('emoji-new', 'shipit', 'emoji-author', 'emoji-file', 3000)
	`); err != nil {
		t.Fatalf("reuse deleted emoji name after migration: %v", err)
	}

	// Live uniqueness — the invariant the service relies on for its
	// create-time collision race — must survive the relaxation.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO emojis (id, name, creator_id, file_id, create_at)
		VALUES ('emoji-dupe', 'shipit', 'emoji-author', 'emoji-file', 4000)
	`); err == nil {
		t.Fatal("two live emojis share a name; live uniqueness was lost")
	}

	// The historical row is untouched, so old posts still resolve the shortcode.
	var preserved int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM emojis WHERE id='emoji-old' AND delete_at=2000`).Scan(&preserved); err != nil {
		t.Fatalf("read preserved row: %v", err)
	}
	if preserved != 1 {
		t.Fatalf("soft-deleted emoji rows preserved = %d, want 1", preserved)
	}

	// The replaced structures are gone and the unique partial index is in place.
	var oldIndexes int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM pg_indexes
		WHERE schemaname = current_schema()
		  AND indexname IN ('emojis_live_idx', 'emojis_name_key')
	`).Scan(&oldIndexes); err != nil {
		t.Fatalf("read replaced emoji indexes: %v", err)
	}
	if oldIndexes != 0 {
		t.Fatalf("replaced emoji indexes still present = %d, want 0", oldIndexes)
	}
	var unique bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT indisunique FROM pg_index
		WHERE indexrelid = (current_schema() || '.emojis_live_name_idx')::regclass
	`).Scan(&unique); err != nil {
		t.Fatalf("read emojis_live_name_idx: %v", err)
	}
	if !unique {
		t.Fatal("emojis_live_name_idx is not unique")
	}
}

func migrationsBeforeCustomEmojiNameReuse(t *testing.T) fstest.MapFS {
	t.Helper()
	const emojiNameReuseVersion = 17
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
		if version >= emojiNameReuseVersion {
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
		t.Fatalf("build pre-reuse migration set: %v", err)
	}
	return files
}
