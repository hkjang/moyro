package approval

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// TestMarkExecutedIsIdempotentAcrossConcurrentExecutors pins the contract the
// decision handler and the recovery worker both rely on: whoever loses the
// approved -> executed transition still learns that the protected action ran.
// Before this was idempotent, the loser got ErrAlreadyDecided, which the HTTP
// layer rendered as 400 for a reviewer whose approval had in fact succeeded.
func TestMarkExecutedIsIdempotentAcrossConcurrentExecutors(t *testing.T) {
	db := newApprovalActivityTestDB(t)
	ctx := approvalActivityTestContext(t)
	seedApprovalActivityPrincipals(t, ctx, db)

	service := New(db, func(_ context.Context, reviewerID string, _ *Policy, _ *Request) (bool, error) {
		return reviewerID == "approval-reviewer", nil
	})
	requestID := approveTestRequest(t, ctx, service, "execute-once")

	first, err := service.MarkExecuted(ctx, requestID)
	if err != nil || first == nil || first.Status != "executed" {
		t.Fatalf("first MarkExecuted = %#v, %v", first, err)
	}
	if first.ExecutedAt <= 0 {
		t.Fatalf("executed_at = %d, want a stamp", first.ExecutedAt)
	}

	second, err := service.MarkExecuted(ctx, requestID)
	if err != nil {
		t.Fatalf("second MarkExecuted = %v, want the executed request", err)
	}
	if second == nil || second.Status != "executed" {
		t.Fatalf("second MarkExecuted = %#v, want status executed", second)
	}
	if second.ExecutedAt != first.ExecutedAt {
		t.Fatalf("executed_at moved %d -> %d; the winner's stamp must stand", first.ExecutedAt, second.ExecutedAt)
	}

	var outboxStatus string
	if err := db.Pool.QueryRow(ctx, `SELECT status FROM workflow_outbox WHERE request_id=$1`, requestID).Scan(&outboxStatus); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if outboxStatus != "succeeded" {
		t.Fatalf("outbox status = %q, want succeeded", outboxStatus)
	}
}

// TestMarkExecutedRejectsRequestsTheActionNeverReached keeps the idempotent
// path from swallowing a genuine misuse: only an already-executed request is a
// success, a rejected one is still an error.
func TestMarkExecutedRejectsRequestsTheActionNeverReached(t *testing.T) {
	db := newApprovalActivityTestDB(t)
	ctx := approvalActivityTestContext(t)
	seedApprovalActivityPrincipals(t, ctx, db)

	service := New(db, func(_ context.Context, reviewerID string, _ *Policy, _ *Request) (bool, error) {
		return reviewerID == "approval-reviewer", nil
	})
	requestID := approveTestRequest(t, ctx, service, "execute-rejected")
	if _, err := db.Pool.Exec(ctx, `UPDATE approval_requests SET status='rejected' WHERE id=$1`, requestID); err != nil {
		t.Fatalf("force rejected: %v", err)
	}
	if _, err := service.MarkExecuted(ctx, requestID); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("MarkExecuted on a rejected request = %v, want ErrAlreadyDecided", err)
	}
	if _, err := service.MarkExecuted(ctx, "missing-request"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkExecuted on a missing request = %v, want ErrNotFound", err)
	}
}

// TestApprovedOutboxIsHeldBackFromTheRecoveryWorker documents why the flake is
// rare rather than constant: the recovery sweep is for actions the deciding
// request never finished, so a freshly approved row is not yet due.
func TestApprovedOutboxIsHeldBackFromTheRecoveryWorker(t *testing.T) {
	db := newApprovalActivityTestDB(t)
	ctx := approvalActivityTestContext(t)
	seedApprovalActivityPrincipals(t, ctx, db)

	service := New(db, func(_ context.Context, reviewerID string, _ *Policy, _ *Request) (bool, error) {
		return reviewerID == "approval-reviewer", nil
	})
	requestID := approveTestRequest(t, ctx, service, "execute-grace")

	due, err := service.PendingExecutions(ctx, 10)
	if err != nil {
		t.Fatalf("PendingExecutions: %v", err)
	}
	for _, request := range due {
		if request.ID == requestID {
			t.Fatal("a just-approved request is already due for the recovery worker")
		}
	}

	if _, err := db.Pool.Exec(ctx, `UPDATE workflow_outbox SET available_at=1 WHERE request_id=$1`, requestID); err != nil {
		t.Fatalf("age the outbox row: %v", err)
	}
	due, err = service.PendingExecutions(ctx, 10)
	if err != nil {
		t.Fatalf("PendingExecutions after ageing: %v", err)
	}
	found := false
	for _, request := range due {
		if request.ID == requestID {
			found = true
		}
	}
	if !found {
		t.Fatal("a stranded approved request never becomes due for recovery")
	}
}

// approveTestRequest drives a request all the way to approved so the execution
// transition can be exercised on its own.
func approveTestRequest(t *testing.T, ctx context.Context, service *Service, key string) string {
	t.Helper()
	if _, err := service.UpsertPolicy(ctx, Policy{
		ScopeType: "team", ScopeID: "approval-team", ActionType: "mcp.create_post", Enabled: true,
		ReviewerPermission: "review_approval", ApprovalsRequired: 1, ForbidSelfApproval: true,
		ExpiresAfterSeconds: 3_600, Config: json.RawMessage(`{"reviewer_roles":["system_admin"]}`),
	}, "approval-reviewer"); err != nil {
		t.Fatalf("upsert approval policy: %v", err)
	}
	result, err := service.Submit(ctx, Submission{
		ActionType: "mcp.create_post", RequesterID: "approval-requester", TeamID: "approval-team",
		ResourceType: "channel", ResourceID: "approval-channel", IdempotencyKey: key,
		Payload: map[string]any{"channel_id": "approval-channel", "message": "보호된 작업"},
	})
	if err != nil || result == nil || result.Request == nil {
		t.Fatalf("submit approval request: %#v, %v", result, err)
	}
	approved, err := service.Decide(ctx, result.Request.ID, "approval-reviewer", "approve", "release regression")
	if err != nil || approved == nil || approved.Status != "approved" {
		t.Fatalf("decide = %#v, %v", approved, err)
	}
	return result.Request.ID
}
