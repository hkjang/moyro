package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/secrets"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const authTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

func TestSessionJTIHashRollingUpgradePostgres(t *testing.T) {
	db := newAuthTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	manager, err := secrets.New(bytes.Repeat([]byte{0x52}, secrets.MasterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	svc := New(db, testJWTSecret, time.Hour, manager)
	user, err := svc.Register(ctx, "session-user", "session@example.test", "long-test-password")
	if err != nil {
		t.Fatalf("register user: %v", err)
	}

	_, token, err := svc.LoginWithDevice(ctx, user.Email, "long-test-password", "browser-a")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	parsed, err := svc.Parse(token)
	if err != nil {
		t.Fatalf("parse issued token: %v", err)
	}
	wantDigest, err := manager.Digest(sessionJTIDigestPurpose, []byte(parsed.ID))
	if err != nil {
		t.Fatal(err)
	}

	// Expand-phase dual write keeps the raw token for old binaries while new
	// binaries use the keyed lookup column.
	var sessionID, storedToken string
	var storedDigest []byte
	if err := db.Pool.QueryRow(ctx, `
		SELECT id, token, jti_hash FROM sessions WHERE user_id=$1
	`, user.ID).Scan(&sessionID, &storedToken, &storedDigest); err != nil {
		t.Fatalf("read dual-written session: %v", err)
	}
	if storedToken != token || !bytes.Equal(storedDigest, wantDigest) {
		t.Fatal("session row was not dual-written with token and JTI digest")
	}

	authenticated, err := svc.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("authenticate hashed session: %v", err)
	}
	if authenticated.SessionID != sessionID || authenticated.UserID != user.ID {
		t.Fatalf("authenticated session = (%q,%q), want (%q,%q)", authenticated.SessionID, authenticated.UserID, sessionID, user.ID)
	}
	updated, err := svc.SetSessionDeviceID(ctx, authenticated.SessionID, user.ID, "browser-b")
	if err != nil || !updated {
		t.Fatalf("update current device = %v, %v", updated, err)
	}

	// Simulate a row written by an old node after the expand migration. The new
	// node may read it by token only once, then must populate its keyed digest.
	legacy, err := svc.issueToken(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacySessionID := uuid.NewString()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO sessions (id, user_id, token, device_id, expires_at, create_at)
		VALUES ($1,$2,$3,'legacy-node',$4,$5)
	`, legacySessionID, user.ID, legacy.Token, legacy.ExpiresAt, time.Now().UnixMilli()); err != nil {
		t.Fatalf("insert legacy session: %v", err)
	}
	legacyClaims, err := svc.Authenticate(ctx, legacy.Token)
	if err != nil {
		t.Fatalf("authenticate legacy session: %v", err)
	}
	if legacyClaims.SessionID != legacySessionID {
		t.Fatalf("legacy SessionID = %q, want %q", legacyClaims.SessionID, legacySessionID)
	}
	var backfilled []byte
	if err := db.Pool.QueryRow(ctx, `SELECT jti_hash FROM sessions WHERE id=$1`, legacySessionID).Scan(&backfilled); err != nil {
		t.Fatalf("read legacy backfill: %v", err)
	}
	if !bytes.Equal(backfilled, legacy.JTIHash) {
		t.Fatal("legacy session JTI digest was not lazily backfilled")
	}
	expiredSessionID := uuid.NewString()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO sessions (id, user_id, token, device_id, expires_at, create_at)
		VALUES ($1,$2,$3,'expired-browser',$4,$5)
	`, expiredSessionID, user.ID, "expired-token-"+expiredSessionID, time.Now().Add(-time.Minute).UnixMilli(), time.Now().Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatalf("insert expired session: %v", err)
	}

	listed, err := svc.ListSessions(ctx, user.ID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	rawList, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawList), "token") || strings.Contains(string(rawList), "jti_hash") || strings.Contains(string(rawList), token) {
		t.Fatalf("session list exposed credential material: %s", rawList)
	}
	for _, session := range listed {
		if session.ID == expiredSessionID {
			t.Fatal("active session list included an expired row")
		}
	}

	revoked, err := svc.RevokeOthers(ctx, user.ID, authenticated.SessionID)
	if err != nil || revoked != 1 {
		t.Fatalf("revoke other sessions = %d, %v", revoked, err)
	}
	if _, err := svc.Authenticate(ctx, token); err != nil {
		t.Fatalf("current session was revoked with other sessions: %v", err)
	}
}

func newAuthTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(authTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", authTestPostgresDSN)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	schemaName := "moyro_auth_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create test schema: %v", err)
	}

	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		adminPool.Close()
		t.Fatalf("parse PostgreSQL DSN: %v", err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		adminPool.Close()
		t.Fatalf("open isolated pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		adminPool.Close()
	})
	db := &store.DB{Pool: pool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate auth test database: %v", err)
	}
	return db
}
