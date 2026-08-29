package scheduled

import (
	"context"
	"errors"
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

const scheduledTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

func TestScheduledPostgresLeaseCASRetryAndIdempotency(t *testing.T) {
	db := newScheduledTestDB(t)
	ctx := scheduledTestContext(t)
	seedScheduledTestOwner(t, ctx, db)
	svc := New(db)
	postSvc := posts.New(db)
	now := time.Now().UTC().UnixMilli()

	first := createScheduledTestPost(t, ctx, svc, "first", now-2_000)
	second := createScheduledTestPost(t, ctx, svc, "second", now-1_000)
	future := createScheduledTestPost(t, ctx, svc, "future", now+time.Hour.Milliseconds())

	claimed, err := svc.ClaimDue(ctx, now, 10)
	if err != nil {
		t.Fatalf("claim due posts: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed posts = %d, want 2", len(claimed))
	}
	byID := map[string]*ScheduledPost{claimed[0].ID: claimed[0], claimed[1].ID: claimed[1]}
	firstClaim, secondClaim := byID[first.ID], byID[second.ID]
	if firstClaim == nil || secondClaim == nil {
		t.Fatalf("claimed ids = %q, %q", claimed[0].ID, claimed[1].ID)
	}
	if firstClaim.ClaimToken == "" || secondClaim.ClaimToken == "" || firstClaim.ClaimToken == secondClaim.ClaimToken {
		t.Fatalf("claim tokens are not distinct: %q, %q", firstClaim.ClaimToken, secondClaim.ClaimToken)
	}
	for _, item := range claimed {
		if item.Status != StatusProcessing || item.SentAt != -1 || item.ClaimedAt != now || item.LeaseUntil != now+claimLeaseDuration.Milliseconds() || item.AttemptCount != 1 {
			t.Fatalf("claim metadata for %s = %#v", item.ID, item)
		}
	}
	if early, err := svc.ClaimDue(ctx, now+claimLeaseDuration.Milliseconds()-1, 10); err != nil || len(early) != 0 {
		t.Fatalf("claim before lease expiry = %d, %v", len(early), err)
	}

	if err := svc.MarkSent(ctx, first.ID, "stale-token", "", now+1); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("stale MarkSent error = %v", err)
	}
	delivered, err := postSvc.CreateScheduled(ctx, first.ID, first.ChannelID, first.UserID, first.RootID, first.Message, first.Props, first.FileIDs)
	if err != nil {
		t.Fatalf("create scheduled result post: %v", err)
	}
	if _, err := postSvc.CreateScheduled(ctx, first.ID, first.ChannelID, first.UserID, first.RootID, first.Message, first.Props, first.FileIDs); err == nil {
		t.Fatal("duplicate scheduled result post was accepted")
	}
	replayed, err := postSvc.GetByScheduledPost(ctx, first.ID)
	if err != nil || replayed.ID != delivered.ID {
		t.Fatalf("replay lookup = %#v, %v", replayed, err)
	}
	if err := svc.MarkSent(ctx, first.ID, firstClaim.ClaimToken, delivered.ID, now+2); err != nil {
		t.Fatalf("mark scheduled post sent: %v", err)
	}
	if err := svc.MarkSent(ctx, first.ID, firstClaim.ClaimToken, delivered.ID, now+3); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("repeated MarkSent error = %v", err)
	}
	assertScheduledState(t, ctx, db, first.ID, StatusSucceeded, now+2, 0, delivered.ID)

	if deleted, err := svc.Delete(ctx, second.ID, second.UserID); err != nil || deleted {
		t.Fatalf("delete processing post = %v, %v", deleted, err)
	}
	if err := svc.MarkFailed(ctx, second.ID, "stale-token", "create_failed", "transient", secondClaim.AttemptCount, now+10); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("stale MarkFailed error = %v", err)
	}
	failureAt := now + 20
	if err := svc.MarkFailed(ctx, second.ID, secondClaim.ClaimToken, "create_failed", "transient", secondClaim.AttemptCount, failureAt); err != nil {
		t.Fatalf("mark scheduled post failed: %v", err)
	}
	assertScheduledState(t, ctx, db, second.ID, StatusRetry, 0, failureAt+initialRetryDelay.Milliseconds(), "")

	pending, err := svc.ListPending(ctx, second.UserID)
	if err != nil {
		t.Fatalf("list mutable scheduled posts: %v", err)
	}
	if len(pending) != 2 || !containsScheduledPost(pending, second.ID) || !containsScheduledPost(pending, future.ID) {
		t.Fatalf("mutable scheduled ids = %#v", scheduledIDs(pending))
	}

	// Force the next legitimate claim to be the final allowed attempt.
	if _, err := db.Pool.Exec(ctx, `
		UPDATE scheduled_posts
		SET attempt_count=$1, next_attempt_at=$2
		WHERE id=$3`, maxDeliveryAttempts-1, failureAt, second.ID); err != nil {
		t.Fatalf("prepare final retry: %v", err)
	}
	finalClaims, err := svc.ClaimDue(ctx, failureAt, 10)
	if err != nil || len(finalClaims) != 1 || finalClaims[0].ID != second.ID {
		t.Fatalf("final retry claim = %#v, %v", finalClaims, err)
	}
	if finalClaims[0].AttemptCount != maxDeliveryAttempts {
		t.Fatalf("final attempt count = %d", finalClaims[0].AttemptCount)
	}
	if err := svc.MarkFailed(ctx, second.ID, finalClaims[0].ClaimToken, "create_failed", "permanent", finalClaims[0].AttemptCount, failureAt+1); err != nil {
		t.Fatalf("mark scheduled post dead: %v", err)
	}
	assertScheduledState(t, ctx, db, second.ID, StatusDead, -2, 0, "")
	if deleted, err := svc.Delete(ctx, second.ID, second.UserID); err != nil || deleted {
		t.Fatalf("delete dead post = %v, %v", deleted, err)
	}
	if deleted, err := svc.Delete(ctx, future.ID, future.UserID); err != nil || !deleted {
		t.Fatalf("delete pending post = %v, %v", deleted, err)
	}
}

