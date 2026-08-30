package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hkjang/moyro/server/internal/buildinfo"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	migrationTableSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version      BIGINT PRIMARY KEY CHECK (version > 0),
    name         TEXT UNIQUE NOT NULL,
    checksum     CHAR(64) NOT NULL CHECK (length(checksum) = 64),
    applied_at   BIGINT NOT NULL,
    execution_ms BIGINT NOT NULL CHECK (execution_ms >= 0),
    app_version  TEXT NOT NULL,
    irreversible BOOLEAN NOT NULL DEFAULT FALSE
)`

	// PostgreSQL session advisory locks are shared by every connection using
	// the same database. Deriving the key from database + effective schema
	// serializes runners targeting the same Moyro schema while allowing truly
	// independent schemas (including isolated integration tests) to migrate in
	// parallel. hashtextextended is available on every supported PostgreSQL.
	migrationLockSQL = `
SELECT pg_advisory_lock(
    hashtextextended(current_database() || ':' || COALESCE(current_schema(), ''), 0)
)`
	migrationUnlockSQL = `
SELECT pg_advisory_unlock(
    hashtextextended(current_database() || ':' || COALESCE(current_schema(), ''), 0)
)`
)

var migrationFilenamePattern = regexp.MustCompile(`^(\d{6})_([a-z0-9][a-z0-9_]*)\.up\.sql$`)

//go:embed migrations/*.up.sql
var embeddedMigrations embed.FS

type migration struct {
	Version      int64
	Name         string
	Checksum     string
	SQL          string
	Irreversible bool
}

type appliedMigration struct {
	Name         string
	Checksum     string
	Irreversible bool
}

// EmbeddedMigrationTarget returns the latest migration bundled into this
// binary. Operational diagnostics use it to compare the durable ledger with
// the exact schema version the running process expects; callers never need to
// duplicate a version number that would drift as migrations are added.
func EmbeddedMigrationTarget() (version int64, name string, err error) {
	migrations, err := loadMigrations(embeddedMigrations)
	if err != nil {
		return 0, "", err
	}
	if len(migrations) == 0 {
		return 0, "", errors.New("store: no database migrations embedded")
	}
	target := migrations[len(migrations)-1]
	return target.Version, target.Name, nil
}

// Migrate applies every embedded migration that has not yet been recorded.
// Applied files are immutable: a checksum or metadata mismatch fails startup
// instead of silently changing release history.
func Migrate(ctx context.Context, db *DB) error {
	return migrate(ctx, db, embeddedMigrations, buildinfo.Current().Version)
}

func migrate(ctx context.Context, db *DB, migrationFiles fs.FS, appVersion string) error {
	if db == nil || db.Pool == nil {
		return errors.New("store: nil db")
	}

	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		return err
	}
	if len(migrations) == 0 {
		return errors.New("store: no database migrations embedded")
	}
	if strings.TrimSpace(appVersion) == "" {
		appVersion = "unknown"
	}

	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("store: acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, migrationLockSQL); err != nil {
		return fmt.Errorf("store: acquire migration lock: %w", err)
	}
	defer releaseMigrationLock(conn)

	if _, err := conn.Exec(ctx, migrationTableSQL); err != nil {
		return fmt.Errorf("store: create migration ledger: %w", err)
	}

	applied, highestApplied, err := readAppliedMigrations(ctx, conn)
	if err != nil {
		return err
	}
	if err := validateAppliedMigrations(migrations, applied); err != nil {
		return err
	}

	for _, next := range migrations {
		if _, ok := applied[next.Version]; ok {
			continue
		}
		if next.Version < highestApplied {
			return fmt.Errorf("store: migration %06d_%s is pending below applied version %06d", next.Version, next.Name, highestApplied)
		}
		if err := applyMigration(ctx, conn, next, appVersion); err != nil {
			return err
		}
	}

	return nil
}

func loadMigrations(migrationFiles fs.FS) ([]migration, error) {
	if migrationFiles == nil {
		return nil, errors.New("store: nil migration filesystem")
	}

	filenames, err := fs.Glob(migrationFiles, "migrations/*.up.sql")
	if err != nil {
		return nil, fmt.Errorf("store: list database migrations: %w", err)
	}
	sort.Strings(filenames)

	seenVersions := make(map[int64]string, len(filenames))
	migrations := make([]migration, 0, len(filenames))
	for _, filename := range filenames {
		matches := migrationFilenamePattern.FindStringSubmatch(path.Base(filename))
		if matches == nil {
			return nil, fmt.Errorf("store: invalid migration filename %q", filename)
		}
		version, err := strconv.ParseInt(matches[1], 10, 64)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("store: invalid migration version in %q", filename)
		}
		if previous, exists := seenVersions[version]; exists {
			return nil, fmt.Errorf("store: duplicate migration version %06d in %q and %q", version, previous, filename)
		}

		contents, err := fs.ReadFile(migrationFiles, filename)
		if err != nil {
			return nil, fmt.Errorf("store: read migration %q: %w", filename, err)
		}
		if strings.TrimSpace(string(contents)) == "" {
			return nil, fmt.Errorf("store: migration %q is empty", filename)
		}
		checksum := sha256.Sum256(contents)
		seenVersions[version] = filename
		migrations = append(migrations, migration{
			Version:      version,
			Name:         matches[2],
			Checksum:     fmt.Sprintf("%x", checksum),
			SQL:          string(contents),
			Irreversible: hasIrreversibleMarker(contents),
		})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})
	for index, item := range migrations {
		expected := int64(index + 1)
		if item.Version != expected {
			return nil, fmt.Errorf("store: migration sequence gap: got %06d_%s, want version %06d", item.Version, item.Name, expected)
		}
	}
	return migrations, nil
}

func hasIrreversibleMarker(contents []byte) bool {
	for _, line := range strings.Split(string(contents), "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if trimmed == "" {
			continue
		}
		return trimmed == "-- moyro:irreversible"
	}
	return false
}

func readAppliedMigrations(ctx context.Context, conn *pgxpool.Conn) (map[int64]appliedMigration, int64, error) {
	rows, err := conn.Query(ctx, `
