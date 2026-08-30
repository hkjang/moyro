package workitems

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const workItemsTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

func TestWorkItemsPostgresSourceScopeIdempotencyAndOwnership(t *testing.T) {
	db := newWorkItemsTestDB(t)
	ctx := workItemsTestContext(t)
	seedWorkItemsFixture(t, ctx, db)
	service := New(db)

	taskInput := CreateInput{
		Kind: KindTask, Title: "배포 확인", Description: "체크리스트를 완료합니다.",
		AssigneeID: "work-user-b", SourcePostID: "work-reply", DueAt: time.Now().Add(time.Hour).UnixMilli(),
		IdempotencyKey: "task-request-1",
	}
	task, replayed, err := service.Create(ctx, "work-user-a", taskInput)
	if err != nil || replayed {
		t.Fatalf("create task = %#v, replayed=%v, err=%v", task, replayed, err)
	}
	if task.ChannelID != "work-channel" || task.TeamID != "work-team" || task.SourcePostID != "work-reply" || task.SourceThreadID != "work-root" || task.AssigneeID != "work-user-b" || task.Status != StatusOpen {
		t.Fatalf("derived task scope = %#v", task)
	}
	replayedTask, replayed, err := service.Create(ctx, "work-user-a", taskInput)
	if err != nil || !replayed || replayedTask.ID != task.ID {
		t.Fatalf("replay task = %#v, replayed=%v, err=%v", replayedTask, replayed, err)
	}
	changedTitle := taskInput
	changedTitle.Title = "같은 키의 다른 제목"
	if _, _, err := service.Create(ctx, "work-user-a", changedTitle); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed idempotent request error = %v", err)
	}
	conflicting := taskInput
	conflicting.Kind = KindDecision
	conflicting.AssigneeID = ""
	conflicting.DueAt = 0
	if _, _, err := service.Create(ctx, "work-user-a", conflicting); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency collision error = %v", err)
	}
	invalidAssignee := taskInput
	invalidAssignee.IdempotencyKey = "task-request-2"
	invalidAssignee.AssigneeID = "work-user-c"
	if _, _, err := service.Create(ctx, "work-user-a", invalidAssignee); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-member assignee error = %v", err)
	}
	if _, _, err := service.Create(ctx, "work-user-c", CreateInput{Kind: KindTask, Title: "숨김", SourcePostID: "work-root", IdempotencyKey: "hidden-1"}); !errors.Is(err, ErrSourceNotAccessible) {
		t.Fatalf("non-member source error = %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE users SET delete_at=10 WHERE id='work-user-c'`); err != nil {
		t.Fatalf("deactivate non-member user: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO channel_members (channel_id,user_id,roles,create_at) VALUES ('work-channel','work-user-c','channel_user',1)`); err != nil {
		t.Fatalf("seed inactive channel member: %v", err)
	}
	inactiveAssignee := taskInput
	inactiveAssignee.IdempotencyKey = "task-request-inactive"
	inactiveAssignee.AssigneeID = "work-user-c"
	if _, _, err := service.Create(ctx, "work-user-a", inactiveAssignee); !errors.Is(err, ErrForbidden) {
		t.Fatalf("inactive assignee error = %v", err)
	}
	if _, _, err := service.Create(ctx, "work-user-c", CreateInput{
		Kind: KindTask, Title: "비활성 사용자의 작업", SourcePostID: "work-root", IdempotencyKey: "inactive-actor",
	}); !errors.Is(err, ErrSourceNotAccessible) {
		t.Fatalf("inactive actor source error = %v", err)
	}

	decision, replayed, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindDecision, Title: "금요일에 배포", SourcePostID: "work-root", IdempotencyKey: "decision-request-1",
	})
	if err != nil || replayed || decision.Status != StatusRecorded || decision.DecidedAt == 0 {
		t.Fatalf("create decision = %#v, replayed=%v, err=%v", decision, replayed, err)
	}
	firstPage, err := service.ListForUser(ctx, "work-user-b", ListOptions{PageSize: 1})
	if err != nil || len(firstPage.Items) != 1 || firstPage.NextCursor == "" {
		t.Fatalf("first cursor page = %#v, %v", firstPage, err)
	}
	secondPage, err := service.ListForUser(ctx, "work-user-b", ListOptions{PageSize: 1, Cursor: firstPage.NextCursor})
	if err != nil || len(secondPage.Items) != 1 || secondPage.Items[0].ID == firstPage.Items[0].ID || secondPage.NextCursor != "" {
		t.Fatalf("second cursor page = %#v, %v", secondPage, err)
	}
	if _, err := service.ListForUser(ctx, "work-user-b", ListOptions{Cursor: "not-a-cursor"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid cursor error = %v", err)
	}

	assignedPage, err := service.ListForUser(ctx, "work-user-b", ListOptions{Kind: KindTask})
	if err != nil || len(assignedPage.Items) != 1 || assignedPage.Items[0].ID != task.ID {
		t.Fatalf("assignee task page = %#v, %v", assignedPage, err)
	}
	decisionPage, err := service.ListForUser(ctx, "work-user-b", ListOptions{Kind: KindDecision})
	if err != nil || len(decisionPage.Items) != 1 || decisionPage.Items[0].ID != decision.ID {
		t.Fatalf("channel decision page = %#v, %v", decisionPage, err)
	}
	done := StatusDone
	if page, err := service.ListForUser(ctx, "work-user-d", ListOptions{Kind: KindTask}); err != nil || len(page.Items) != 0 {
		t.Fatalf("unrelated member task page = %#v, %v", page, err)
	}
	if _, err := service.Patch(ctx, "work-user-d", task.ID, PatchInput{Status: &done}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unrelated member task patch error = %v", err)
	}
	superseded := StatusSuperseded
	if _, err := service.Patch(ctx, "work-user-d", decision.ID, PatchInput{Status: &superseded}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("channel-visible decision patch error = %v", err)
	}
	updated, err := service.Patch(ctx, "work-user-b", task.ID, PatchInput{Status: &done})
	if err != nil || updated.Status != StatusDone {
		t.Fatalf("assignee completes task = %#v, %v", updated, err)
	}
	forbiddenTitle := "권한 없는 제목 변경"
	if _, err := service.Patch(ctx, "work-user-b", task.ID, PatchInput{Title: &forbiddenTitle}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("assignee title patch error = %v", err)
	}
	inactiveID := "work-user-c"
	if _, err := service.Patch(ctx, "work-user-a", task.ID, PatchInput{AssigneeID: &inactiveID}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("inactive reassignment error = %v", err)
	}
	if _, err := service.Delete(ctx, "work-user-b", task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("assignee delete error = %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE users SET delete_at=0 WHERE id='work-user-c'`); err != nil {
		t.Fatalf("reactivate replacement assignee: %v", err)
	}
	reassigned, err := service.Patch(ctx, "work-user-a", task.ID, PatchInput{AssigneeID: &inactiveID})
	if err != nil || reassigned.AssigneeID != "work-user-c" || reassigned.PreviousAssigneeID != "work-user-b" || !reassigned.AssigneeChanged {
		t.Fatalf("reassigned task = %#v, %v", reassigned, err)
	}

	if _, err := db.Pool.Exec(ctx, `DELETE FROM channel_members WHERE channel_id='work-channel' AND user_id='work-user-b'`); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	pageAfterRemoval, err := service.ListForUser(ctx, "work-user-b", ListOptions{})
	if err != nil || len(pageAfterRemoval.Items) != 0 {
		t.Fatalf("removed member page = %#v, %v", pageAfterRemoval, err)
	}
	open := StatusOpen
	if _, err := service.Patch(ctx, "work-user-b", task.ID, PatchInput{Status: &open}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed member patch error = %v", err)
	}
	deleted, err := service.Delete(ctx, "work-user-a", task.ID)
	if err != nil || deleted == nil || deleted.DeleteAt == 0 || deleted.AssigneeID != "work-user-c" {
		t.Fatalf("owner delete: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO channel_members (channel_id,user_id,roles,create_at) VALUES ('work-channel','work-user-b','channel_user',1)`); err != nil {
		t.Fatalf("restore assignee membership: %v", err)
	}
	if _, _, err := service.Create(ctx, "work-user-a", taskInput); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("deleted idempotent request error = %v", err)
	}
}

func TestWorkItemsPostgresCredentialScopeIntersection(t *testing.T) {
	db := newWorkItemsTestDB(t)
	ctx := workItemsTestContext(t)
	seedWorkItemsFixture(t, ctx, db)
	service := New(db)
	principal := rbac.Principal{
		UserID:            "work-user-a",
		CredentialID:      "scoped-key",
		Restricted:        true,
		AllowedTeamIDs:    map[string]struct{}{"work-team": {}},
		AllowedChannelIDs: map[string]struct{}{"work-channel": {}},
	}

	allowed, replayed, err := service.CreateForPrincipal(ctx, principal, CreateInput{
		Kind: KindTask, Title: "허용 작업", SourcePostID: "work-root", IdempotencyKey: "scope-allowed",
	})
	if err != nil || replayed {
		t.Fatalf("create in-scope work item = %#v, replayed=%v, err=%v", allowed, replayed, err)
	}
	denied, replayed, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindTask, Title: "다른 채널 작업", SourcePostID: "other-work-post", IdempotencyKey: "scope-existing-denied",
	})
	if err != nil || replayed {
		t.Fatalf("seed out-of-scope work item = %#v, replayed=%v, err=%v", denied, replayed, err)
	}
	if _, _, err := service.CreateForPrincipal(ctx, principal, CreateInput{
		Kind: KindTask, Title: "차단 작업", SourcePostID: "other-work-post", IdempotencyKey: "scope-denied-create",
	}); !errors.Is(err, ErrSourceNotAccessible) {
		t.Fatalf("out-of-scope create error = %v", err)
	}

	unrestricted, err := service.ListForUser(ctx, "work-user-a", ListOptions{Kind: KindTask})
	if err != nil || len(unrestricted.Items) != 2 {
		t.Fatalf("unrestricted work-item list = %#v, %v", unrestricted, err)
	}
	page, err := service.ListForPrincipal(ctx, principal, ListOptions{Kind: KindTask})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != allowed.ID {
		t.Fatalf("credential-scoped work-item list = %#v, %v", page, err)
	}

	done := StatusDone
	if _, err := service.PatchForPrincipal(ctx, principal, denied.ID, PatchInput{Status: &done}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("out-of-scope patch error = %v", err)
	}
	if _, err := service.DeleteForPrincipal(ctx, principal, denied.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("out-of-scope delete error = %v", err)
	}
	updated, err := service.PatchForPrincipal(ctx, principal, allowed.ID, PatchInput{Status: &done})
	if err != nil || updated.Status != StatusDone {
		t.Fatalf("in-scope patch = %#v, %v", updated, err)
	}
	deleted, err := service.DeleteForPrincipal(ctx, principal, allowed.ID)
	if err != nil || deleted.DeleteAt == 0 {
		t.Fatalf("in-scope delete = %#v, %v", deleted, err)
	}

	var deniedDeleteAt, blockedCreateCount int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT
			(SELECT delete_at FROM work_items WHERE id=$1),
			(SELECT count(*) FROM work_items WHERE idempotency_key='scope-denied-create')
	`, denied.ID).Scan(&deniedDeleteAt, &blockedCreateCount); err != nil {
		t.Fatalf("read out-of-scope work-item state: %v", err)
	}
	if deniedDeleteAt != 0 || blockedCreateCount != 0 {
		t.Fatalf("out-of-scope mutation escaped: delete_at=%d blocked_create_count=%d", deniedDeleteAt, blockedCreateCount)
	}
}

func TestWorkItemsPostgresArchivedChannelBlocksPatchAndDelete(t *testing.T) {
	db := newWorkItemsTestDB(t)
	ctx := workItemsTestContext(t)
	seedWorkItemsFixture(t, ctx, db)
	service := New(db)

	item, replayed, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindTask, Title: "보관 전 작업", SourcePostID: "work-root", IdempotencyKey: "archive-channel-task",
	})
	if err != nil || replayed {
		t.Fatalf("create work item before archive = %#v, replayed=%v, err=%v", item, replayed, err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE channels SET delete_at=10, update_at=10 WHERE id='work-channel'`); err != nil {
		t.Fatalf("archive source channel: %v", err)
	}

	done := StatusDone
	if _, err := service.Patch(ctx, "work-user-a", item.ID, PatchInput{Status: &done}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("patch archived-channel work item error = %v", err)
	}
	if _, err := service.Delete(ctx, "work-user-a", item.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete archived-channel work item error = %v", err)
	}

	var status string
	var deleteAt int64
	if err := db.Pool.QueryRow(ctx, `SELECT status, delete_at FROM work_items WHERE id=$1`, item.ID).Scan(&status, &deleteAt); err != nil {
		t.Fatalf("read blocked archived-channel work item: %v", err)
	}
	if status != StatusOpen || deleteAt != 0 {
		t.Fatalf("archived-channel mutation escaped: status=%q delete_at=%d", status, deleteAt)
	}
}

func newWorkItemsTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(workItemsTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", workItemsTestPostgresDSN)
	}
	ctx := workItemsTestContext(t)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open work-item test admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping work-item test PostgreSQL: %v", err)
	}
	schemaName := "moyro_work_items_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create work-item test schema: %v", err)
	}
	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop work-item test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse work-item test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 4
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated work-item test pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated work-item test pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate work-item test schema: %v", err)
	}
	return db
}

func seedWorkItemsFixture(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES
			('work-user-a','work-user-a','work-a@example.test','hash','system_user',1,1),
			('work-user-b','work-user-b','work-b@example.test','hash','system_user',1,1),
			('work-user-c','work-user-c','work-c@example.test','hash','system_user',1,1),
			('work-user-d','work-user-d','work-d@example.test','hash','system_user',1,1);
		INSERT INTO teams (id, display_name, name, type, create_at, update_at)
		VALUES
			('work-team','Work Team','work-team','O',1,1),
			('other-work-team','Other Work Team','other-work-team','O',1,1);
		INSERT INTO team_members (team_id,user_id,roles,create_at)
		VALUES
			('work-team','work-user-a','team_user',1),
			('work-team','work-user-b','team_user',1),
			('work-team','work-user-d','team_user',1),
			('other-work-team','work-user-a','team_user',1),
			('other-work-team','work-user-b','team_user',1),
			('other-work-team','work-user-d','team_user',1);
		INSERT INTO channels (id,team_id,type,display_name,name,create_at,update_at)
		VALUES
			('work-channel','work-team','O','Work Channel','work-channel',1,1),
			('other-work-channel','other-work-team','O','Other Work Channel','other-work-channel',1,1);
		INSERT INTO channel_members (channel_id,user_id,roles,create_at)
		VALUES
			('work-channel','work-user-a','channel_user',1),
			('work-channel','work-user-b','channel_user',1),
			('work-channel','work-user-d','channel_user',1),
			('other-work-channel','work-user-a','channel_user',1),
			('other-work-channel','work-user-b','channel_user',1),
			('other-work-channel','work-user-d','channel_user',1);
		INSERT INTO posts (id,channel_id,user_id,root_id,message,create_at,update_at)
		VALUES
			('work-root','work-channel','work-user-a','','root',1,1),
			('work-reply','work-channel','work-user-b','work-root','reply',2,2),
			('other-work-post','other-work-channel','work-user-a','','other root',3,3)
	`); err != nil {
		t.Fatalf("seed work-item fixture: %v", err)
	}
}

func workItemsTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	return ctx
}