func TestScheduledPostgresReconcilesLegacyStateAfterCoordinatedUpgrade(t *testing.T) {
	db := newScheduledTestDB(t)
	ctx := scheduledTestContext(t)
	seedScheduledTestOwner(t, ctx, db)
	svc := New(db)
	now := time.Now().UTC().UnixMilli()

	legacySuccess := createScheduledTestPost(t, ctx, svc, "legacy-success", now-1_000)
	if _, err := db.Pool.Exec(ctx, `UPDATE scheduled_posts SET sent_at=-1 WHERE id=$1`, legacySuccess.ID); err != nil {
		t.Fatalf("simulate legacy claim: %v", err)
	}
	claimed, err := svc.ClaimDue(ctx, now, 10)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("legacy grace claim = %#v, %v", claimed, err)
	}
	status, token, leaseUntil, attempts := readScheduledClaimState(t, ctx, db, legacySuccess.ID)
	if status != StatusProcessing || !strings.HasPrefix(token, "legacy-") || leaseUntil != now+legacyClaimGrace.Milliseconds() || attempts != 1 {
		t.Fatalf("legacy grace state = (%q, %q, %d, %d)", status, token, leaseUntil, attempts)
	}
	if early, err := svc.ClaimDue(ctx, leaseUntil-1, 10); err != nil || len(early) != 0 {
		t.Fatalf("legacy claim replayed before grace expiry = %#v, %v", early, err)
	}

	// The old worker knows only sent_at; a positive value must win over the
	// expired status lease and reconcile to succeeded without another post.
	legacySentAt := now + 123
	if _, err := db.Pool.Exec(ctx, `UPDATE scheduled_posts SET sent_at=$1, error_text='' WHERE id=$2`, legacySentAt, legacySuccess.ID); err != nil {
		t.Fatalf("simulate legacy success: %v", err)
	}
	if replayed, err := svc.ClaimDue(ctx, leaseUntil+1, 10); err != nil || len(replayed) != 0 {
		t.Fatalf("legacy success was reclaimed = %#v, %v", replayed, err)
	}
	assertScheduledState(t, ctx, db, legacySuccess.ID, StatusSucceeded, legacySentAt, 0, "")

	legacyCrash := createScheduledTestPost(t, ctx, svc, "legacy-crash", now-500)
	if _, err := db.Pool.Exec(ctx, `UPDATE scheduled_posts SET sent_at=-1 WHERE id=$1`, legacyCrash.ID); err != nil {
		t.Fatalf("simulate crashed legacy claim: %v", err)
	}
	if claimed, err := svc.ClaimDue(ctx, now, 10); err != nil || len(claimed) != 0 {
		t.Fatalf("crashed legacy initial reconciliation = %#v, %v", claimed, err)
	}
	_, legacyToken, crashLease, _ := readScheduledClaimState(t, ctx, db, legacyCrash.ID)
	reclaimed, err := svc.ClaimDue(ctx, crashLease, 10)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].ID != legacyCrash.ID {
		t.Fatalf("expired legacy reclaim = %#v, %v", reclaimed, err)
	}
	if reclaimed[0].ClaimToken == legacyToken || strings.HasPrefix(reclaimed[0].ClaimToken, "legacy-") || reclaimed[0].AttemptCount != 2 {
		t.Fatalf("expired legacy claim metadata = %#v", reclaimed[0])
	}
}

