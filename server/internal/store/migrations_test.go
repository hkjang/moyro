package store

import (
	"crypto/sha256"
	"fmt"
	"testing"
	"testing/fstest"
)

func TestV01BaselineMigrationIsImmutable(t *testing.T) {
	t.Parallel()

	migrations, err := loadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatalf("load embedded migrations: %v", err)
	}
	if len(migrations) == 0 || migrations[0].Version != 1 || migrations[0].Name != "v0_1_baseline" {
		t.Fatalf("first embedded migration = %#v", migrations)
	}
	const wantChecksum = "4f2051ce2e9a070a64d8fab92049ad83e6a7640b6ff3d96086465dff2557da9f"
	if migrations[0].Checksum != wantChecksum {
		t.Fatalf("v0.1 baseline checksum changed: got %s, want %s", migrations[0].Checksum, wantChecksum)
	}
	if !migrations[0].Irreversible {
		t.Fatal("v0.1 baseline must remain marked irreversible")
	}
}

func TestLoadMigrationsOrdersAndCapturesMetadata(t *testing.T) {
	t.Parallel()

	files := fstest.MapFS{
		"migrations/000002_add_posts.up.sql": {
			Data: []byte("CREATE TABLE posts (id TEXT PRIMARY KEY);\n"),
		},
		"migrations/000001_core.up.sql": {
			Data: []byte("-- moyro:irreversible\nCREATE TABLE users (id TEXT PRIMARY KEY);\n"),
		},
		"migrations/README.md": {Data: []byte("ignored")},
	}

	migrations, err := loadMigrations(files)
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if len(migrations) != 2 {
		t.Fatalf("migration count = %d, want 2", len(migrations))
	}
	if migrations[0].Version != 1 || migrations[0].Name != "core" {
		t.Fatalf("first migration = %#v", migrations[0])
	}
	if !migrations[0].Irreversible {
		t.Fatal("first migration should be irreversible")
	}
	if migrations[1].Version != 2 || migrations[1].Name != "add_posts" {
		t.Fatalf("second migration = %#v", migrations[1])
	}
	if migrations[1].Irreversible {
		t.Fatal("second migration should be reversible by default")
	}
	wantChecksum := fmt.Sprintf("%x", sha256.Sum256(files["migrations/000002_add_posts.up.sql"].Data))
	if migrations[1].Checksum != wantChecksum {
		t.Fatalf("checksum = %q, want %q", migrations[1].Checksum, wantChecksum)
	}
}

func TestLoadMigrationsRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files fstest.MapFS
	}{
		{
			name: "invalid filename",
			files: fstest.MapFS{
				"migrations/not_versioned.up.sql": {Data: []byte("SELECT 1")},
			},
		},
		{
			name: "zero version",
			files: fstest.MapFS{
				"migrations/000000_core.up.sql": {Data: []byte("SELECT 1")},
			},
		},
		{
			name: "duplicate version",
			files: fstest.MapFS{
				"migrations/000001_core.up.sql":  {Data: []byte("SELECT 1")},
				"migrations/000001_other.up.sql": {Data: []byte("SELECT 2")},
			},
		},
		{
			name: "empty migration",
			files: fstest.MapFS{
				"migrations/000001_core.up.sql": {Data: []byte(" \n")},
			},
		},
		{
			name: "sequence gap",
			files: fstest.MapFS{
				"migrations/000001_core.up.sql": {Data: []byte("SELECT 1")},
				"migrations/000003_gap.up.sql":  {Data: []byte("SELECT 3")},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := loadMigrations(tt.files); err == nil {
				t.Fatal("loadMigrations returned nil error")
			}
		})
	}
}

func TestValidateAppliedMigrations(t *testing.T) {
	t.Parallel()

	embedded := []migration{{Version: 1, Name: "core", Checksum: "abc", Irreversible: true}}
	tests := []struct {
		name    string
		applied map[int64]appliedMigration
	}{
		{
			name: "future version",
			applied: map[int64]appliedMigration{
				2: {Name: "future", Checksum: "def"},
			},
		},
		{
			name: "name drift",
			applied: map[int64]appliedMigration{
				1: {Name: "renamed", Checksum: "abc", Irreversible: true},
			},
		},
		{
			name: "checksum drift",
			applied: map[int64]appliedMigration{
				1: {Name: "core", Checksum: "changed", Irreversible: true},
			},
		},
		{
			name: "metadata drift",
			applied: map[int64]appliedMigration{
				1: {Name: "core", Checksum: "abc", Irreversible: false},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := validateAppliedMigrations(embedded, tt.applied); err == nil {
				t.Fatal("validateAppliedMigrations returned nil error")
			}
		})
	}

	if err := validateAppliedMigrations(embedded, map[int64]appliedMigration{
		1: {Name: "core", Checksum: "abc", Irreversible: true},
	}); err != nil {
		t.Fatalf("matching migration rejected: %v", err)
	}
}
