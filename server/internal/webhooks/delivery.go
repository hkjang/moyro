package webhooks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/store"
)

const (
	deliveryStatusPending    = "pending"
	deliveryStatusProcessing = "processing"
	deliveryStatusRetry      = "retry"
	deliveryStatusSucceeded  = "succeeded"
	deliveryStatusDead       = "dead"

	deliveryLeaseDuration   = 30 * time.Second
	deliveryPollInterval    = time.Second
	deliveryClaimTimeout    = 5 * time.Second
	deliveryFinalizeTimeout = 5 * time.Second
	deliveryRequestTimeout  = 12 * time.Second
	deliveryMaxAttempts     = 8
	deliveryInitialBackoff  = 30 * time.Second
	deliveryMaximumBackoff  = time.Hour
	// Leave headroom below the database's 256 KiB JSONB text constraint because
	// PostgreSQL's canonical JSON rendering adds insignificant whitespace.
	maxPersistedPayloadSize = 240 << 10
	maxDrainedResponseSize  = 64 << 10
	redactedPayloadValue    = "[REDACTED]"
)

var ErrStaleDeliveryClaim = errors.New("webhooks: stale delivery claim")

// Delivery is one callback URL invocation. A hook with multiple callback URLs
// creates one row per URL so one slow or broken endpoint cannot hold back the
// others. Payload deliberately excludes usable credentials.
type Delivery struct {
	ID             string
	EventID        string
	IntegrationID  string
	CallbackURL    string
	ContentType    string
	Payload        []byte
	Status         string
	AttemptCount   int
	NextAttemptAt  int64
	ClaimedAt      int64
	LeaseUntil     int64
	ClaimToken     string
	LastErrorCode  string
	LastErrorText  string
	ResponseStatus int
	CreateAt       int64
	UpdateAt       int64
	SucceededAt    int64
	DeadAt         int64
}

type DeliveryService struct{ db *store.DB }

func NewDeliveryService(db *store.DB) *DeliveryService { return &DeliveryService{db: db} }

type enqueueResult struct {
	EventID  string
	Inserted int
	Replayed int
	Rejected int
}

