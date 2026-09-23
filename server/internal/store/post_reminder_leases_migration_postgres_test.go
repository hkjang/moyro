package store

import (
	"context"
	"io/fs"
	"testing"
	"time"
)

func TestPostReminderLeaseMigrationGivesInFlightRowsAFullLease(t *testing.T) {
	db := newMigrationTestDB(t)
	ctx := testContext(t)
	if _, err := db.Pool.Exec(ctx, `
		CREATE TABLE post_reminders (
			id           TEXT PRIMARY KEY,
			user_id      TEXT NOT NULL,
			post_id      TEXT NOT NULL,
			remind_at    BIGINT NOT NULL,
			create_at    BIGINT NOT NULL,
			delivered_at BIGINT NOT NULL DEFAULT 0
		);
		INSERT INTO post_reminders (id, user_id, post_id, remind_at, create_at, delivered_at)
		VALUES
			('pending', 'user', 'post', 100, 10, 0),
			('in-flight', 'user', 'post', 100, 10, -1),
			('delivered', 'user', 'post', 100, 10, 200);
	`); err != nil {
		t.Fatalf("create legacy reminder schema: %v", err)
	}

	before := time.Now().UTC().UnixMilli()
	contents, err := fs.ReadFile(embeddedMigrations, "migrations/000018_post_reminder_leases.up.sql")
	if err != nil {
		t.Fatalf("read post reminder lease migration: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, string(contents)); err != nil {
		t.Fatalf("apply post reminder lease migration: %v", err)
	}
	after := time.Now().UTC().UnixMilli()

	// Rows that were not in flight keep an empty lease, and a row interrupted
	// by the upgrade gets a full lease measured from migration time rather than
	// being replayed immediately.
	assertReminderClaimedAt(t, ctx, db, "pending", 0, 0)
	assertReminderClaimedAt(t, ctx, db, "delivered", 0, 0)
	assertReminderClaimedAt(t, ctx, db, "in-flight", before, after)

	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO post_reminders (id, user_id, post_id, remind_at, create_at, claimed_at)
		VALUES ('negative', 'user', 'post', 100, 10, -1)
	`); err == nil {
		t.Fatal("post_reminders accepted a negative claimed_at")
	}

	var indexed bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_catalog.pg_class AS index_class
			JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
			WHERE namespace.nspname = current_schema()
			  AND index_class.relname = 'post_reminders_expired_lease_idx'
		)
	`).Scan(&indexed); err != nil {
		t.Fatalf("read expired lease index metadata: %v", err)
	}
	if !indexed {
		t.Fatal("post_reminders_expired_lease_idx was not created")
	}
}

func TestPostReminderLeaseMigrationIsRecordedInTheLedger(t *testing.T) {
	db := newMigrationTestDB(t)
	ctx := testContext(t)
	if err := migrate(ctx, db, embeddedMigrations, "v0.2-test"); err != nil {
		t.Fatalf("apply embedded migrations: %v", err)
	}

	var name string
	if err := db.Pool.QueryRow(ctx, `
		SELECT name FROM schema_migrations WHERE version = 18
	`).Scan(&name); err != nil {
		t.Fatalf("read post reminder lease migration ledger: %v", err)
	}
	if name != "post_reminder_leases" {
		t.Fatalf("migration 18 name = %q", name)
	}

	var columnDefault string
	var notNull bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT COALESCE(column_default, ''), is_nullable = 'NO'
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'post_reminders'
		  AND column_name = 'claimed_at'
	`).Scan(&columnDefault, &notNull); err != nil {
		t.Fatalf("read claimed_at column metadata: %v", err)
	}
	if columnDefault != "0" || !notNull {
		t.Fatalf("claimed_at column = (default %q, not null %v)", columnDefault, notNull)
	}
}

func assertReminderClaimedAt(t *testing.T, ctx context.Context, db *DB, id string, lowerBound, upperBound int64) {
	t.Helper()
	var claimedAt int64
	if err := db.Pool.QueryRow(ctx, `SELECT claimed_at FROM post_reminders WHERE id=$1`, id).Scan(&claimedAt); err != nil {
		t.Fatalf("read migrated reminder row %s: %v", id, err)
	}
	if claimedAt < lowerBound || claimedAt > upperBound {
		t.Fatalf("migrated reminder %s claimed_at = %d, want between %d and %d", id, claimedAt, lowerBound, upperBound)
	}
}