func TestScheduledPostgresConcurrentClaimsDoNotOverlap(t *testing.T) {
	db := newScheduledTestDB(t)
	ctx := scheduledTestContext(t)
	seedScheduledTestOwner(t, ctx, db)
	svc := New(db)
	now := time.Now().UTC().UnixMilli()
	for i := 0; i < 10; i++ {
		createScheduledTestPost(t, ctx, svc, uuid.NewString(), now-1_000)
	}

	start := make(chan struct{})
	results := make(chan []*ScheduledPost, 2)
	errorsCh := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			claimed, err := svc.ClaimDue(ctx, now, 5)
			results <- claimed
			errorsCh <- err
		}()
	}
	close(start)

	seenIDs := map[string]struct{}{}
	seenTokens := map[string]struct{}{}
	for range 2 {
		claimed := <-results
		if err := <-errorsCh; err != nil {
			t.Fatalf("concurrent claim: %v", err)
		}
		for _, item := range claimed {
			if _, exists := seenIDs[item.ID]; exists {
				t.Fatalf("scheduled post %s was claimed twice", item.ID)
			}
			if _, exists := seenTokens[item.ClaimToken]; exists {
				t.Fatalf("claim token %s was reused", item.ClaimToken)
			}
			seenIDs[item.ID] = struct{}{}
			seenTokens[item.ClaimToken] = struct{}{}
		}
	}
	if len(seenIDs) != 10 {
		t.Fatalf("concurrently claimed rows = %d, want 10", len(seenIDs))
	}
}

func newScheduledTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(scheduledTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", scheduledTestPostgresDSN)
	}
	ctx := scheduledTestContext(t)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open scheduled test admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping scheduled test PostgreSQL: %v", err)
	}

	schemaName := "moyro_scheduled_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create scheduled test schema: %v", err)
	}

	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop scheduled test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse scheduled test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 6
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open scheduled test pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping scheduled test pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate scheduled test schema: %v", err)
	}
	return db
}

func seedScheduledTestOwner(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES ('scheduled-user', 'scheduled-user', 'scheduled@example.test', 'hash', 'system_user', 1, 1);
		INSERT INTO teams (id, name, display_name, type, create_at, update_at)
		VALUES ('scheduled-team', 'scheduled-team', 'Scheduled Team', 'O', 1, 1);
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES ('scheduled-channel', 'scheduled-team', 'O', 'Scheduled Channel', 'scheduled-channel', 1, 1);
	`); err != nil {
		t.Fatalf("seed scheduled test owner: %v", err)
	}
}

func createScheduledTestPost(t *testing.T, ctx context.Context, svc *Service, message string, sendAt int64) *ScheduledPost {
	t.Helper()
	sp, err := svc.Create(ctx, "scheduled-user", "scheduled-channel", "", message, nil, nil, sendAt)
	if err != nil {
		t.Fatalf("create scheduled test post %q: %v", message, err)
	}
	return sp
}

func assertScheduledState(t *testing.T, ctx context.Context, db *store.DB, id, wantStatus string, wantSentAt, wantNextAttemptAt int64, wantResultPostID string) {
	t.Helper()
	var status string
	var sentAt, nextAttemptAt int64
	var resultPostID *string
	if err := db.Pool.QueryRow(ctx, `
		SELECT status, sent_at, next_attempt_at, result_post_id
		FROM scheduled_posts WHERE id=$1`, id).Scan(&status, &sentAt, &nextAttemptAt, &resultPostID); err != nil {
		t.Fatalf("read scheduled state %s: %v", id, err)
	}
	gotResultPostID := ""
	if resultPostID != nil {
		gotResultPostID = *resultPostID
	}
	if status != wantStatus || sentAt != wantSentAt || nextAttemptAt != wantNextAttemptAt || gotResultPostID != wantResultPostID {
		t.Fatalf("scheduled state %s = (%q, %d, %d, %q), want (%q, %d, %d, %q)", id, status, sentAt, nextAttemptAt, gotResultPostID, wantStatus, wantSentAt, wantNextAttemptAt, wantResultPostID)
	}
}

func readScheduledClaimState(t *testing.T, ctx context.Context, db *store.DB, id string) (string, string, int64, int) {
	t.Helper()
	var status, token string
	var leaseUntil int64
	var attempts int
	if err := db.Pool.QueryRow(ctx, `
		SELECT status, claim_token, lease_until, attempt_count
		FROM scheduled_posts WHERE id=$1`, id).Scan(&status, &token, &leaseUntil, &attempts); err != nil {
		t.Fatalf("read scheduled claim state %s: %v", id, err)
	}
	return status, token, leaseUntil, attempts
}

func containsScheduledPost(posts []*ScheduledPost, id string) bool {
	for _, post := range posts {
		if post.ID == id {
			return true
		}
	}
	return false
}

func scheduledIDs(posts []*ScheduledPost) []string {
	ids := make([]string, 0, len(posts))
	for _, post := range posts {
		ids = append(ids, post.ID)
	}
	return ids
}

func scheduledTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}
