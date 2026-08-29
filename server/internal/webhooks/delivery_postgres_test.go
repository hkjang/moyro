package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const webhookTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

func TestDeliveryPostgresEnqueueMasksDeduplicatesAndDeadLettersOversize(t *testing.T) {
	db := newWebhookTestDB(t)
	ctx := webhookTestContext(t)
	seedWebhookTestScope(t, ctx, db)
	outgoing := NewOutgoing(db)
	hook := createWebhookTestHook(t, ctx, outgoing, []string{
		"https://first.example.test/hook",
		"https://second.example.test/hook",
		"https://first.example.test/hook",
	})
	queue := NewDeliveryService(db)
	now := time.Now().UTC().UnixMilli()
	job := webhookTestJob(hook, "post-enqueue", "deploy safely")

	first, err := queue.enqueue(ctx, job, now)
	if err != nil {
		t.Fatalf("enqueue outgoing deliveries: %v", err)
	}
	if first.Inserted != 2 || first.Replayed != 0 || first.Rejected != 0 {
		t.Fatalf("first enqueue = %#v", first)
	}
	second, err := queue.enqueue(ctx, job, now+1)
	if err != nil {
		t.Fatalf("replay outgoing deliveries: %v", err)
	}
	if second.EventID != first.EventID || second.Inserted != 0 || second.Replayed != 2 {
		t.Fatalf("replayed enqueue = %#v, first = %#v", second, first)
	}

	rows, err := db.Pool.Query(ctx, `
		SELECT id, event_id, payload
		FROM integration_deliveries
		WHERE event_id=$1
		ORDER BY id`, first.EventID)
	if err != nil {
		t.Fatalf("query persisted deliveries: %v", err)
	}
	defer rows.Close()
	ids := make(map[string]struct{})
	for rows.Next() {
		var id, eventID string
		var raw []byte
		if err := rows.Scan(&id, &eventID, &raw); err != nil {
			t.Fatalf("scan persisted delivery: %v", err)
		}
		if eventID != first.EventID {
			t.Fatalf("event id = %q, want %q", eventID, first.EventID)
		}
		if strings.Contains(string(raw), hook.Token) {
			t.Fatal("persisted delivery contains usable hook token")
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode persisted body: %v", err)
		}
		if body["token"] != redactedPayloadValue || body["text"] != job.post.Message {
			t.Fatalf("persisted body = %#v", body)
		}
		ids[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate persisted deliveries: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("persisted delivery ids = %d, want 2", len(ids))
	}
	if depth, err := queue.outstandingCount(ctx); err != nil || depth != 2 {
		t.Fatalf("durable queue depth = %d, %v", depth, err)
	}

	oversized := webhookTestJob(hook, "post-oversized", strings.Repeat("x", maxPersistedPayloadSize+1))
	rejected, err := queue.enqueue(ctx, oversized, now+2)
	if err != nil {
		t.Fatalf("enqueue oversized audit records: %v", err)
	}
	if rejected.Inserted != 2 || rejected.Rejected != 2 {
		t.Fatalf("oversized enqueue = %#v", rejected)
	}
	var deadCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM integration_deliveries
		WHERE event_id=$1 AND status='dead' AND last_error_code='payload_too_large'
		  AND payload->>'payload_rejected'='true'
	`, rejected.EventID).Scan(&deadCount); err != nil {
		t.Fatalf("query oversized dead letters: %v", err)
	}
	if deadCount != 2 {
		t.Fatalf("oversized dead letters = %d, want 2", deadCount)
	}
	if depth, err := queue.outstandingCount(ctx); err != nil || depth != 2 {
		t.Fatalf("dead letters affected queue depth = %d, %v", depth, err)
	}
}

func TestDeliveryPostgresLeaseCASRetryAndDeadState(t *testing.T) {
	db := newWebhookTestDB(t)
	ctx := webhookTestContext(t)
	seedWebhookTestScope(t, ctx, db)
	outgoing := NewOutgoing(db)
	hook := createWebhookTestHook(t, ctx, outgoing, []string{"https://retry.example.test/hook"})
	queue := NewDeliveryService(db)
	now := time.Now().UTC().UnixMilli()
	result, err := queue.enqueue(ctx, webhookTestJob(hook, "post-lease", "deploy retry"), now)
	if err != nil || result.Inserted != 1 {
		t.Fatalf("enqueue lease test delivery = %#v, %v", result, err)
	}

	first, err := queue.claimDue(ctx, now, 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim = %#v, %v", first, err)
	}
	if first[0].Status != deliveryStatusProcessing || first[0].AttemptCount != 1 || first[0].ClaimToken == "" {
		t.Fatalf("first claim metadata = %#v", first[0])
	}
	if early, err := queue.claimDue(ctx, first[0].LeaseUntil-1, 1); err != nil || len(early) != 0 {
		t.Fatalf("claim before lease expiry = %#v, %v", early, err)
	}

	reclaimed, err := queue.claimDue(ctx, first[0].LeaseUntil, 1)
	if err != nil || len(reclaimed) != 1 {
		t.Fatalf("expired lease reclaim = %#v, %v", reclaimed, err)
	}
	if reclaimed[0].ID != first[0].ID || reclaimed[0].ClaimToken == first[0].ClaimToken || reclaimed[0].AttemptCount != 2 {
		t.Fatalf("reclaimed metadata = %#v, old token %q", reclaimed[0], first[0].ClaimToken)
	}
	failureAt := reclaimed[0].ClaimedAt + 10
	staleAttempt := deliveryAttempt{
		StartedAt:   first[0].ClaimedAt,
		CompletedAt: failureAt,
		ErrorCode:   "request_failed",
		ErrorText:   "stale worker",
	}
	if err := queue.finalize(ctx, first[0], staleAttempt); !errors.Is(err, ErrStaleDeliveryClaim) {
		t.Fatalf("stale finalize error = %v", err)
	}
	retryAttempt := deliveryAttempt{
		StartedAt:      reclaimed[0].ClaimedAt,
		CompletedAt:    failureAt,
		ResponseStatus: http.StatusServiceUnavailable,
		ErrorCode:      "http_503",
		ErrorText:      "temporary failure",
	}
	if err := queue.finalize(ctx, reclaimed[0], retryAttempt); err != nil {
		t.Fatalf("finalize retry: %v", err)
	}
	wantNext := failureAt + deliveryRetryDelay(reclaimed[0].AttemptCount).Milliseconds()
	assertDeliveryState(t, ctx, db, reclaimed[0].ID, deliveryStatusRetry, wantNext, 2, "http_503")
	if early, err := queue.claimDue(ctx, wantNext-1, 1); err != nil || len(early) != 0 {
		t.Fatalf("retry claimed before backoff = %#v, %v", early, err)
	}

	if _, err := db.Pool.Exec(ctx, `
		UPDATE integration_deliveries
		SET attempt_count=$1, next_attempt_at=$2::BIGINT
		WHERE id=$3`, deliveryMaxAttempts-1, wantNext, reclaimed[0].ID); err != nil {
		t.Fatalf("prepare final attempt: %v", err)
	}
	finalClaim, err := queue.claimDue(ctx, wantNext, 1)
	if err != nil || len(finalClaim) != 1 || finalClaim[0].AttemptCount != deliveryMaxAttempts {
		t.Fatalf("final claim = %#v, %v", finalClaim, err)
	}
	deadAt := finalClaim[0].ClaimedAt + 5
	if err := queue.finalize(ctx, finalClaim[0], deliveryAttempt{
		StartedAt:      finalClaim[0].ClaimedAt,
		CompletedAt:    deadAt,
		ResponseStatus: http.StatusServiceUnavailable,
		ErrorCode:      "http_503",
		ErrorText:      "attempt limit reached",
	}); err != nil {
		t.Fatalf("finalize dead delivery: %v", err)
	}
	assertDeliveryState(t, ctx, db, finalClaim[0].ID, deliveryStatusDead, 0, deliveryMaxAttempts, "http_503")

	var attemptCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM integration_delivery_attempts
		WHERE delivery_id=$1`, finalClaim[0].ID).Scan(&attemptCount); err != nil {
		t.Fatalf("count attempt history: %v", err)
	}
	if attemptCount != 2 {
		t.Fatalf("attempt history rows = %d, want 2 successful CAS finalizations", attemptCount)
	}
}