SELECT version, name, checksum, irreversible
FROM schema_migrations
ORDER BY version`)
	if err != nil {
		return nil, 0, fmt.Errorf("store: read migration ledger: %w", err)
	}
	defer rows.Close()

	applied := make(map[int64]appliedMigration)
	var highest int64
	for rows.Next() {
		var version int64
		var record appliedMigration
		if err := rows.Scan(&version, &record.Name, &record.Checksum, &record.Irreversible); err != nil {
			return nil, 0, fmt.Errorf("store: scan migration ledger: %w", err)
		}
		record.Checksum = strings.TrimSpace(record.Checksum)
		applied[version] = record
		if version > highest {
			highest = version
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: iterate migration ledger: %w", err)
	}
	return applied, highest, nil
}

func validateAppliedMigrations(migrations []migration, applied map[int64]appliedMigration) error {
	embedded := make(map[int64]migration, len(migrations))
	for _, item := range migrations {
		embedded[item.Version] = item
	}
	for version, record := range applied {
		item, ok := embedded[version]
		if !ok {
			return fmt.Errorf("store: database has unknown migration version %06d; refusing possible downgrade", version)
		}
		if record.Name != item.Name {
			return fmt.Errorf("store: migration %06d name mismatch: database=%q embedded=%q", version, record.Name, item.Name)
		}
		if record.Checksum != item.Checksum {
			return fmt.Errorf("store: migration %06d_%s checksum mismatch", version, item.Name)
		}
		if record.Irreversible != item.Irreversible {
			return fmt.Errorf("store: migration %06d_%s irreversible metadata mismatch", version, item.Name)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, conn *pgxpool.Conn, item migration, appVersion string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin migration %06d_%s: %w", item.Version, item.Name, err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	startedAt := time.Now()
	if _, err := tx.Exec(ctx, item.SQL); err != nil {
		return fmt.Errorf("store: execute migration %06d_%s: %w", item.Version, item.Name, err)
	}
	executionMS := time.Since(startedAt).Milliseconds()
	if _, err := tx.Exec(ctx, `
INSERT INTO schema_migrations
    (version, name, checksum, applied_at, execution_ms, app_version, irreversible)
VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		item.Version,
		item.Name,
		item.Checksum,
		time.Now().UTC().UnixMilli(),
		executionMS,
		appVersion,
		item.Irreversible,
	); err != nil {
		return fmt.Errorf("store: record migration %06d_%s: %w", item.Version, item.Name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit migration %06d_%s: %w", item.Version, item.Name, err)
	}
	return nil
}

func releaseMigrationLock(conn *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = conn.Exec(ctx, migrationUnlockSQL)
}
