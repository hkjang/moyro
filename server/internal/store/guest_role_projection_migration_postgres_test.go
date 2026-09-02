package store

import (
	"io/fs"
	"strconv"
	"testing"
	"testing/fstest"
)

// TestGuestRoleProjectionMatchesLegacyRolePredicate proves the generated column
// agrees with the regular-expression predicate it replaces, including the
// whitespace and substring edge cases that made the original test non-trivial:
// "system_guest_manager" must not read as a guest, and a role string with
// padding or tabs must.
func TestGuestRoleProjectionMatchesLegacyRolePredicate(t *testing.T) {
	db := newMigrationTestDB(t)
	ctx := testContext(t)
	if err := migrate(ctx, db, migrationsBeforeGuestRoleProjection(t), "pre-guest-projection-test"); err != nil {
		t.Fatalf("apply pre-projection migrations: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at, delete_at)
		VALUES
			('plain-user',     'plain-user',     'plain@example.test',     'unused', 'system_user', 1, 1, 0),
			('bare-guest',     'bare-guest',     'bare@example.test',      'unused', 'system_guest', 1, 1, 0),
			('padded-guest',   'padded-guest',   'padded@example.test',    'unused', '  system_guest  ', 1, 1, 0),
			('tabbed-guest',   'tabbed-guest',   'tabbed@example.test',    'unused', E'system_user\tsystem_guest', 1, 1, 0),
			('trailing-guest', 'trailing-guest', 'trailing@example.test',  'unused', 'system_user system_guest', 1, 1, 0),
			('guest-manager',  'guest-manager',  'manager@example.test',   'unused', 'system_guest_manager', 1, 1, 0),
			('admin-user',     'admin-user',     'admin@example.test',     'unused', 'system_user system_admin', 1, 1, 0)
	`); err != nil {
		t.Fatalf("seed role fixtures: %v", err)
	}

	if err := migrate(ctx, db, embeddedMigrations, "guest-projection-test"); err != nil {
		t.Fatalf("apply guest projection migration: %v", err)
	}

	// The projection must equal the legacy predicate for every seeded row.
	var disagreements int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM users
		WHERE is_guest IS DISTINCT FROM
			('system_guest' = ANY (regexp_split_to_array(BTRIM(roles), E'\\s+')))
	`).Scan(&disagreements); err != nil {
		t.Fatalf("compare projection with legacy predicate: %v", err)
	}
	if disagreements != 0 {
		t.Fatalf("projection disagrees with legacy predicate on %d rows", disagreements)
	}

	for id, want := range map[string]bool{
		"plain-user":     false,
		"bare-guest":     true,
		"padded-guest":   true,
		"tabbed-guest":   true,
		"trailing-guest": true,
		"guest-manager":  false,
		"admin-user":     false,
	} {
		var got bool
		if err := db.Pool.QueryRow(ctx, `SELECT is_guest FROM users WHERE id=$1`, id).Scan(&got); err != nil {
			t.Fatalf("read is_guest for %s: %v", id, err)
		}
		if got != want {
			t.Fatalf("is_guest for %s = %v, want %v", id, got, want)
		}
	}

	// The column is generated, so a later role change must re-project without
	// any application write to the column itself.
	if _, err := db.Pool.Exec(ctx, `UPDATE users SET roles='system_user system_guest' WHERE id='plain-user'`); err != nil {
		t.Fatalf("promote plain-user to guest: %v", err)
	}
	var promoted bool
	if err := db.Pool.QueryRow(ctx, `SELECT is_guest FROM users WHERE id='plain-user'`).Scan(&promoted); err != nil {
		t.Fatalf("read re-projected is_guest: %v", err)
	}
	if !promoted {
		t.Fatalf("generated column did not re-project after a role change")
	}

	// A direct write to a generated column must be rejected, which is what
	// makes the projection trustworthy as an authorization input.
	if _, err := db.Pool.Exec(ctx, `UPDATE users SET is_guest=FALSE WHERE id='bare-guest'`); err == nil {
		t.Fatalf("generated column accepted a direct write")
	}

	var indexCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM pg_indexes
		WHERE schemaname = current_schema()
		  AND indexname IN ('users_guest_expiry_idx', 'users_active_non_guest_idx')
	`).Scan(&indexCount); err != nil {
		t.Fatalf("read projection indexes: %v", err)
	}
	if indexCount != 2 {
		t.Fatalf("projection indexes present = %d, want 2", indexCount)
	}
}

func migrationsBeforeGuestRoleProjection(t *testing.T) fstest.MapFS {
	t.Helper()
	const guestProjectionVersion = 16
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
		if version >= guestProjectionVersion {
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
		t.Fatalf("build pre-projection migration set: %v", err)
	}
	return files
}