func TestDeliveryPostgresConcurrentClaimsDoNotOverlap(t *testing.T) {
	db := newWebhookTestDB(t)
	ctx := webhookTestContext(t)
	seedWebhookTestScope(t, ctx, db)
	outgoing := NewOutgoing(db)
	callbacks := make([]string, 10)
	for index := range callbacks {
		callbacks[index] = "https://callback-" + string(rune('a'+index)) + ".example.test/hook"
	}
	hook := createWebhookTestHook(t, ctx, outgoing, callbacks)
	queue := NewDeliveryService(db)
	now := time.Now().UTC().UnixMilli()
	result, err := queue.enqueue(ctx, webhookTestJob(hook, "post-concurrent", "deploy all"), now)
	if err != nil || result.Inserted != len(callbacks) {
		t.Fatalf("enqueue concurrent deliveries = %#v, %v", result, err)
	}

	type claimResult struct {
		deliveries []*Delivery
		err        error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	for range 2 {
		go func() {
			<-start
			claimed, claimErr := queue.claimDue(ctx, now, 5)
			results <- claimResult{deliveries: claimed, err: claimErr}
		}()
	}
	close(start)

	seenIDs := make(map[string]struct{}, len(callbacks))
	seenTokens := make(map[string]struct{}, len(callbacks))
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent claim: %v", result.err)
		}
		for _, delivery := range result.deliveries {
			if _, duplicate := seenIDs[delivery.ID]; duplicate {
				t.Fatalf("delivery %s was claimed by two workers", delivery.ID)
			}
			if _, duplicate := seenTokens[delivery.ClaimToken]; duplicate {
				t.Fatalf("claim token %s was reused", delivery.ClaimToken)
			}
			seenIDs[delivery.ID] = struct{}{}
			seenTokens[delivery.ClaimToken] = struct{}{}
		}
	}
	if len(seenIDs) != len(callbacks) {
		t.Fatalf("concurrently claimed deliveries = %d, want %d", len(seenIDs), len(callbacks))
	}
}

