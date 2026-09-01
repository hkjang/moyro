package automations

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/hkjang/moyro/server/internal/workitems"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const automationTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

func TestAutomationPostgresEnqueueUpdatePreservesHistoryAndInactiveRuleFailsClosed(t *testing.T) {
	db := newAutomationTestDB(t)
	ctx := automationTestContext(t)
	seedAutomationFixture(t, ctx, db)
	service := New(db)
	postService := posts.New(db)
	postService.SetTransactionalEnqueuer(service)

	rule, err := service.CreateForPrincipal(ctx, rbac.UserPrincipal("automation-user"), SaveInput{
		Name: "TODO reminder", ChannelID: "automation-channel", Enabled: true,
		MatchType: MatchStartsWith, MatchValue: "todo:",
		Actions: []Action{{ID: "old-action", Type: ActionReminder, Config: ActionConfig{RemindOffsetMinutes: 5}}},
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}
	post, err := postService.Create(ctx, "automation-channel", "automation-author", "", "TODO: 배포 확인", nil, nil)
	if err != nil {
		t.Fatalf("create triggering post: %v", err)
	}
	var originalRunID, originalActionID string
	var originalConfig []byte
	if err := db.Pool.QueryRow(ctx, `
		SELECT id, action_id, action_config FROM automation_runs WHERE post_id=$1
	`, post.ID).Scan(&originalRunID, &originalActionID, &originalConfig); err != nil {
		t.Fatalf("read durable run: %v", err)
	}
	if originalActionID != "old-action" || !strings.Contains(string(originalConfig), "remind_offset_minutes") {
		t.Fatalf("run snapshot action=%q config=%s", originalActionID, originalConfig)
	}

	rule, err = service.UpdateForPrincipal(ctx, rbac.UserPrincipal("automation-user"), rule.ID, SaveInput{
		Name: "updated TODO reminder", ChannelID: "automation-channel", Enabled: true,
		MatchType: MatchContains, MatchValue: "배포",
		Revision: rule.Revision,
		Actions:  []Action{{ID: "new-action", Type: ActionReminder, Config: ActionConfig{RemindOffsetMinutes: 10}}},
	})
	if err != nil {
		t.Fatalf("update rule: %v", err)
	}
	var runCount int
	var preservedActionID string
	if err := db.Pool.QueryRow(ctx, `
		SELECT count(*), min(action_id) FROM automation_runs WHERE rule_id=$1
	`, rule.ID).Scan(&runCount, &preservedActionID); err != nil {
		t.Fatalf("read run after action replacement: %v", err)
	}
	if runCount != 1 || preservedActionID != "old-action" {
		t.Fatalf("rule update deleted or rewrote history: count=%d action=%q", runCount, preservedActionID)
	}
	runs, err := service.ListRunsForPrincipal(ctx, rbac.UserPrincipal("automation-user"), rule.ID, 30)
	if err != nil || len(runs) != 1 || runs[0].ID != originalRunID || runs[0].ActionConfig != nil {
		t.Fatalf("public run history = %#v, %v", runs, err)
	}

	claimed, err := service.ClaimDue(ctx, time.Now().Add(time.Second).UnixMilli(), 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim run = %#v, %v", claimed, err)
	}
	rule, err = service.UpdateForPrincipal(ctx, rbac.UserPrincipal("automation-user"), rule.ID, SaveInput{
		Name: rule.Name, ChannelID: rule.ChannelID, Enabled: false,
		MatchType: rule.MatchType, MatchValue: rule.MatchValue, Revision: rule.Revision, Actions: rule.Actions,
	})
	if err != nil {
		t.Fatalf("disable rule: %v", err)
	}
	worker := NewWorker(service, postService, workitems.New(db), nil, nil)
	if _, _, _, err := worker.executeAction(ctx, &claimed[0]); !errors.Is(err, ErrRuleInactive) {
		t.Fatalf("disabled processing run error = %v", err)
	}
}

func TestAutomationPostgresPostAndRunCommitOrRollbackTogether(t *testing.T) {
	db := newAutomationTestDB(t)
	ctx := automationTestContext(t)
	seedAutomationFixture(t, ctx, db)
	postService := posts.New(db)
	postService.SetTransactionalEnqueuer(failingEnqueuer{})
	if _, err := postService.Create(ctx, "automation-channel", "automation-author", "", "must roll back", nil, nil); err == nil {
		t.Fatal("post creation unexpectedly succeeded")
	}
	var postCount int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM posts WHERE message='must roll back'`).Scan(&postCount); err != nil {
		t.Fatalf("count rolled-back post: %v", err)
	}
	if postCount != 0 {
		t.Fatalf("post survived failed transactional enqueue: count=%d", postCount)
	}
}

func TestAutomationPostgresDeleteCancelsQueuedRunsAtomically(t *testing.T) {
	db := newAutomationTestDB(t)
	ctx := automationTestContext(t)
	seedAutomationFixture(t, ctx, db)
	service := New(db)
	postService := posts.New(db)
	postService.SetTransactionalEnqueuer(service)
	rule, err := service.CreateForPrincipal(ctx, rbac.UserPrincipal("automation-user"), SaveInput{
		Name: "task rule", ChannelID: "automation-channel", Enabled: true,
		MatchType: MatchContains, MatchValue: "action",
		Actions: []Action{{Type: ActionTask, Config: ActionConfig{Priority: workitems.PriorityNormal}}},
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}
	if _, err := postService.Create(ctx, "automation-channel", "automation-author", "", "action now", nil, nil); err != nil {
		t.Fatalf("create triggering post: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM channel_members WHERE channel_id='automation-channel' AND user_id='automation-user'`); err != nil {
		t.Fatalf("revoke rule owner membership: %v", err)
	}
	if _, err := postService.Create(ctx, "automation-channel", "automation-author", "", "action after revoke", nil, nil); err != nil {
		t.Fatalf("create post after owner revoke: %v", err)
	}
	var queuedCount int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM automation_runs WHERE rule_id=$1`, rule.ID).Scan(&queuedCount); err != nil {
		t.Fatalf("count runs after membership revoke: %v", err)
	}
	if queuedCount != 1 {
		t.Fatalf("revoked rule owner still enqueued runs: count=%d", queuedCount)
	}
	if err := service.DeleteForPrincipal(ctx, rbac.UserPrincipal("automation-user"), rule.ID); err != nil {
		t.Fatalf("delete rule: %v", err)
	}
	var deleteAt int64
	var enabled bool
	var status string
	if err := db.Pool.QueryRow(ctx, `
		SELECT rule.delete_at, rule.enabled, run.status
		FROM automation_rules rule JOIN automation_runs run ON run.rule_id=rule.id
		WHERE rule.id=$1
	`, rule.ID).Scan(&deleteAt, &enabled, &status); err != nil {
		t.Fatalf("read deleted rule and run: %v", err)
	}
	if deleteAt == 0 || enabled || status != StatusCancelled {
		t.Fatalf("delete state delete_at=%d enabled=%v run=%q", deleteAt, enabled, status)
	}
}

func TestAutomationPostgresCredentialScopeCannotMoveOrReadHiddenRule(t *testing.T) {
	db := newAutomationTestDB(t)
	ctx := automationTestContext(t)
	seedAutomationFixture(t, ctx, db)
	service := New(db)
	postService := posts.New(db)
	postService.SetTransactionalEnqueuer(service)
	action := Action{Type: ActionReminder, Config: ActionConfig{RemindOffsetMinutes: 5}}
	allowed, err := service.CreateForPrincipal(ctx, rbac.UserPrincipal("automation-user"), SaveInput{
		Name: "allowed", ChannelID: "automation-channel", Enabled: true,
		MatchType: MatchContains, MatchValue: "run", Actions: []Action{action},
	})
	if err != nil {
		t.Fatalf("create allowed rule: %v", err)
	}
	hidden, err := service.CreateForPrincipal(ctx, rbac.UserPrincipal("automation-user"), SaveInput{
		Name: "hidden", ChannelID: "other-automation-channel", Enabled: true,
		MatchType: MatchContains, MatchValue: "run", Actions: []Action{action},
	})
	if err != nil {
		t.Fatalf("create hidden rule: %v", err)
	}
	if _, err := postService.Create(ctx, "other-automation-channel", "automation-author", "", "run hidden", nil, nil); err != nil {
		t.Fatalf("trigger hidden rule: %v", err)
	}
	principal := rbac.Principal{
		UserID: "automation-user", Restricted: true,
		AllowedTeamIDs:    map[string]struct{}{"automation-team": {}},
		AllowedChannelIDs: map[string]struct{}{"automation-channel": {}},
	}
	rules, err := service.ListForPrincipal(ctx, principal)
	if err != nil || len(rules) != 1 || rules[0].ID != allowed.ID {
		t.Fatalf("scoped rules = %#v, %v", rules, err)
	}
	if _, err := service.UpdateForPrincipal(ctx, principal, hidden.ID, SaveInput{
		Name: "move hidden", ChannelID: "automation-channel", Enabled: true,
		MatchType: MatchContains, MatchValue: "run", Revision: hidden.Revision, Actions: []Action{action},
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("move hidden rule error = %v", err)
	}
	if _, err := service.ListRunsForPrincipal(ctx, principal, hidden.ID, 30); !errors.Is(err, ErrNotFound) {
		t.Fatalf("hidden run history error = %v", err)
	}
	if err := service.DeleteForPrincipal(ctx, principal, hidden.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete hidden rule error = %v", err)
	}
}

func TestAutomationPostgresExpiredGuestCannotConfigureOrExecuteActions(t *testing.T) {
	db := newAutomationTestDB(t)
	ctx := automationTestContext(t)
	seedAutomationFixture(t, ctx, db)
	service := New(db)
	if _, err := service.CreateForPrincipal(ctx, rbac.UserPrincipal("automation-expired-guest"), SaveInput{
		Name: "expired owner", ChannelID: "automation-channel", Enabled: true,
		MatchType: MatchContains, MatchValue: "run",
		Actions: []Action{{Type: ActionReminder, Config: ActionConfig{RemindOffsetMinutes: 5}}},
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expired rule owner error = %v", err)
	}
	for name, action := range map[string]Action{
		"assignee": {Type: ActionTask, Config: ActionConfig{AssigneeID: "automation-expired-guest", Priority: workitems.PriorityNormal}},
		"reviewer": {Type: ActionDecision, Config: ActionConfig{ReviewerID: "automation-expired-guest", InitialStatus: workitems.StatusProposed}},
	} {
		if _, err := service.CreateForPrincipal(ctx, rbac.UserPrincipal("automation-user"), SaveInput{
			Name: name, ChannelID: "automation-channel", Enabled: true,
			MatchType: MatchContains, MatchValue: name, Actions: []Action{action},
		}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("expired guest %s error = %v", name, err)
		}
	}

	rule, err := service.CreateForPrincipal(ctx, rbac.UserPrincipal("automation-user"), SaveInput{
		Name: "queued before expiry", ChannelID: "automation-channel", Enabled: true,
		MatchType: MatchContains, MatchValue: "queued",
		Actions: []Action{{Type: ActionReminder, Config: ActionConfig{RemindOffsetMinutes: 5}}},
	})
	if err != nil {
		t.Fatalf("create pre-expiry rule: %v", err)
	}
	postService := posts.New(db)
	postService.SetTransactionalEnqueuer(service)
	if _, err := postService.Create(ctx, "automation-channel", "automation-author", "", "queued run", nil, nil); err != nil {
		t.Fatalf("enqueue before expiry: %v", err)
	}
	claimed, err := service.ClaimDue(ctx, time.Now().Add(time.Second).UnixMilli(), 1)
	if err != nil || len(claimed) != 1 || claimed[0].RuleID != rule.ID {
		t.Fatalf("claim pre-expiry run = %#v, %v", claimed, err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE users SET roles='system_guest', guest_expires_at=1 WHERE id='automation-user'`); err != nil {
		t.Fatalf("expire automation owner: %v", err)
	}
	if _, err := postService.Create(ctx, "automation-channel", "automation-author", "", "queued after expiry", nil, nil); err != nil {
		t.Fatalf("post after owner expiry: %v", err)
	}
	var runCount int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM automation_runs WHERE rule_id=$1`, rule.ID).Scan(&runCount); err != nil {
		t.Fatalf("count runs after owner expiry: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("expired owner enqueued another run: count=%d", runCount)
	}
	worker := NewWorker(service, postService, workitems.New(db), nil, nil)
	if _, _, _, err := worker.executeAction(ctx, &claimed[0]); !errors.Is(err, ErrNoLongerAuthorized) {
		t.Fatalf("expired queued run error = %v", err)
	}
}

type failingEnqueuer struct{}

func (failingEnqueuer) EnqueuePost(context.Context, pgx.Tx, *posts.Post) error {
	return errors.New("injected enqueue failure")
}

func newAutomationTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(automationTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", automationTestPostgresDSN)
	}
	ctx := automationTestContext(t)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open automation test admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping automation test PostgreSQL: %v", err)
	}
	schemaName := "moyro_automation_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create automation test schema: %v", err)
	}
	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop automation test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse automation test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 4
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated automation test pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated automation test pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate automation test schema: %v", err)
	}
	return db
}

func seedAutomationFixture(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES
			('automation-user','automation-user','automation@example.test','hash','system_user',1,1),
			('automation-author','automation-author','author@example.test','hash','system_user',1,1),
			('automation-expired-guest','automation-expired-guest','expired@example.test','hash','system_guest',1,1);
		UPDATE users SET guest_expires_at=1 WHERE id='automation-expired-guest';
		INSERT INTO teams (id, display_name, name, type, create_at, update_at)
		VALUES
			('automation-team','Automation Team','automation-team','O',1,1),
			('other-automation-team','Other Automation Team','other-automation-team','O',1,1);
		INSERT INTO team_members (team_id,user_id,roles,create_at)
		VALUES
			('automation-team','automation-user','team_user',1),
			('automation-team','automation-author','team_user',1),
			('automation-team','automation-expired-guest','team_user',1),
			('other-automation-team','automation-user','team_user',1),
			('other-automation-team','automation-author','team_user',1);
		INSERT INTO channels (id,team_id,type,display_name,name,create_at,update_at)
		VALUES
			('automation-channel','automation-team','O','Automation','automation',1,1),
			('other-automation-channel','other-automation-team','O','Other Automation','other-automation',1,1);
		INSERT INTO channel_members (channel_id,user_id,roles,create_at)
		VALUES
			('automation-channel','automation-user','channel_user',1),
			('automation-channel','automation-author','channel_user',1),
			('automation-channel','automation-expired-guest','channel_user',1),
			('other-automation-channel','automation-user','channel_user',1),
			('other-automation-channel','automation-author','channel_user',1)
	`); err != nil {
		t.Fatalf("seed automation fixture: %v", err)
	}
}

func automationTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	return ctx
}
