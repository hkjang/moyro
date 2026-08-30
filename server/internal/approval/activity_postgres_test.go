package approval

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/activityevents"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const approvalActivityTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

func TestApprovalActivityPersistsChannelAndRevalidatesCanonicalReviewer(t *testing.T) {
	db := newApprovalActivityTestDB(t)
	ctx := approvalActivityTestContext(t)
	seedApprovalActivityPrincipals(t, ctx, db)

	var reviewerAllowed atomic.Bool
	reviewerAllowed.Store(true)
	service := New(db, func(_ context.Context, reviewerID string, _ *Policy, _ *Request) (bool, error) {
		return reviewerAllowed.Load() && reviewerID == "approval-reviewer", nil
	})
	activityService := activityevents.New(db)
	activityService.SetApprovalReviewAuthorizer(service.CanReviewRequest)
	service.SetActivityEmitter(activityService)

	policy, err := service.UpsertPolicy(ctx, Policy{
		ScopeType: "team", ScopeID: "approval-team", ActionType: "mcp.create_post", Enabled: true,
		ReviewerPermission: "review_approval", ApprovalsRequired: 1, ForbidSelfApproval: true,
		ExpiresAfterSeconds: 3_600, Config: json.RawMessage(`{"reviewer_roles":["system_admin"]}`),
	}, "approval-reviewer")
	if err != nil {
		t.Fatalf("upsert approval policy: %v", err)
	}
	if policy.ID == "" {
		t.Fatal("approval policy id is empty")
	}

	result, err := service.Submit(ctx, Submission{
		ActionType: "mcp.create_post", RequesterID: "approval-requester", TeamID: "approval-team",
		ResourceType: "channel", ResourceID: "approval-channel",
		Payload: map[string]any{"channel_id": "approval-channel", "message": "운영 공지"},
	})
	if err != nil || result == nil || result.Request == nil {
		t.Fatalf("submit channel approval = %#v, %v", result, err)
	}
	requestID := result.Request.ID

	reviewerPage, err := activityService.List(ctx, "approval-reviewer", activityevents.ListOptions{})
	if err != nil || len(reviewerPage.Events) != 1 {
		t.Fatalf("reviewer activity = %#v, %v", reviewerPage, err)
	}
	reviewEvent := reviewerPage.Events[0]
	if reviewEvent.ResourceType != "approval_review" || reviewEvent.ResourceID != requestID ||
		reviewEvent.TeamID != "approval-team" || reviewEvent.ChannelID != "approval-channel" {
		t.Fatalf("review activity scope = %#v", reviewEvent)
	}
	requesterPage, err := activityService.List(ctx, "approval-requester", activityevents.ListOptions{})
	if err != nil || len(requesterPage.Events) != 1 || requesterPage.Events[0].ChannelID != "approval-channel" {
		t.Fatalf("requester submitted activity = %#v, %v", requesterPage, err)
	}

	if allowed, err := service.CanReviewRequest(ctx, "approval-reviewer", requestID); err != nil || !allowed {
		t.Fatalf("canonical reviewer before revocation = %v, %v", allowed, err)
	}
	if _, err := service.Decide(ctx, requestID, "approval-reviewer", "approve", ""); err != nil {
		t.Fatalf("decide approval: %v", err)
	}
	requesterPage, err = activityService.List(ctx, "approval-requester", activityevents.ListOptions{})
	if err != nil || len(requesterPage.Events) != 2 {
		t.Fatalf("requester decided activity = %#v, %v", requesterPage, err)
	}
	for _, event := range requesterPage.Events {
		if event.TeamID != "approval-team" || event.ChannelID != "approval-channel" {
			t.Fatalf("requester activity lost channel scope: %#v", event)
		}
	}

	reviewerAllowed.Store(false)
	if allowed, err := service.CanReviewRequest(ctx, "approval-reviewer", requestID); err != nil || allowed {
		t.Fatalf("canonical reviewer after revocation = %v, %v", allowed, err)
	}
	reviewerPage, err = activityService.List(ctx, "approval-reviewer", activityevents.ListOptions{})
	if err != nil || len(reviewerPage.Events) != 0 {
		t.Fatalf("revoked reviewer activity = %#v, %v", reviewerPage, err)
	}
	read := true
	if _, err := activityService.UpdateState(ctx, "approval-reviewer", reviewEvent.ID, activityevents.StatePatch{Read: &read}); !errors.Is(err, activityevents.ErrNotFound) {
		t.Fatalf("revoked reviewer update error = %v", err)
	}
	count, err := activityService.MarkRead(ctx, "approval-reviewer", []string{reviewEvent.ID})
	if err != nil || count != 0 {
		t.Fatalf("revoked reviewer mark-read = %d, %v", count, err)
	}
	var readAt int64
	if err := db.Pool.QueryRow(ctx, `SELECT read_at FROM activity_events WHERE id=$1`, reviewEvent.ID).Scan(&readAt); err != nil {
		t.Fatalf("read review event state: %v", err)
	}
	if readAt != 0 {
		t.Fatalf("revoked reviewer event mutated: read_at=%d", readAt)
	}
}

func newApprovalActivityTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(approvalActivityTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", approvalActivityTestPostgresDSN)
	}
	ctx := approvalActivityTestContext(t)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open approval activity admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping approval activity PostgreSQL: %v", err)
	}

	schemaName := "moyro_approval_activity_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create approval activity schema: %v", err)
	}

	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop approval activity schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse approval activity DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 6
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated approval activity pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated approval activity pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate approval activity schema: %v", err)
	}
	return db
}

func seedApprovalActivityPrincipals(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES
			('approval-requester','approval-requester','approval-requester@example.test','hash','system_user',1,1),
			('approval-reviewer','approval-reviewer','approval-reviewer@example.test','hash','system_admin',1,1);
		INSERT INTO teams (id,name,display_name,type,create_at,update_at)
		VALUES ('approval-team','approval-team','Approval Team','O',1,1);
		INSERT INTO team_members (team_id,user_id,roles,create_at)
		VALUES
			('approval-team','approval-requester','team_user',1),
			('approval-team','approval-reviewer','team_user',1);
		INSERT INTO channels (id,team_id,type,display_name,name,create_at,update_at)
		VALUES ('approval-channel','approval-team','O','Approval Channel','approval-channel',1,1);
		INSERT INTO channel_members (channel_id,user_id,roles,create_at)
		VALUES
			('approval-channel','approval-requester','channel_user',1),
			('approval-channel','approval-reviewer','channel_user',1)
	`); err != nil {
		t.Fatalf("seed approval activity principals: %v", err)
	}
}

func approvalActivityTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	return ctx
}
