package workitems

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
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
	renamed := "배포 확인 · 수정됨"
	if _, err := service.Patch(ctx, "work-user-a", task.ID, PatchInput{Title: &renamed}); err != nil {
		t.Fatalf("mutate task after create: %v", err)
	}
	mutatedReplay, replayed, err := service.Create(ctx, "work-user-a", taskInput)
	if err != nil || !replayed || mutatedReplay.ID != task.ID || mutatedReplay.Title != renamed {
		t.Fatalf("replay after mutation = %#v, replayed=%v, err=%v", mutatedReplay, replayed, err)
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
	if _, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindDecision, Title: "금요일에 배포", SourcePostID: "work-root",
		IdempotencyKey: "decision-request-1", InitialStatus: StatusProposed,
	}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("decision initial-status idempotency error = %v", err)
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

func TestWorkItemsPostgresDependenciesAndRecurringCompletion(t *testing.T) {
	db := newWorkItemsTestDB(t)
	ctx := workItemsTestContext(t)
	seedWorkItemsFixture(t, ctx, db)
	service := New(db)

	dependency, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindTask, Title: "선행 작업", SourcePostID: "work-root", IdempotencyKey: "dependency",
	})
	if err != nil {
		t.Fatalf("create dependency: %v", err)
	}
	blocked, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindTask, Title: "후속 작업", SourcePostID: "work-root", IdempotencyKey: "dependent",
		DependencyIDs: []string{dependency.ID},
	})
	if err != nil {
		t.Fatalf("create dependent task: %v", err)
	}
	inProgress := StatusInProgress
	if _, err := service.Patch(ctx, "work-user-a", blocked.ID, PatchInput{Status: &inProgress}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("blocked transition error = %v", err)
	}
	if _, err := service.AddLinkForPrincipal(ctx, rbac.UserPrincipal("work-user-a"), dependency.ID, blocked.ID, RelationDependsOn); !errors.Is(err, ErrDependencyCycle) {
		t.Fatalf("dependency cycle error = %v", err)
	}
	otherChannelTask, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindTask, Title: "다른 채널 작업", SourcePostID: "other-work-post", IdempotencyKey: "cross-channel-link-target",
	})
	if err != nil {
		t.Fatalf("create cross-channel target: %v", err)
	}
	if _, err := service.AddLinkForPrincipal(ctx, rbac.UserPrincipal("work-user-a"), blocked.ID, otherChannelTask.ID, RelationDependsOn); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-channel dependency error = %v", err)
	}
	cancelled := StatusCancelled
	if _, err := service.Patch(ctx, "work-user-a", dependency.ID, PatchInput{Status: &cancelled}); err != nil {
		t.Fatalf("cancel dependency: %v", err)
	}
	if _, err := service.Patch(ctx, "work-user-a", blocked.ID, PatchInput{Status: &inProgress}); err != nil {
		t.Fatalf("cancelled dependency must be resolved: %v", err)
	}

	left, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindTask, Title: "동시 A", SourcePostID: "work-root", IdempotencyKey: "concurrent-a",
	})
	if err != nil {
		t.Fatalf("create concurrent left task: %v", err)
	}
	right, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindTask, Title: "동시 B", SourcePostID: "work-root", IdempotencyKey: "concurrent-b",
	})
	if err != nil {
		t.Fatalf("create concurrent right task: %v", err)
	}
	cycleStart := make(chan struct{})
	cycleErrors := make(chan error, 2)
	for _, edge := range [][2]string{{left.ID, right.ID}, {right.ID, left.ID}} {
		edge := edge
		go func() {
			<-cycleStart
			_, linkErr := service.AddLinkForPrincipal(ctx, rbac.UserPrincipal("work-user-a"), edge[0], edge[1], RelationDependsOn)
			cycleErrors <- linkErr
		}()
	}
	close(cycleStart)
	var successes, rejectedCycles int
	for range 2 {
		linkErr := <-cycleErrors
		switch {
		case linkErr == nil:
			successes++
		case errors.Is(linkErr, ErrDependencyCycle):
			rejectedCycles++
		default:
			t.Fatalf("concurrent reverse dependency error = %v", linkErr)
		}
	}
	if successes != 1 || rejectedCycles != 1 {
		t.Fatalf("concurrent reverse dependencies successes=%d cycle_rejections=%d", successes, rejectedCycles)
	}
	var reverseEdgeCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM work_item_links
		WHERE relation='depends_on' AND source_item_id=ANY($1::text[]) AND target_item_id=ANY($1::text[])
	`, []string{left.ID, right.ID}).Scan(&reverseEdgeCount); err != nil {
		t.Fatalf("count concurrent dependency edges: %v", err)
	}
	if reverseEdgeCount != 1 {
		t.Fatalf("concurrent dependency edge count=%d, want 1", reverseEdgeCount)
	}

	dueAt := time.Now().Add(time.Hour).UnixMilli()
	recurring, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindTask, Title: "매일 점검", SourcePostID: "work-root", IdempotencyKey: "recurring",
		DueAt: dueAt, RecurrenceUnit: RecurrenceDaily, RecurrenceInterval: 1,
	})
	if err != nil {
		t.Fatalf("create recurring task: %v", err)
	}
	done := StatusDone
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, patchErr := service.Patch(ctx, "work-user-a", recurring.ID, PatchInput{Status: &done})
			errs <- patchErr
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for patchErr := range errs {
		if patchErr != nil {
			t.Fatalf("concurrent completion: %v", patchErr)
		}
	}
	var occurrenceCount int
	var nextDue int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT count(*), max(due_at) FROM work_items WHERE series_id=$1
	`, recurring.ID).Scan(&occurrenceCount, &nextDue); err != nil {
		t.Fatalf("read recurring series: %v", err)
	}
	if occurrenceCount != 2 || nextDue <= dueAt {
		t.Fatalf("recurring series count=%d next_due=%d original_due=%d", occurrenceCount, nextDue, dueAt)
	}
}

