package reminders

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	reminderTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"
	reminderTestUserID      = "reminder-user"
	reminderTestTeamID      = "reminder-team"
	reminderTestChannelID   = "reminder-channel"
)

// A dispatch that stops between the claim and the delivery stamp used to strand
// its row at delivered_at=-1 forever: ClaimDue, ListPending and Delete all
// matched delivered_at=0, so the reminder never fired and its owner could not
// see or cancel it. The claim is a bounded lease now.
func TestClaimDueReclaimsADispatchAbandonedPastItsLease(t *testing.T) {
	db := newReminderTestDB(t)
	ctx := reminderTestContext(t)
	seedReminderTestFixtures(t, ctx, db)
	svc := New(db)
	post := createReminderTestPost(t, ctx, db, "확인할 메시지")
	now := time.Now().UTC().UnixMilli()

	rem, err := svc.Create(ctx, reminderTestUserID, post.ID, now-1_000)
	if err != nil {
		t.Fatalf("create reminder: %v", err)
	}

	claimed, err := svc.ClaimDue(ctx, now, 10)
	if err != nil {
		t.Fatalf("claim due reminders: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != rem.ID {
		t.Fatalf("claimed reminders = %#v, want only %s", claimed, rem.ID)
	}
	assertReminderRow(t, ctx, db, rem.ID, -1, now)

	// The lease holds while the claiming worker is presumed alive, so a second
	// worker cannot deliver the same reminder twice.
	held, err := svc.ClaimDue(ctx, now+claimLeaseDuration.Milliseconds()-1, 10)
	if err != nil {
		t.Fatalf("claim inside lease: %v", err)
	}
	if len(held) != 0 {
		t.Fatalf("claimed %d reminders inside the lease, want 0", len(held))
	}

	// Once the lease expires the abandoned claim returns to the due queue.
	reclaimAt := now + claimLeaseDuration.Milliseconds()
	reclaimed, err := svc.ClaimDue(ctx, reclaimAt, 10)
	if err != nil {
		t.Fatalf("reclaim expired lease: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ID != rem.ID {
		t.Fatalf("reclaimed reminders = %#v, want only %s", reclaimed, rem.ID)
	}
	assertReminderRow(t, ctx, db, rem.ID, -1, reclaimAt)

	// Delivery ends the lease for good.
	if err := svc.MarkDelivered(ctx, rem.ID, reclaimAt+5); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
	assertReminderRow(t, ctx, db, rem.ID, reclaimAt+5, 0)
	settled, err := svc.ClaimDue(ctx, reclaimAt+time.Hour.Milliseconds(), 10)
	if err != nil {
		t.Fatalf("claim after delivery: %v", err)
	}
	if len(settled) != 0 {
		t.Fatalf("claimed %d delivered reminders, want 0", len(settled))
	}
}

// A pending reminder that is not due yet must never be swept up by the
// expired-lease branch.
func TestClaimDueLeavesUndueRemindersAlone(t *testing.T) {
	db := newReminderTestDB(t)
	ctx := reminderTestContext(t)
	seedReminderTestFixtures(t, ctx, db)
	svc := New(db)
	post := createReminderTestPost(t, ctx, db, "나중에 볼 메시지")
	now := time.Now().UTC().UnixMilli()

	rem, err := svc.Create(ctx, reminderTestUserID, post.ID, now+time.Hour.Milliseconds())
	if err != nil {
		t.Fatalf("create reminder: %v", err)
	}
	claimed, err := svc.ClaimDue(ctx, now, 10)
	if err != nil {
		t.Fatalf("claim due reminders: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed %d undue reminders, want 0", len(claimed))
	}
	assertReminderRow(t, ctx, db, rem.ID, 0, 0)

	pending, err := svc.ListPending(ctx, reminderTestUserID)
	if err != nil {
		t.Fatalf("list pending reminders: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != rem.ID {
		t.Fatalf("pending reminders = %#v, want only %s", pending, rem.ID)
	}
	deleted, err := svc.Delete(ctx, rem.ID, reminderTestUserID)
	if err != nil || !deleted {
		t.Fatalf("delete pending reminder = %v, %v", deleted, err)
	}
}

func assertReminderRow(t *testing.T, ctx context.Context, db *store.DB, id string, wantDeliveredAt, wantClaimedAt int64) {
	t.Helper()
	var deliveredAt, claimedAt int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT delivered_at, claimed_at FROM post_reminders WHERE id=$1
	`, id).Scan(&deliveredAt, &claimedAt); err != nil {
		t.Fatalf("read reminder row %s: %v", id, err)
	}
	if deliveredAt != wantDeliveredAt || claimedAt != wantClaimedAt {
		t.Fatalf("reminder row %s = (delivered_at %d, claimed_at %d), want (%d, %d)", id, deliveredAt, claimedAt, wantDeliveredAt, wantClaimedAt)
	}
}

func reminderTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func newReminderTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(reminderTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", reminderTestPostgresDSN)
	}
	ctx := reminderTestContext(t)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open reminder test admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping reminder test PostgreSQL: %v", err)
	}

	schemaName := "moyro_reminders_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create reminder test schema: %v", err)
	}

	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop reminder test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse reminder test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 6
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open reminder test pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping reminder test pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate reminder test schema: %v", err)
	}
	return db
}

func seedReminderTestFixtures(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES ('reminder-user', 'reminder-user', 'reminder@example.test', 'hash', 'system_user', 1, 1);
		INSERT INTO teams (id, name, display_name, type, create_at, update_at)
		VALUES ('reminder-team', 'reminder-team', 'Reminder Team', 'O', 1, 1);
		INSERT INTO team_members (team_id, user_id, create_at)
		VALUES ('reminder-team', 'reminder-user', 1);
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES ('reminder-channel', 'reminder-team', 'O', 'Reminder Channel', 'reminder-channel', 1, 1);
		INSERT INTO channel_members (channel_id, user_id, create_at)
		VALUES ('reminder-channel', 'reminder-user', 1);
	`); err != nil {
		t.Fatalf("seed reminder test fixtures: %v", err)
	}
}

func createReminderTestPost(t *testing.T, ctx context.Context, db *store.DB, message string) *posts.Post {
	t.Helper()
	p, err := posts.New(db).Create(ctx, reminderTestChannelID, reminderTestUserID, "", message, nil, nil)
	if err != nil {
		t.Fatalf("create reminder test post: %v", err)
	}
	return p
}