// enqueue persists all callback work before Dispatch returns. The post itself
// is already committed by the current PostCommand call site, so this closes the
// process-restart loss window but is not yet transactionally atomic with post
// creation. Moving this INSERT into the future post transaction remains needed
// for a true domain-event outbox.
func (s *DeliveryService) enqueue(ctx context.Context, job dispatchJob, now int64) (enqueueResult, error) {
	result := enqueueResult{EventID: stableWebhookID("event", job.hook.ID, job.post.ID)}
	if s == nil || s.db == nil || s.db.Pool == nil {
		return result, errors.New("webhooks: nil delivery store")
	}

	payload, payloadErr := persistedOutgoingPayload(job)
	status := deliveryStatusPending
	nextAttemptAt := now
	lastErrorCode := ""
	lastErrorText := ""
	deadAt := int64(0)
	if payloadErr != nil {
		// Keep an auditable dead-letter row rather than losing oversized work.
		// The diagnostic body contains identifiers only, never the original text.
		payload, _ = json.Marshal(map[string]any{
			"post_id":          job.post.ID,
			"payload_rejected": true,
		})
		status = deliveryStatusDead
		nextAttemptAt = 0
		lastErrorCode = "payload_too_large"
		lastErrorText = truncateDeliveryError(payloadErr.Error())
		deadAt = now
	}

	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("webhooks: begin delivery enqueue: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	contentType := strings.TrimSpace(job.hook.ContentType)
	if contentType == "" {
		contentType = "application/json"
	}
	seenCallbacks := make(map[string]struct{}, len(job.hook.CallbackURLs))
	for _, callbackURL := range job.hook.CallbackURLs {
		callbackURL = strings.TrimSpace(callbackURL)
		if _, duplicate := seenCallbacks[callbackURL]; duplicate {
			continue
		}
		seenCallbacks[callbackURL] = struct{}{}
		deliveryID := stableWebhookID("delivery", result.EventID, callbackURL)
		tag, err := tx.Exec(ctx, `
			INSERT INTO integration_deliveries
				(id, event_id, integration_type, integration_id, callback_url, content_type,
				 payload, status, next_attempt_at, last_error_code, last_error_text,
				 create_at, update_at, dead_at)
			VALUES ($1, $2, 'outgoing_webhook', $3, $4, $5,
			        $6, $7, $8::BIGINT, $9, $10, $11::BIGINT, $11::BIGINT, $12::BIGINT)
			ON CONFLICT DO NOTHING
		`, deliveryID, result.EventID, job.hook.ID, callbackURL, contentType,
			payload, status, nextAttemptAt, lastErrorCode, lastErrorText, now, deadAt)
		if err != nil {
			return result, fmt.Errorf("webhooks: enqueue delivery: %w", err)
		}
		if tag.RowsAffected() == 0 {
			result.Replayed++
			continue
		}
		result.Inserted++
		if payloadErr != nil {
			result.Rejected++
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("webhooks: commit delivery enqueue: %w", err)
	}
	return result, nil
}

// claimDue atomically leases due or abandoned work. FOR UPDATE SKIP LOCKED and
// per-row tokens make concurrent processes safe; finalizers must CAS the token.
func (s *DeliveryService) claimDue(ctx context.Context, now int64, limit int) ([]*Delivery, error) {
	if limit <= 0 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	leaseUntil := now + deliveryLeaseDuration.Milliseconds()
	rows, err := s.db.Pool.Query(ctx, `
		WITH due AS (
			SELECT id
			FROM integration_deliveries
			WHERE (status IN ('pending', 'retry') AND next_attempt_at <= $1::BIGINT)
			   OR (status = 'processing' AND lease_until <= $1::BIGINT)
			ORDER BY CASE WHEN status = 'processing' THEN lease_until ELSE next_attempt_at END,
			         create_at, id
			LIMIT $2::INTEGER
			FOR UPDATE SKIP LOCKED
		), leased AS MATERIALIZED (
			SELECT id, gen_random_uuid()::TEXT AS claim_token
			FROM due
		)
		UPDATE integration_deliveries AS delivery
		SET status = 'processing',
		    claimed_at = $1::BIGINT,
		    lease_until = $3::BIGINT,
		    claim_token = leased.claim_token,
		    attempt_count = delivery.attempt_count + 1,
		    update_at = $1::BIGINT
		FROM leased
		WHERE delivery.id = leased.id
		RETURNING delivery.id, delivery.event_id, delivery.integration_id,
		          delivery.callback_url, delivery.content_type, delivery.payload,
		          delivery.status, delivery.attempt_count, delivery.next_attempt_at,
		          delivery.claimed_at, delivery.lease_until, delivery.claim_token,
		          delivery.last_error_code, delivery.last_error_text,
		          delivery.response_status, delivery.create_at, delivery.update_at,
		          delivery.succeeded_at, delivery.dead_at
	`, now, limit, leaseUntil)
	if err != nil {
		return nil, fmt.Errorf("webhooks: claim deliveries: %w", err)
	}
	defer rows.Close()

	claimed := make([]*Delivery, 0, limit)
	for rows.Next() {
		delivery, err := scanDelivery(rows)
		if err != nil {
			return nil, fmt.Errorf("webhooks: scan claimed delivery: %w", err)
		}
		claimed = append(claimed, delivery)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("webhooks: iterate claimed deliveries: %w", err)
	}
	return claimed, nil
}

type deliveryAttempt struct {
	StartedAt      int64
	CompletedAt    int64
	ResponseStatus int
	ErrorCode      string
	ErrorText      string
	Succeeded      bool
	Permanent      bool
}

// finalize records the attempt and changes state in one transaction. A stale
// worker cannot overwrite a newer lease, even if its HTTP request completed.
func (s *DeliveryService) finalize(ctx context.Context, delivery *Delivery, attempt deliveryAttempt) error {
	if delivery == nil || delivery.ClaimToken == "" {
		return ErrStaleDeliveryClaim
	}
	if attempt.CompletedAt < attempt.StartedAt {
		attempt.CompletedAt = attempt.StartedAt
	}
	attempt.ErrorText = truncateDeliveryError(attempt.ErrorText)

	status := deliveryStatusRetry
	nextAttemptAt := attempt.CompletedAt + deliveryRetryDelay(delivery.AttemptCount).Milliseconds()
	succeededAt := int64(0)
	deadAt := int64(0)
	if attempt.Succeeded {
		status = deliveryStatusSucceeded
		nextAttemptAt = 0
		succeededAt = attempt.CompletedAt
		attempt.ErrorCode = ""
		attempt.ErrorText = ""
	} else if attempt.Permanent || delivery.AttemptCount >= deliveryMaxAttempts {
		status = deliveryStatusDead
		nextAttemptAt = 0
		deadAt = attempt.CompletedAt
	}

	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("webhooks: begin delivery finalize: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	tag, err := tx.Exec(ctx, `
		UPDATE integration_deliveries
		SET status = $1,
		    next_attempt_at = $2::BIGINT,
		    claimed_at = 0,
		    lease_until = 0,
		    claim_token = '',
		    last_error_code = $3,
		    last_error_text = $4,
		    response_status = $5,
		    update_at = $6::BIGINT,
		    succeeded_at = $7::BIGINT,
		    dead_at = $8::BIGINT
		WHERE id = $9 AND status = 'processing' AND claim_token = $10
	`, status, nextAttemptAt, attempt.ErrorCode, attempt.ErrorText,
		attempt.ResponseStatus, attempt.CompletedAt, succeededAt, deadAt,
		delivery.ID, delivery.ClaimToken)
	if err != nil {
		return fmt.Errorf("webhooks: update delivery result: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: delivery %s", ErrStaleDeliveryClaim, delivery.ID)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO integration_delivery_attempts
			(delivery_id, attempt_number, claim_token, started_at, completed_at,
			 duration_ms, response_status, error_code, error_text, succeeded)
		VALUES ($1, $2, $3, $4::BIGINT, $5::BIGINT, $6::BIGINT, $7, $8, $9, $10)
	`, delivery.ID, delivery.AttemptCount, delivery.ClaimToken,
		attempt.StartedAt, attempt.CompletedAt, attempt.CompletedAt-attempt.StartedAt,
		attempt.ResponseStatus, attempt.ErrorCode, attempt.ErrorText, attempt.Succeeded); err != nil {
		return fmt.Errorf("webhooks: record delivery attempt: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("webhooks: commit delivery result: %w", err)
	}
	return nil
}

func (s *DeliveryService) outstandingCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM integration_deliveries
		WHERE status IN ('pending', 'processing', 'retry')
	`).Scan(&count)
	return count, err
}

type deliveryScannable interface {
	Scan(dest ...any) error
}

func scanDelivery(row deliveryScannable) (*Delivery, error) {
	var delivery Delivery
	if err := row.Scan(
		&delivery.ID, &delivery.EventID, &delivery.IntegrationID,
		&delivery.CallbackURL, &delivery.ContentType, &delivery.Payload,
		&delivery.Status, &delivery.AttemptCount, &delivery.NextAttemptAt,
		&delivery.ClaimedAt, &delivery.LeaseUntil, &delivery.ClaimToken,
		&delivery.LastErrorCode, &delivery.LastErrorText,
		&delivery.ResponseStatus, &delivery.CreateAt, &delivery.UpdateAt,
		&delivery.SucceededAt, &delivery.DeadAt,
	); err != nil {
		return nil, err
	}
	return &delivery, nil
}

// QueueDepth preserves the existing monitoring API, but now counts durable
// outstanding rows instead of reading an in-memory channel length.
func (d *Dispatcher) QueueDepth() int {
	if d == nil || d.queue == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), deliveryClaimTimeout)
	defer cancel()
	count, err := d.queue.outstandingCount(ctx)
	if err != nil {
		d.logger.Warn("outgoing queue depth query failed", "err", err)
		return 0
	}
	return count
}

// Dispatch matches active hooks and synchronously persists callback rows. The
// HTTP request still runs asynchronously, but queue saturation can no longer
// drop work because PostgreSQL, not a bounded channel, is the source of truth.
func (d *Dispatcher) Dispatch(ctx context.Context, post *posts.Post, authorUsername string) {
	if d == nil || d.svc == nil || d.queue == nil || d.teamOf == nil || post == nil || post.Message == "" {
		return
	}
	depth := webhookDepth(post)
	if depth >= maximumWebhookDepth {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	enqueueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deliveryClaimTimeout)
	defer cancel()

	teamID, err := d.teamOf(enqueueCtx, post.ChannelID)
	if err != nil || teamID == "" {
		if err != nil {
			d.logger.Warn("outgoing team resolution failed", "post", post.ID, "err", err)
		}
		return
	}
	hooks, err := d.svc.candidatesFor(enqueueCtx, teamID, post.ChannelID)
	if err != nil {
		d.logger.Warn("outgoing candidate query failed", "post", post.ID, "err", err)
		return
	}
	for _, hook := range hooks {
		if !matchTrigger(post.Message, hook.TriggerWords, hook.TriggerWhen) {
			continue
		}
		result, err := d.queue.enqueue(enqueueCtx, dispatchJob{
			hook: hook,
			post: withDepth(post, depth+1),
			user: authorUsername,
		}, time.Now().UTC().UnixMilli())
		if err != nil {
			// Dispatch cannot return an error without breaking the existing
			// postcommand interface. Persistence failures are therefore explicit
			// operator-visible errors, never silent bounded-channel drops.
			d.logger.Error("outgoing durable enqueue failed", "hook", hook.ID, "post", post.ID, "err", err)
			continue
		}
		if result.Rejected > 0 {
			d.logger.Warn("outgoing payload rejected by size limit", "hook", hook.ID, "post", post.ID, "deliveries", result.Rejected)
		}
		d.signalWorkers(result.Inserted - result.Rejected)
	}
}

func webhookDepth(post *posts.Post) int {
	if post == nil {
		return 0
	}
	value, ok := post.Props["webhook_depth"]
	if !ok {
		return 0
	}
	switch depth := value.(type) {
	case float64:
		return int(depth)
	case int:
		return depth
	case int64:
		return int(depth)
	case json.Number:
		parsed, _ := depth.Int64()
		return int(parsed)
	default:
		return 0
	}
}

func (d *Dispatcher) signalWorkers(count int) {
	if count > d.workers {
		count = d.workers
	}
	for i := 0; i < count; i++ {
		select {
		case d.wake <- struct{}{}:
		default:
			return
		}
	}
}

func (d *Dispatcher) runWorker() {
	ticker := time.NewTicker(deliveryPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-d.wake:
		}
		for d.processOne() {
		}
	}
}

func (d *Dispatcher) processOne() bool {
	// A nil startup policy means durable site settings are not loaded yet.
	// Do not claim work: hostAllowed would correctly fail closed, but marking
	// that temporary state permanent would destroy restart recovery semantics.
	if !d.policyReady() {
		return false
	}
	claimCtx, cancel := context.WithTimeout(context.Background(), deliveryClaimTimeout)
	claimed, err := d.queue.claimDue(claimCtx, time.Now().UTC().UnixMilli(), 1)
	cancel()
	if err != nil {
		d.logger.Warn("outgoing delivery claim failed", "err", err)
		return false
	}
	if len(claimed) == 0 {
		return false
	}
	d.deliver(claimed[0])
	return true
}

func (d *Dispatcher) deliver(delivery *Delivery) {
	started := time.Now().UTC()
	attempt := deliveryAttempt{StartedAt: started.UnixMilli()}

	if !d.hostAllowed(delivery.CallbackURL) {
		attempt.ErrorCode = "host_blocked"
		attempt.ErrorText = "outgoing callback URL is not allowed"
		attempt.Permanent = true
		d.finalizeDelivery(delivery, attempt)
		return
	}

	hookCtx, hookCancel := context.WithTimeout(context.Background(), deliveryClaimTimeout)
	hook, err := d.svc.Get(hookCtx, delivery.IntegrationID)
	hookCancel()
	if err != nil {
		attempt.ErrorCode = "hook_lookup_failed"
		attempt.ErrorText = err.Error()
		attempt.Permanent = errors.Is(err, ErrHookNotFound)
		d.finalizeDelivery(delivery, attempt)
		return
	}
	if !callbackStillConfigured(delivery.CallbackURL, hook.CallbackURLs) {
		attempt.ErrorCode = "callback_removed"
		attempt.ErrorText = "outgoing callback URL is no longer configured"
		attempt.Permanent = true
		d.finalizeDelivery(delivery, attempt)
		return
	}

	var body map[string]any
	if err := json.Unmarshal(delivery.Payload, &body); err != nil {
		attempt.ErrorCode = "invalid_payload"
		attempt.ErrorText = err.Error()
		attempt.Permanent = true
		d.finalizeDelivery(delivery, attempt)
		return
	}
	body["token"] = hook.Token
	raw, err := json.Marshal(body)
	if err != nil {
		attempt.ErrorCode = "invalid_payload"
		attempt.ErrorText = err.Error()
		attempt.Permanent = true
		d.finalizeDelivery(delivery, attempt)
		return
	}

	requestCtx, requestCancel := context.WithTimeout(context.Background(), deliveryRequestTimeout)
	defer requestCancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, delivery.CallbackURL, bytes.NewReader(raw))
	if err != nil {
		attempt.ErrorCode = "invalid_callback_url"
		attempt.ErrorText = err.Error()
		attempt.Permanent = true
		d.finalizeDelivery(delivery, attempt)
		return
	}
	contentType := strings.TrimSpace(delivery.ContentType)
	if contentType == "" {
		contentType = "application/json"
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "moyro-webhook/1.0")
	req.Header.Set("X-Moyro-Event-ID", delivery.EventID)
	req.Header.Set("X-Moyro-Delivery-ID", delivery.ID)
	req.Header.Set("Idempotency-Key", delivery.ID)

	resp, err := d.client.Do(req)
	if err != nil {
		attempt.ErrorCode = "request_failed"
		attempt.ErrorText = err.Error()
		attempt.Permanent = errors.Is(err, errOutgoingRedirect)
		d.finalizeDelivery(delivery, attempt)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainedResponseSize))
	_ = resp.Body.Close()
	attempt.ResponseStatus = resp.StatusCode
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		attempt.Succeeded = true
	} else {
		attempt.ErrorCode = fmt.Sprintf("http_%d", resp.StatusCode)
		attempt.ErrorText = fmt.Sprintf("outgoing callback returned HTTP %d", resp.StatusCode)
		attempt.Permanent = !retryableHTTPStatus(resp.StatusCode)
	}
	d.finalizeDelivery(delivery, attempt)
}

func (d *Dispatcher) finalizeDelivery(delivery *Delivery, attempt deliveryAttempt) {
	attempt.CompletedAt = time.Now().UTC().UnixMilli()
	ctx, cancel := context.WithTimeout(context.Background(), deliveryFinalizeTimeout)
	defer cancel()
	if err := d.queue.finalize(ctx, delivery, attempt); err != nil {
		if errors.Is(err, ErrStaleDeliveryClaim) {
			d.logger.Info("outgoing delivery lease became stale", "delivery", delivery.ID)
			return
		}
		d.logger.Error("outgoing delivery finalize failed", "delivery", delivery.ID, "err", err)
		return
	}
	if attempt.Succeeded {
		d.logger.Debug("outgoing delivery succeeded", "delivery", delivery.ID, "hook", delivery.IntegrationID)
		return
	}
	d.logger.Info("outgoing delivery failed", "delivery", delivery.ID, "hook", delivery.IntegrationID,
		"code", attempt.ErrorCode, "permanent", attempt.Permanent)
}

func persistedOutgoingPayload(job dispatchJob) ([]byte, error) {
	body := maskPayload(outgoingPayload(job)).(map[string]any)
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("webhooks: encode outgoing payload: %w", err)
	}
	if len(raw) > maxPersistedPayloadSize {
		return nil, fmt.Errorf("webhooks: payload is %d bytes; maximum is %d", len(raw), maxPersistedPayloadSize)
	}
	return raw, nil
}

func maskPayload(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		masked := make(map[string]any, len(typed))
		for key, child := range typed {
			if isSensitivePayloadKey(key) {
				masked[key] = redactedPayloadValue
				continue
			}
			masked[key] = maskPayload(child)
		}
		return masked
	case []any:
		masked := make([]any, len(typed))
		for i, child := range typed {
			masked[i] = maskPayload(child)
		}
		return masked
	default:
		return value
	}
}

func isSensitivePayloadKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"))
	switch normalized {
	case "token", "access_token", "refresh_token", "api_key", "password", "secret", "authorization":
		return true
	default:
		return false
	}
}

func stableWebhookID(kind string, parts ...string) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("moyro:outgoing-webhook:v1:" + kind))
	for _, part := range parts {
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(part))
	}
	return kind + "_" + hex.EncodeToString(digest.Sum(nil))
}

func retryableHTTPStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooEarly ||
		status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func callbackStillConfigured(callbackURL string, configured []string) bool {
	want := strings.TrimSpace(callbackURL)
	for _, current := range configured {
		if strings.TrimSpace(current) == want {
			return true
		}
	}
	return false
}

func deliveryRetryDelay(attemptCount int) time.Duration {
	if attemptCount < 1 {
		attemptCount = 1
	}
	delay := deliveryInitialBackoff
	for i := 1; i < attemptCount && delay < deliveryMaximumBackoff; i++ {
		delay *= 2
		if delay >= deliveryMaximumBackoff {
			return deliveryMaximumBackoff
		}
	}
	return delay
}

func truncateDeliveryError(message string) string {
	const maxErrorBytes = 4096
	if len(message) <= maxErrorBytes {
		return message
	}
	message = message[:maxErrorBytes]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}