func TestWorkItemsPostgresRejectsUnresolvedDependencyOnStartedTask(t *testing.T) {
	db := newWorkItemsTestDB(t)
	ctx := workItemsTestContext(t)
	seedWorkItemsFixture(t, ctx, db)
	service := New(db)
	principal := rbac.UserPrincipal("work-user-a")

	dependency, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindTask, Title: "미완료 선행 작업", SourcePostID: "work-root", IdempotencyKey: "late-dependency",
	})
	if err != nil {
		t.Fatalf("create dependency: %v", err)
	}

	for _, status := range []string{StatusInProgress, StatusDone} {
		status := status
		source, _, createErr := service.Create(ctx, "work-user-a", CreateInput{
			Kind: KindTask, Title: "이미 시작된 작업", SourcePostID: "work-root", IdempotencyKey: "late-source-" + status,
		})
		if createErr != nil {
			t.Fatalf("create %s source: %v", status, createErr)
		}
		if _, patchErr := service.Patch(ctx, "work-user-a", source.ID, PatchInput{Status: &status}); patchErr != nil {
			t.Fatalf("transition source to %s: %v", status, patchErr)
		}
		if _, linkErr := service.AddLinkForPrincipal(ctx, principal, source.ID, dependency.ID, RelationDependsOn); !errors.Is(linkErr, ErrBlocked) {
			t.Fatalf("add unresolved dependency to %s source error = %v", status, linkErr)
		}
	}

	cancelled := StatusCancelled
	if _, err := service.Patch(ctx, "work-user-a", dependency.ID, PatchInput{Status: &cancelled}); err != nil {
		t.Fatalf("cancel dependency: %v", err)
	}
	completedSource, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindTask, Title: "완료된 작업", SourcePostID: "work-root", IdempotencyKey: "resolved-late-source",
	})
	if err != nil {
		t.Fatalf("create resolved source: %v", err)
	}
	done := StatusDone
	if _, err := service.Patch(ctx, "work-user-a", completedSource.ID, PatchInput{Status: &done}); err != nil {
		t.Fatalf("complete resolved source: %v", err)
	}
	if _, err := service.AddLinkForPrincipal(ctx, principal, completedSource.ID, dependency.ID, RelationDependsOn); err != nil {
		t.Fatalf("add terminal dependency to completed source: %v", err)
	}
}

