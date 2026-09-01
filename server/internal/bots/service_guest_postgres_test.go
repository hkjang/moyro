package bots

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

func TestResolveTokenCredentialRejectsExpiredGuestPostgres(t *testing.T) {
	db := newBotGuestTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate PAT guest schema: %v", err)
	}

	now := time.Now().UnixMilli()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users
			(id, username, email, password_hash, roles, create_at, update_at, guest_expires_at)
		VALUES
			('pat-regular', 'pat-regular', 'pat-regular@example.test', 'unused', 'system_user', $1, $1, 0),
			('pat-live-guest', 'pat-live-guest', 'pat-live-guest@example.test', 'unused', 'system_guest', $1, $1, $2),
			('pat-expired-guest', 'pat-expired-guest', 'pat-expired-guest@example.test', 'unused', 'system_guest', $1, $1, $3)
	`, now, now+int64(time.Hour/time.Millisecond), now-1); err != nil {
		t.Fatalf("seed PAT guest users: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO personal_access_tokens (id, user_id, token_hash, create_at)
		VALUES
			('pat-row-regular', 'pat-regular', $2, $1),
			('pat-row-live', 'pat-live-guest', $3, $1),
			('pat-row-expired', 'pat-expired-guest', $4, $1)
	`, now,
		hashToken("mdp_regular_secret"), hashToken("mdp_live_secret"), hashToken("mdp_expired_secret")); err != nil {
		t.Fatalf("seed PAT guest credentials: %v", err)
	}

	service := New(db)
	for _, test := range []struct {
		name   string
		secret string
		wantID string
	}{
		{name: "regular", secret: "mdp_regular_secret", wantID: "pat-row-regular"},
		{name: "live guest", secret: "mdp_live_secret", wantID: "pat-row-live"},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := service.ResolveTokenCredential(ctx, test.secret)
			if err != nil || resolved.ID != test.wantID {
				t.Fatalf("ResolveTokenCredential() = (%q, %v), want %q", resolved.ID, err, test.wantID)
			}
		})
	}
	if _, err := service.ResolveTokenCredential(ctx, "mdp_expired_secret"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expired guest PAT error = %v, want ErrTokenInvalid", err)
	}
}

func newBotGuestTestDB(t *testing.T) *store.DB {
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
	schema := "moyro_pat_guest_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
			t.Errorf("drop PAT guest schema: %v", err)
		}
		admin.Close()
	})
	return &store.DB{Pool: pool}
}