func TestDeliveryPostgresRestartDeliveryUsesStableHeadersAndCurrentToken(t *testing.T) {
	db := newWebhookTestDB(t)
	ctx := webhookTestContext(t)
	seedWebhookTestScope(t, ctx, db)

	type receivedRequest struct {
		body        map[string]any
		eventID     string
		deliveryID  string
		idempotency string
	}
	received := make(chan receivedRequest, 3)
	var responseStatus atomic.Int32
	responseStatus.Store(http.StatusNoContent)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		received <- receivedRequest{
			body:        body,
			eventID:     request.Header.Get("X-Moyro-Event-ID"),
			deliveryID:  request.Header.Get("X-Moyro-Delivery-ID"),
			idempotency: request.Header.Get("Idempotency-Key"),
		}
		w.WriteHeader(int(responseStatus.Load()))
	}))
	defer server.Close()

	outgoing := NewOutgoing(db)
	hook := createWebhookTestHook(t, ctx, outgoing, []string{server.URL + "/callback"})
	originalToken := hook.Token
	queueBeforeRestart := NewDeliveryService(db)
	now := time.Now().UTC().UnixMilli()
	enqueued, err := queueBeforeRestart.enqueue(ctx, webhookTestJob(hook, "post-restart", "deploy after restart"), now)
	if err != nil || enqueued.Inserted != 1 {
		t.Fatalf("enqueue restart delivery = %#v, %v", enqueued, err)
	}
	rotatedHook, err := outgoing.RegenerateToken(ctx, hook.ID)
	if err != nil {
		t.Fatalf("rotate hook token: %v", err)
	}

	// A distinct service instance has no in-memory state from enqueue. Claiming
	// and delivering the row proves restart durability.
	queueAfterRestart := NewDeliveryService(db)
	claimed, err := queueAfterRestart.claimDue(ctx, now, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim after restart = %#v, %v", claimed, err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dispatcher := &Dispatcher{
		svc:               outgoing,
		queue:             queueAfterRestart,
		client:            newOutboundHTTPClient(),
		logger:            logger,
		allowedHosts:      map[string]struct{}{"127.0.0.1": {}},
		allowedConfigured: true,
	}
	dispatcher.deliver(claimed[0])

	request := <-received
	if request.body["token"] != rotatedHook.Token || request.body["token"] == originalToken {
		t.Fatalf("delivered token = %#v, rotated=%q original=%q", request.body["token"], rotatedHook.Token, originalToken)
	}
	if request.eventID != claimed[0].EventID || request.deliveryID != claimed[0].ID || request.idempotency != claimed[0].ID {
		t.Fatalf("delivery headers = event %q delivery %q idempotency %q", request.eventID, request.deliveryID, request.idempotency)
	}
	assertDeliveryState(t, ctx, db, claimed[0].ID, deliveryStatusSucceeded, 0, 1, "")
	assertDeliveryAttempt(t, ctx, db, claimed[0].ID, 1, http.StatusNoContent, true)

	var persistedPayload []byte
	if err := db.Pool.QueryRow(ctx, `
		SELECT payload FROM integration_deliveries WHERE id=$1`, claimed[0].ID).Scan(&persistedPayload); err != nil {
		t.Fatalf("read delivered payload: %v", err)
	}
	if strings.Contains(string(persistedPayload), originalToken) || strings.Contains(string(persistedPayload), rotatedHook.Token) {
		t.Fatal("persisted payload contains a usable hook token")
	}
	if depth := dispatcher.QueueDepth(); depth != 0 {
		t.Fatalf("queue depth after success = %d", depth)
	}

	responseStatus.Store(http.StatusServiceUnavailable)
	retryEnqueue, err := queueAfterRestart.enqueue(ctx, webhookTestJob(rotatedHook, "post-http-retry", "deploy retry response"), time.Now().UTC().UnixMilli())
	if err != nil || retryEnqueue.Inserted != 1 {
		t.Fatalf("enqueue HTTP retry delivery = %#v, %v", retryEnqueue, err)
	}
	retryClaim, err := queueAfterRestart.claimDue(ctx, time.Now().UTC().UnixMilli(), 1)
	if err != nil || len(retryClaim) != 1 {
		t.Fatalf("claim HTTP retry delivery = %#v, %v", retryClaim, err)
	}
	dispatcher.deliver(retryClaim[0])
	<-received
	var retryStatus string
	var retryAt int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT status, next_attempt_at
		FROM integration_deliveries WHERE id=$1`, retryClaim[0].ID).Scan(&retryStatus, &retryAt); err != nil {
		t.Fatalf("read HTTP retry state: %v", err)
	}
	if retryStatus != deliveryStatusRetry || retryAt <= retryClaim[0].ClaimedAt {
		t.Fatalf("HTTP retry state = (%q, %d), claimed at %d", retryStatus, retryAt, retryClaim[0].ClaimedAt)
	}
	assertDeliveryAttempt(t, ctx, db, retryClaim[0].ID, 1, http.StatusServiceUnavailable, false)
}

func newWebhookTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(webhookTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", webhookTestPostgresDSN)
	}
	ctx := webhookTestContext(t)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open webhook test admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping webhook test PostgreSQL: %v", err)
	}

	schemaName := "moyro_webhooks_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create webhook test schema: %v", err)
	}

	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop webhook test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse webhook test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 8
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated webhook test pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated webhook test pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate webhook test schema: %v", err)
	}
	return db
}

func seedWebhookTestScope(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES ('webhook-user', 'webhook-user', 'webhook@example.test', 'hash', 'system_user', 1, 1);
		INSERT INTO teams (id, name, display_name, type, create_at, update_at)
		VALUES ('webhook-team', 'webhook-team', 'Webhook Team', 'O', 1, 1);
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES ('webhook-channel', 'webhook-team', 'O', 'Webhook Channel', 'webhook-channel', 1, 1);
	`); err != nil {
		t.Fatalf("seed webhook test scope: %v", err)
	}
}