func TestWorkItemsPostgresRecurringSpawnClearsInactiveAssignee(t *testing.T) {
	db := newWorkItemsTestDB(t)
	ctx := workItemsTestContext(t)
	seedWorkItemsFixture(t, ctx, db)
	service := New(db)
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users
			(id, username, email, password_hash, roles, guest_expires_at, create_at, update_at)
		VALUES
			('work-valid-guest','work-valid-guest','valid-guest@example.test','hash','system_guest',$1,1,1)
	`, time.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatalf("seed valid guest assignee: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO channel_members (channel_id,user_id,roles,create_at)
		VALUES ('work-channel','work-valid-guest','channel_guest',1)
	`); err != nil {
		t.Fatalf("seed valid guest channel membership: %v", err)
	}

	dueAt := time.Now().Add(time.Hour).UnixMilli()
	tests := []struct {
		name       string
		assigneeID string
		invalidate string
	}{
		{name: "channel membership removed", assigneeID: "work-user-b", invalidate: `DELETE FROM channel_members WHERE channel_id='work-channel' AND user_id='work-user-b'`},
		{name: "user deleted", assigneeID: "work-user-d", invalidate: `UPDATE users SET delete_at=1 WHERE id='work-user-d'`},
		{name: "guest expired", assigneeID: "work-valid-guest", invalidate: `UPDATE users SET guest_expires_at=1 WHERE id='work-valid-guest'`},
	}
	for index, test := range tests {
		item, _, err := service.Create(ctx, "work-user-a", CreateInput{
			Kind: KindTask, Title: "반복 담당자 검증 " + test.name,
			SourcePostID: "work-root", IdempotencyKey: fmt.Sprintf("recurrence-assignee-%d", index),
			AssigneeID: test.assigneeID, DueAt: dueAt,
			RecurrenceUnit: RecurrenceDaily, RecurrenceInterval: 1,
		})
		if err != nil {
			t.Fatalf("create recurring task for %s: %v", test.name, err)
		}
		if _, err := db.Pool.Exec(ctx, test.invalidate); err != nil {
			t.Fatalf("invalidate assignee for %s: %v", test.name, err)
		}
		done := StatusDone
		if _, err := service.Patch(ctx, "work-user-a", item.ID, PatchInput{Status: &done}); err != nil {
			t.Fatalf("complete recurring task for %s: %v", test.name, err)
		}
		var assigneeID, ownerID, cleared string
		if err := db.Pool.QueryRow(ctx, `
			SELECT COALESCE(next.assignee_id,''), next.created_by,
			       COALESCE(event.details->>'assignee_cleared','')
			FROM work_items next
			JOIN work_item_events event ON event.work_item_id=next.id AND event.event_type='created'
			WHERE next.series_id=$1 AND next.occurrence_no=1
		`, item.ID).Scan(&assigneeID, &ownerID, &cleared); err != nil {
			t.Fatalf("read spawned recurrence for %s: %v", test.name, err)
		}
		if assigneeID != "" || ownerID != "work-user-a" || cleared != "true" {
			t.Fatalf("spawn policy for %s: assignee=%q owner=%q cleared=%q", test.name, assigneeID, ownerID, cleared)
		}
	}
}

