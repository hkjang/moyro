package store

import (
	"context"
	"io/fs"
	"strings"
	"testing"
)

func TestScheduledLeaseMigrationMapsLegacyRows(t *testing.T) {
	db := newMigrationTestDB(t)
	ctx := testContext(t)
	if _, err := db.Pool.Exec(ctx, `
		CREATE TABLE posts (id TEXT PRIMARY KEY);
		CREATE TABLE scheduled_posts (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			channel_id TEXT NOT NULL,
			root_id TEXT NOT NULL DEFAULT '',
			message TEXT NOT NULL,
			file_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
			props JSONB NOT NULL DEFAULT '{}'::jsonb,
			send_at BIGINT NOT NULL,
			create_at BIGINT NOT NULL,
			sent_at BIGINT NOT NULL DEFAULT 0,
			error_text TEXT NOT NULL DEFAULT ''
		);
		INSERT INTO scheduled_posts
			(id, user_id, channel_id, message, send_at, create_at, sent_at, error_text)
		VALUES
			('pending', 'user', 'channel', 'pending', 100, 10, 0, ''),
			('retry', 'user', 'channel', 'retry', 100, 10, 0, 'legacy failure'),
			('processing', 'user', 'channel', 'processing', 100, 10, -1, ''),
			('succeeded', 'user', 'channel', 'succeeded', 100, 10, 200, '');
	`); err != nil {
		t.Fatalf("create legacy scheduled schema: %v", err)
	}

	contents, err := fs.ReadFile(embeddedMigrations, "migrations/000003_scheduled_post_leases.up.sql")
	if err != nil {
		t.Fatalf("read scheduled lease migration: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, string(contents)); err != nil {
		t.Fatalf("apply scheduled lease migration: %v", err)
	}

	assertMigratedScheduledRow(t, ctx, db, "pending", "pending", 0, 100, "", "")
	assertMigratedScheduledRow(t, ctx, db, "retry", "retry", 1, -1, "legacy_error", "legacy failure")
	assertMigratedScheduledRow(t, ctx, db, "succeeded", "succeeded", 1, 0, "", "")

	var status, token string
	var attempts int
	var claimedAt, leaseUntil, nextAttemptAt int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT status, claim_token, attempt_count, claimed_at, lease_until, next_attempt_at
		FROM scheduled_posts WHERE id='processing'
	`).Scan(&status, &token, &attempts, &claimedAt, &leaseUntil, &nextAttemptAt); err != nil {
		t.Fatalf("read migrated processing row: %v", err)
	}
	if status != "processing" || token != "legacy-processing" || attempts != 1 || claimedAt <= 0 || leaseUntil != claimedAt+300000 || nextAttemptAt != leaseUntil {
		t.Fatalf("processing migration = (%q, %q, %d, %d, %d, %d)", status, token, attempts, claimedAt, leaseUntil, nextAttemptAt)
	}

	if _, err := db.Pool.Exec(ctx, `INSERT INTO posts (id, scheduled_post_id) VALUES ('post-1', 'schedule-1')`); err != nil {
		t.Fatalf("insert scheduled result post: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO posts (id, scheduled_post_id) VALUES ('post-2', 'schedule-1')`); err == nil {
		t.Fatal("scheduled post unique index accepted a duplicate")
	}
}

func assertMigratedScheduledRow(t *testing.T, ctx context.Context, db *DB, id, wantStatus string, wantAttempts int, wantNextAttemptAt int64, wantErrorCode, wantErrorText string) {
	t.Helper()
	var status, errorCode, errorText string
	var attempts int
	var nextAttemptAt int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT status, attempt_count, next_attempt_at, last_error_code, last_error_text
		FROM scheduled_posts WHERE id=$1
	`, id).Scan(&status, &attempts, &nextAttemptAt, &errorCode, &errorText); err != nil {
		t.Fatalf("read migrated scheduled row %s: %v", id, err)
	}
	nextAttemptMatches := nextAttemptAt == wantNextAttemptAt
	if wantNextAttemptAt < 0 {
		nextAttemptMatches = nextAttemptAt > 0
	}
	if status != wantStatus || attempts != wantAttempts || !nextAttemptMatches || errorCode != wantErrorCode || errorText != wantErrorText {
		t.Fatalf("migrated scheduled row %s = (%q, %d, %d, %q, %q), want (%q, %d, %d, %q, %q)", id, status, attempts, nextAttemptAt, errorCode, errorText, wantStatus, wantAttempts, wantNextAttemptAt, wantErrorCode, wantErrorText)
	}
}

func TestScheduledLeaseMigrationContainsNoCreateAtClaimFallback(t *testing.T) {
	contents, err := fs.ReadFile(embeddedMigrations, "migrations/000003_scheduled_post_leases.up.sql")
	if err != nil {
		t.Fatalf("read scheduled lease migration: %v", err)
	}
	if strings.Contains(strings.ToLower(string(contents)), "create_at <") {
		t.Fatal("scheduled lease migration contains a create_at retry condition")
	}
}
