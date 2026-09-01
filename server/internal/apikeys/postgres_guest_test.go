package apikeys

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresCredentialResolutionRejectsExpiredGuestOwner(t *testing.T) {
	pool := newAPIKeyGuestTestPool(t)
	db := &store.DB{Pool: pool}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate api key guest schema: %v", err)
	}

	now := time.Now().UnixMilli()
	if _, err := pool.Exec(ctx, `
		INSERT INTO users
			(id, username, email, password_hash, roles, create_at, update_at, guest_expires_at)
		VALUES
			('key-regular', 'key-regular', 'key-regular@example.test', 'unused', 'system_user', $1, $1, 0),
			('key-live-guest', 'key-live-guest', 'key-live-guest@example.test', 'unused', 'system_guest', $1, $1, $2),
			('key-expired-guest', 'key-expired-guest', 'key-expired-guest@example.test', 'unused', 'system_guest', $1, $1, $3)
	`, now, now+int64(time.Hour/time.Millisecond), now-1); err != nil {
		t.Fatalf("seed api key guest users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO api_keys
			(id, owner_user_id, name, key_prefix, secret_hash, kind, status, expires_at,
			 rotation_group_id, created_by, create_at, update_at)
		VALUES
			('regular-key', 'key-regular', 'regular', 'moyro_regular', decode('01','hex'), 'user', 'active', $2, 'regular-key', 'key-regular', $1, $1),
			('live-guest-key', 'key-live-guest', 'live guest', 'moyro_live', decode('02','hex'), 'user', 'active', $2, 'live-guest-key', 'key-live-guest', $1, $1),
			('expired-guest-key', 'key-expired-guest', 'expired guest', 'moyro_expired', decode('03','hex'), 'user', 'active', $2, 'expired-guest-key', 'key-expired-guest', $1, $1)
	`, now, now+int64(24*time.Hour/time.Millisecond)); err != nil {
		t.Fatalf("seed api key guest credentials: %v", err)
	}

	repository := &PostgresRepository{pool: pool}
	for _, test := range []struct {
		name   string
		digest byte
		wantID string
	}{
		{name: "regular", digest: 1, wantID: "regular-key"},
		{name: "live guest", digest: 2, wantID: "live-guest-key"},
	} {
		t.Run(test.name, func(t *testing.T) {
			key, err := repository.ResolveByDigest(ctx, []byte{test.digest})
			if err != nil || key.ID != test.wantID {
				t.Fatalf("ResolveByDigest() = (%q, %v), want %q", key.ID, err, test.wantID)
			}
		})
	}
	if _, err := repository.ResolveByDigest(ctx, []byte{3}); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("expired guest ResolveByDigest error = %v, want ErrInvalidCredential", err)
	}

	service := &Service{repo: repository, opts: DefaultOptions()}
	if _, _, err := service.ResolveCurrent(ctx, "key-expired-guest", "expired-guest-key"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("expired guest ResolveCurrent error = %v, want ErrInvalidCredential", err)
	}
	if principal, key, err := service.ResolveCurrent(ctx, "key-live-guest", "live-guest-key"); err != nil ||
		principal.UserID != "key-live-guest" || key.ID != "live-guest-key" {
		t.Fatalf("live guest ResolveCurrent = (%#v, %q, %v)", principal, key.ID, err)
	}
}

func newAPIKeyGuestTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("MOYRO_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("MOYRO_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	schema := "moyro_apikey_guest_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quoted
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("drop api key guest schema: %v", err)
		}
		admin.Close()
	})
	return pool
}