func TestWorkItemsPostgresRejectsDependencyReopenAfterDependentStarts(t *testing.T) {
	db := newWorkItemsTestDB(t)
	ctx := workItemsTestContext(t)
	seedWorkItemsFixture(t, ctx, db)
	service := New(db)

	dependency, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindTask, Title: "완료할 선행 작업", SourcePostID: "work-root", IdempotencyKey: "reopen-dependency",
	})
	if err != nil {
		t.Fatalf("create dependency: %v", err)
	}
	dependent, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindTask, Title: "진행할 후속 작업", SourcePostID: "work-root", IdempotencyKey: "reopen-dependent",
		DependencyIDs: []string{dependency.ID},
	})
	if err != nil {
		t.Fatalf("create dependent: %v", err)
	}
	done := StatusDone
	if _, err := service.Patch(ctx, "work-user-a", dependency.ID, PatchInput{Status: &done}); err != nil {
		t.Fatalf("complete dependency: %v", err)
	}
	inProgress := StatusInProgress
	if _, err := service.Patch(ctx, "work-user-a", dependent.ID, PatchInput{Status: &inProgress}); err != nil {
		t.Fatalf("start dependent: %v", err)
	}
	open := StatusOpen
	if _, err := service.Patch(ctx, "work-user-a", dependency.ID, PatchInput{Status: &open}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("reopen dependency error = %v", err)
	}
	var dependencyStatus string
	if err := db.Pool.QueryRow(ctx, `SELECT status FROM work_items WHERE id=$1`, dependency.ID).Scan(&dependencyStatus); err != nil {
		t.Fatalf("read dependency status: %v", err)
	}
	if dependencyStatus != StatusDone {
		t.Fatalf("blocked dependency status=%q, want done", dependencyStatus)
	}

	cancelledDependency, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindTask, Title: "취소할 선행 작업", SourcePostID: "work-root", IdempotencyKey: "reopen-cancelled-dependency",
	})
	if err != nil {
		t.Fatalf("create cancelled dependency: %v", err)
	}
	completedDependent, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindTask, Title: "완료할 후속 작업", SourcePostID: "work-root", IdempotencyKey: "reopen-completed-dependent",
		DependencyIDs: []string{cancelledDependency.ID},
	})
	if err != nil {
		t.Fatalf("create completed dependent: %v", err)
	}
	cancelled := StatusCancelled
	if _, err := service.Patch(ctx, "work-user-a", cancelledDependency.ID, PatchInput{Status: &cancelled}); err != nil {
		t.Fatalf("cancel dependency: %v", err)
	}
	if _, err := service.Patch(ctx, "work-user-a", completedDependent.ID, PatchInput{Status: &done}); err != nil {
		t.Fatalf("complete dependent: %v", err)
	}
	if _, err := service.Patch(ctx, "work-user-a", cancelledDependency.ID, PatchInput{Status: &open}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("reopen dependency under completed dependent error = %v", err)
	}
}

