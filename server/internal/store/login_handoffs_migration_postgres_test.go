package store

import (
	"bytes"
	"testing"
)

func TestLoginHandoffsMigrationIntegrityAndRestartPostgres(t *testing.T) {
	db := newMigrationTestDB(t)
	ctx := testContext(t)

	if err := migrate(ctx, db, embeddedMigrations, "sso-handoff-test"); err != nil {
		t.Fatalf("apply embedded migrations: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
VALUES ('handoff-user', 'handoff-user', 'handoff@example.test', 'unused', 'system_user', 1, 1)
`); err != nil {
		t.Fatalf("insert handoff migration user: %v", err)
	}
	codeHash := bytes.Repeat([]byte{0x31}, 32)
	bindingHash := bytes.Repeat([]byte{0x32}, 32)
	if _, err := db.Pool.Exec(ctx, `
INSERT INTO login_handoffs (code_hash, binding_hash, user_id, expires_at, create_at)
VALUES ($1, $2, 'handoff-user', 2000, 1000)
`, codeHash, bindingHash); err != nil {
		t.Fatalf("insert valid login handoff: %v", err)
	}

	if _, err := db.Pool.Exec(ctx, `
INSERT INTO login_handoffs (code_hash, binding_hash, user_id, expires_at, create_at)
VALUES ($1, $2, 'handoff-user', 2000, 1000)
`, bytes.Repeat([]byte{0x41}, 31), bindingHash); err == nil {
		t.Fatal("login handoff accepted a non-32-byte code digest")
	}

	// A restart must trust the migration ledger instead of replaying CREATE
	// TABLE, and it must preserve live handoffs created before the restart.
	if err := migrate(ctx, db, embeddedMigrations, "sso-handoff-restart-test"); err != nil {
		t.Fatalf("restart migrations: %v", err)
	}
	var handoffCount int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM login_handoffs WHERE user_id='handoff-user'`).Scan(&handoffCount); err != nil {
		t.Fatalf("count preserved login handoffs: %v", err)
	}
	if handoffCount != 1 {
		t.Fatalf("preserved login handoffs = %d, want 1", handoffCount)
	}

	if _, err := db.Pool.Exec(ctx, `DELETE FROM users WHERE id='handoff-user'`); err != nil {
		t.Fatalf("delete handoff user: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM login_handoffs WHERE user_id='handoff-user'`).Scan(&handoffCount); err != nil {
		t.Fatalf("count cascaded login handoffs: %v", err)
	}
	if handoffCount != 0 {
		t.Fatalf("login handoffs after user deletion = %d, want 0", handoffCount)
	}

	var migrationName string
	if err := db.Pool.QueryRow(ctx, `SELECT name FROM schema_migrations WHERE version=11`).Scan(&migrationName); err != nil {
		t.Fatalf("read login handoff migration ledger: %v", err)
	}
	if migrationName != "login_handoffs" {
		t.Fatalf("migration 11 name = %q, want login_handoffs", migrationName)
	}
}