func createWebhookTestHook(t *testing.T, ctx context.Context, service *OutgoingService, callbacks []string) *OutgoingHook {
	t.Helper()
	hook, err := service.Create(ctx, "webhook-user", "webhook-team", "webhook-channel",
		[]string{"deploy"}, callbacks, 0, "Test Hook", "application/json")
	if err != nil {
		t.Fatalf("create webhook test hook: %v", err)
	}
	return hook
}

func webhookTestJob(hook *OutgoingHook, postID, message string) dispatchJob {
	return dispatchJob{
		hook: *hook,
		post: &posts.Post{
			ID:        postID,
			ChannelID: "webhook-channel",
			UserID:    "webhook-user",
			Message:   message,
			Props:     map[string]any{"webhook_depth": float64(1)},
			CreateAt:  time.Now().UTC().UnixMilli(),
		},
		user: "webhook-user",
	}
}

func assertDeliveryState(t *testing.T, ctx context.Context, db *store.DB, id, wantStatus string, wantNext int64, wantAttempts int, wantErrorCode string) {
	t.Helper()
	var status, errorCode, claimToken string
	var nextAttemptAt, claimedAt, leaseUntil int64
	var attempts int
	if err := db.Pool.QueryRow(ctx, `
		SELECT status, next_attempt_at, attempt_count, last_error_code,
		       claimed_at, lease_until, claim_token
		FROM integration_deliveries WHERE id=$1`, id).Scan(
		&status, &nextAttemptAt, &attempts, &errorCode,
		&claimedAt, &leaseUntil, &claimToken,
	); err != nil {
		t.Fatalf("read delivery state %s: %v", id, err)
	}
	if status != wantStatus || nextAttemptAt != wantNext || attempts != wantAttempts || errorCode != wantErrorCode {
		t.Fatalf("delivery state %s = (%q, %d, %d, %q), want (%q, %d, %d, %q)",
			id, status, nextAttemptAt, attempts, errorCode, wantStatus, wantNext, wantAttempts, wantErrorCode)
	}
	if claimedAt != 0 || leaseUntil != 0 || claimToken != "" {
		t.Fatalf("final delivery still leased: claimed=%d lease=%d token=%q", claimedAt, leaseUntil, claimToken)
	}
}

func assertDeliveryAttempt(t *testing.T, ctx context.Context, db *store.DB, deliveryID string, attemptNumber, responseStatus int, succeeded bool) {
	t.Helper()
	var gotStatus int
	var gotSucceeded bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT response_status, succeeded
		FROM integration_delivery_attempts
		WHERE delivery_id=$1 AND attempt_number=$2`, deliveryID, attemptNumber).Scan(&gotStatus, &gotSucceeded); err != nil {
		t.Fatalf("read delivery attempt %s/%d: %v", deliveryID, attemptNumber, err)
	}
	if gotStatus != responseStatus || gotSucceeded != succeeded {
		t.Fatalf("delivery attempt %s/%d = (%d, %v), want (%d, %v)",
			deliveryID, attemptNumber, gotStatus, gotSucceeded, responseStatus, succeeded)
	}
}

func webhookTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	return ctx
}