func TestWorkItemsPostgresSerializesConcurrentDependencyReopenAndStart(t *testing.T) {
	db := newWorkItemsTestDB(t)
	ctx := workItemsTestContext(t)
	seedWorkItemsFixture(t, ctx, db)
	service := New(db)

	dependency, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindTask, Title: "동시 선행 작업", SourcePostID: "work-root", IdempotencyKey: "concurrent-reopen-dependency",
	})
	if err != nil {
		t.Fatalf("create dependency: %v", err)
	}
	dependent, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindTask, Title: "동시 후속 작업", SourcePostID: "work-root", IdempotencyKey: "concurrent-reopen-dependent",
		DependencyIDs: []string{dependency.ID},
	})
	if err != nil {
		t.Fatalf("create dependent: %v", err)
	}
	done := StatusDone
	if _, err := service.Patch(ctx, "work-user-a", dependency.ID, PatchInput{Status: &done}); err != nil {
		t.Fatalf("complete dependency: %v", err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, operation := range []func() error{
		func() error {
			inProgress := StatusInProgress
			_, err := service.Patch(ctx, "work-user-a", dependent.ID, PatchInput{Status: &inProgress})
			return err
		},
		func() error {
			open := StatusOpen
			_, err := service.Patch(ctx, "work-user-a", dependency.ID, PatchInput{Status: &open})
			return err
		},
	} {
		operation := operation
		go func() {
			<-start
			errs <- operation()
		}()
	}
	close(start)
	var successes, blocked int
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			successes++
		case errors.Is(err, ErrBlocked):
			blocked++
		default:
			t.Fatalf("concurrent dependency transition: %v", err)
		}
	}
	if successes != 1 || blocked != 1 {
		t.Fatalf("concurrent dependency transitions successes=%d blocked=%d", successes, blocked)
	}
	var dependentStatus, dependencyStatus string
	if err := db.Pool.QueryRow(ctx, `
		SELECT dependent.status, dependency.status
		FROM work_items dependent, work_items dependency
		WHERE dependent.id=$1 AND dependency.id=$2
	`, dependent.ID, dependency.ID).Scan(&dependentStatus, &dependencyStatus); err != nil {
		t.Fatalf("read concurrent transition statuses: %v", err)
	}
	if (dependentStatus == StatusInProgress || dependentStatus == StatusDone) &&
		dependencyStatus != StatusDone && dependencyStatus != StatusCancelled {
		t.Fatalf("dependency invariant broken: dependent=%q dependency=%q", dependentStatus, dependencyStatus)
	}
}

func TestWorkItemsPostgresDecisionLifecycleReplayAndScopedHistory(t *testing.T) {
	db := newWorkItemsTestDB(t)
	ctx := workItemsTestContext(t)
	seedWorkItemsFixture(t, ctx, db)
	service := New(db)

	task, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindTask, Title: "영향 작업", SourcePostID: "work-root", IdempotencyKey: "impact-task",
	})
	if err != nil {
		t.Fatalf("create impacted task: %v", err)
	}
	proposed, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindDecision, Title: "검토할 결정", SourcePostID: "work-root", IdempotencyKey: "proposed-decision",
		InitialStatus: StatusProposed, ReviewerID: "work-user-b", ImpactTaskIDs: []string{task.ID},
	})
	if err != nil || proposed.Status != StatusProposed || proposed.DecidedAt != 0 {
		t.Fatalf("create proposed decision = %#v, %v", proposed, err)
	}
	underReview := StatusUnderReview
	if _, err := service.Patch(ctx, "work-user-b", proposed.ID, PatchInput{Status: &underReview}); err != nil {
		t.Fatalf("reviewer starts review: %v", err)
	}
	recorded := StatusRecorded
	current, err := service.Patch(ctx, "work-user-b", proposed.ID, PatchInput{Status: &recorded})
	if err != nil || current.DecidedAt == 0 {
		t.Fatalf("reviewer records decision = %#v, %v", current, err)
	}
	superseded := StatusSuperseded
	if _, err := service.Patch(ctx, "work-user-a", proposed.ID, PatchInput{Status: &superseded}); !errors.Is(err, ErrTransition) {
		t.Fatalf("standalone supersede error = %v", err)
	}
	if _, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindDecision, Title: "미결 대체안", SourcePostID: "work-root", IdempotencyKey: "invalid-proposed-replacement",
		InitialStatus: StatusProposed, SupersedesID: proposed.ID,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("proposed replacement error = %v", err)
	}
	replacementInput := CreateInput{
		Kind: KindDecision, Title: "확정 대체안", SourcePostID: "work-root", IdempotencyKey: "recorded-replacement",
		InitialStatus: StatusRecorded, SupersedesID: proposed.ID, ImpactTaskIDs: []string{task.ID},
	}
	replacement, replayed, err := service.Create(ctx, "work-user-a", replacementInput)
	if err != nil || replayed {
		t.Fatalf("create replacement = %#v, replayed=%v, err=%v", replacement, replayed, err)
	}
	replayedReplacement, replayed, err := service.Create(ctx, "work-user-a", replacementInput)
	if err != nil || !replayed || replayedReplacement.ID != replacement.ID {
		t.Fatalf("replay replacement = %#v, replayed=%v, err=%v", replayedReplacement, replayed, err)
	}
	page, err := service.ListForUser(ctx, "work-user-a", ListOptions{Kind: KindDecision})
	if err != nil {
		t.Fatalf("list decisions: %v", err)
	}
	var predecessor *Item
	for index := range page.Items {
		if page.Items[index].ID == proposed.ID {
			predecessor = &page.Items[index]
		}
	}
	if predecessor == nil || predecessor.Status != StatusSuperseded || predecessor.SupersededByID != replacement.ID {
		t.Fatalf("superseded predecessor = %#v", predecessor)
	}
	events, err := service.ListEventsForPrincipal(ctx, rbac.UserPrincipal("work-user-a"), proposed.ID)
	if err != nil || len(events) < 4 {
		t.Fatalf("decision history = %#v, %v", events, err)
	}

	outOfScope, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindTask, Title: "다른 채널", SourcePostID: "other-work-post", IdempotencyKey: "empty-history-out-of-scope",
	})
	if err != nil {
		t.Fatalf("create out-of-scope task: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM work_item_events WHERE work_item_id=$1`, outOfScope.ID); err != nil {
		t.Fatalf("remove events for fallback test: %v", err)
	}
	principal := rbac.Principal{
		UserID: "work-user-a", Restricted: true,
		AllowedTeamIDs:    map[string]struct{}{"work-team": {}},
		AllowedChannelIDs: map[string]struct{}{"work-channel": {}},
	}
	if _, err := service.ListEventsForPrincipal(ctx, principal, outOfScope.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty out-of-scope history error = %v", err)
	}
}

