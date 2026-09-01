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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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

func TestGuestBulkDirectoryFiltersExpiredSharedGuestPostgres(t *testing.T) {
	db := newAuthTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().UnixMilli()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at, guest_expires_at)
		VALUES
			('directory-actor', 'directory-actor', 'directory-actor@example.test', 'unused', 'system_guest', $1, $1, $2),
			('directory-regular', 'directory-regular', 'directory-regular@example.test', 'unused', 'system_user', $1, $1, 0),
			('directory-live-guest', 'directory-live-guest', 'directory-live-guest@example.test', 'unused', 'system_guest', $1, $1, $2),
			('directory-expired-guest', 'directory-expired-guest', 'directory-expired-guest@example.test', 'unused', 'system_guest', $1, $1, $3)
	`, now, now+int64(time.Hour/time.Millisecond), now-1); err != nil {
		t.Fatalf("seed guest directory users: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO teams (id, display_name, name, type, create_at, update_at)
		VALUES ('directory-team', 'Directory Team', 'directory-team', 'O', $1, $1)
	`, now); err != nil {
		t.Fatalf("seed guest directory team: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		SELECT 'directory-team', id, 'team_user', $1 FROM users WHERE id LIKE 'directory-%'
	`, now); err != nil {
		t.Fatalf("seed guest directory team memberships: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES ('directory-channel', 'directory-team', 'P', 'Directory Channel', 'directory-channel', $1, $1)
	`, now); err != nil {
		t.Fatalf("seed guest directory channel: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO channel_members (channel_id, user_id, roles, create_at)
		SELECT 'directory-channel', id, 'channel_user', $1 FROM users WHERE id LIKE 'directory-%'
	`, now); err != nil {
		t.Fatalf("seed guest directory channel memberships: %v", err)
	}

	service := New(db, testJWTSecret, time.Hour, nil)
	candidates := []User{
		{ID: "directory-regular"},
		{ID: "directory-live-guest", Roles: "system_guest"},
		{ID: "directory-expired-guest", Roles: "system_guest"},
	}
	filtered, err := service.FilterUsersVisibleToGuest(ctx, "directory-actor", candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 || filtered[0].ID != "directory-regular" || filtered[1].ID != "directory-live-guest" {
		t.Fatalf("filtered guest directory = %#v", filtered)
	}
	if visible, err := service.CanGuestSeeUser(ctx, "directory-actor", "directory-expired-guest"); err != nil || visible {
		t.Fatalf("expired shared guest visibility = (%v, %v), want false", visible, err)
	}
	if visible, err := service.CanGuestSeeUser(ctx, "directory-actor", "directory-regular"); err != nil || !visible {
		t.Fatalf("live shared regular visibility = (%v, %v), want true", visible, err)
	}

	// A stale channel_members pair must not retain directory visibility after
	// either side loses its team membership or the parent team is archived.
	if _, err := db.Pool.Exec(ctx, `DELETE FROM team_members WHERE team_id='directory-team' AND user_id='directory-regular'`); err != nil {
		t.Fatalf("revoke target team membership: %v", err)
	}
	if visible, err := service.CanGuestSeeUser(ctx, "directory-actor", "directory-regular"); err != nil || visible {
		t.Fatalf("target without team membership visibility = (%v, %v), want false", visible, err)
	}
	filtered, err = service.FilterUsersVisibleToGuest(ctx, "directory-actor", candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].ID != "directory-live-guest" {
		t.Fatalf("filtered after target team revoke = %#v", filtered)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO team_members (team_id,user_id,roles,create_at) VALUES ('directory-team','directory-regular','team_user',$1)`, now); err != nil {
		t.Fatalf("restore target team membership: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE teams SET delete_at=$1 WHERE id='directory-team'`, now); err != nil {
		t.Fatalf("archive shared team: %v", err)
	}
	if visible, err := service.CanGuestSeeUser(ctx, "directory-actor", "directory-regular"); err != nil || visible {
		t.Fatalf("archived team visibility = (%v, %v), want false", visible, err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE teams SET delete_at=0 WHERE id='directory-team'`); err != nil {
		t.Fatalf("restore shared team: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM team_members WHERE team_id='directory-team' AND user_id='directory-actor'`); err != nil {
		t.Fatalf("revoke actor team membership: %v", err)
	}
	if visible, err := service.CanGuestSeeUser(ctx, "directory-actor", "directory-regular"); err != nil || visible {
		t.Fatalf("actor without team membership visibility = (%v, %v), want false", visible, err)
	}
	listed, err := service.ListUsersVisibleToGuest(ctx, "directory-actor", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != "directory-actor" {
		t.Fatalf("directory after actor team revoke = %#v, want self only", listed)
	}
	searched, err := service.SearchUsersVisibleToGuest(ctx, "directory-actor", "directory-regular", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(searched) != 0 {
		t.Fatalf("search after actor team revoke = %#v, want empty", searched)
	}
}

func newAuthTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(authTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", authTestPostgresDSN)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