func TestWorkItemsPostgresExpiredGuestIsNotAnActiveActorAssigneeOrReviewer(t *testing.T) {
	db := newWorkItemsTestDB(t)
	ctx := workItemsTestContext(t)
	seedWorkItemsFixture(t, ctx, db)
	service := New(db)

	if _, _, err := service.Create(ctx, "work-expired-guest", CreateInput{
		Kind: KindTask, Title: "만료 게스트 작업", SourcePostID: "work-root", IdempotencyKey: "expired-actor",
	}); !errors.Is(err, ErrSourceNotAccessible) {
		t.Fatalf("expired guest actor error = %v", err)
	}
	if _, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindTask, Title: "만료 게스트 담당", SourcePostID: "work-root", IdempotencyKey: "expired-assignee",
		AssigneeID: "work-expired-guest",
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expired guest assignee error = %v", err)
	}
	task, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindTask, Title: "재할당 테스트", SourcePostID: "work-root", IdempotencyKey: "expired-reassignment",
	})
	if err != nil {
		t.Fatalf("create reassignment task: %v", err)
	}
	expiredID := "work-expired-guest"
	if _, err := service.Patch(ctx, "work-user-a", task.ID, PatchInput{AssigneeID: &expiredID}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expired guest reassignment error = %v", err)
	}
	if _, _, err := service.Create(ctx, "work-user-a", CreateInput{
		Kind: KindDecision, Title: "만료 게스트 검토", SourcePostID: "work-root", IdempotencyKey: "expired-reviewer",
		InitialStatus: StatusProposed, ReviewerID: "work-expired-guest",
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expired guest reviewer error = %v", err)
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
			('work-user-d','work-user-d','work-d@example.test','hash','system_user',1,1),
			('work-expired-guest','work-expired-guest','expired@example.test','hash','system_guest',1,1);
		UPDATE users SET guest_expires_at=1 WHERE id='work-expired-guest';
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
			('work-channel','work-expired-guest','channel_user',1),
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
