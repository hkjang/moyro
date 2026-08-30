package activityevents

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

const activityEventsTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

func TestActivityEventsPostgresDedupePaginationFiltersAndState(t *testing.T) {
	db := newActivityEventsTestDB(t)
	ctx := activityEventsTestContext(t)
	seedActivityEventUsers(t, ctx, db)
	service := New(db)
	now := int64(1_000)
	service.nowMS = func() int64 { return now }

	mention, err := service.Emit(ctx, EmitInput{
		UserID: "activity-user-a", Type: TypeMention, DedupeKey: "post-1:user-a",
		ActorID: "activity-user-b", TeamID: "team-1", ChannelID: "channel-1", PostID: "post-1",
		ResourceType: "post", ResourceID: "post-1", Title: "새 멘션", Summary: "원본 요약",
	})
	if err != nil {
		t.Fatalf("emit mention: %v", err)
	}
	now++
	replayed, err := service.Emit(ctx, EmitInput{
		UserID: "activity-user-a", Type: TypeMention, DedupeKey: "post-1:user-a",
		Title: "재시도 제목", Summary: "재시도에서 바뀌면 안 됨",
	})
	if err != nil {
		t.Fatalf("replay mention: %v", err)
	}
	if replayed.ID != mention.ID || replayed.Title != mention.Title || replayed.Summary != mention.Summary {
		t.Fatalf("replayed event = %#v, original = %#v", replayed, mention)
	}

	now++
	reply, err := service.Emit(ctx, EmitInput{
		UserID: "activity-user-a", Type: TypeThreadReply, DedupeKey: "post-2:user-a",
		ActorID: "activity-user-b", ChannelID: "channel-1", PostID: "post-2",
		ResourceType: "post", ResourceID: "post-2", Title: "스레드 답글",
	})
	if err != nil {
		t.Fatalf("emit reply: %v", err)
	}
	for _, typ := range []EventType{
		TypeMention, TypeThreadReply, TypeDirectMessage, TypeApprovalRequested,
		TypeDecided, TypeReminderFired, TypeTaskAssigned, TypeSystemWarning, TypePluginEvent,
	} {
		now++
		if _, err := service.Emit(ctx, EmitInput{
			UserID: "activity-user-b", Type: typ, DedupeKey: "type:" + string(typ),
			Title: "다른 사용자의 비공개 이벤트",
		}); err != nil {
			t.Fatalf("emit %s event: %v", typ, err)
		}
	}
	foreign, err := service.List(ctx, "activity-user-b", ListOptions{})
	if err != nil || len(foreign.Events) != len(supportedEventTypes) {
		t.Fatalf("supported event type rows = %d, %v", len(foreign.Events), err)
	}

	first, err := service.List(ctx, "activity-user-a", ListOptions{Limit: 1})
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(first.Events) != 1 || first.Events[0].ID != reply.ID || first.NextCursor == "" {
		t.Fatalf("first page = %#v", first)
	}
	second, err := service.List(ctx, "activity-user-a", ListOptions{Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if len(second.Events) != 1 || second.Events[0].ID != mention.ID || second.NextCursor != "" {
		t.Fatalf("second page = %#v", second)
	}
	mentions, err := service.List(ctx, "activity-user-a", ListOptions{Types: []EventType{TypeMention}})
	if err != nil || len(mentions.Events) != 1 || mentions.Events[0].ID != mention.ID {
		t.Fatalf("mention filter = %#v, %v", mentions, err)
	}

	now = 2_000
	read, completed := true, true
	snoozedUntil := int64(5_000)
	updated, err := service.UpdateState(ctx, "activity-user-a", mention.ID, StatePatch{
		Read: &read, Completed: &completed, SnoozedUntil: &snoozedUntil,
	})
	if err != nil {
		t.Fatalf("update state: %v", err)
	}
	if updated.ReadAt != now || updated.CompletedAt != now || updated.SnoozedUntil != snoozedUntil {
		t.Fatalf("updated state = %#v", updated)
	}
	if _, err := service.UpdateState(ctx, "activity-user-b", mention.ID, StatePatch{Read: &read}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign state update error = %v", err)
	}
	unread, err := service.List(ctx, "activity-user-a", ListOptions{UnreadOnly: true})
	if err != nil || len(unread.Events) != 1 || unread.Events[0].ID != reply.ID {
		t.Fatalf("unread filter = %#v, %v", unread, err)
	}

	now++
	count, err := service.MarkRead(ctx, "activity-user-a", []string{reply.ID, reply.ID, mention.ID})
	if err != nil || count != 1 {
		t.Fatalf("bulk mark read count = %d, %v", count, err)
	}
	count, err = service.MarkRead(ctx, "activity-user-b", []string{mention.ID, reply.ID})
	if err != nil || count != 0 {
		t.Fatalf("foreign bulk mark read count = %d, %v", count, err)
	}
	unread, err = service.List(ctx, "activity-user-a", ListOptions{UnreadOnly: true})
	if err != nil || len(unread.Events) != 0 {
		t.Fatalf("unread after bulk update = %#v, %v", unread, err)
	}

	var ownerCount int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM activity_events WHERE user_id='activity-user-a'`).Scan(&ownerCount); err != nil {
		t.Fatalf("count owner events: %v", err)
	}
	if ownerCount != 2 {
		t.Fatalf("owner event count = %d, want 2", ownerCount)
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM channel_members WHERE channel_id='channel-1' AND user_id='activity-user-a'`); err != nil {
		t.Fatalf("remove activity recipient membership: %v", err)
	}
	if _, err := service.UpdateState(ctx, "activity-user-a", mention.ID, StatePatch{Read: &read}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("state update after channel access removal error = %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE activity_events SET read_at=0 WHERE id=$1`, reply.ID); err != nil {
		t.Fatalf("reset reply state for revoked access test: %v", err)
	}
	count, err = service.MarkRead(ctx, "activity-user-a", []string{reply.ID})
	if err != nil || count != 0 {
		t.Fatalf("bulk mark read after channel access removal count = %d, %v", count, err)
	}
	afterRemoval, err := service.List(ctx, "activity-user-a", ListOptions{})
	if err != nil || len(afterRemoval.Events) != 0 {
		t.Fatalf("events after channel access removal = %#v, %v", afterRemoval, err)
	}
}

func TestActivityEventsPostgresCredentialScopeIntersection(t *testing.T) {
	db := newActivityEventsTestDB(t)
	ctx := activityEventsTestContext(t)
	seedActivityEventUsers(t, ctx, db)
	service := New(db)
	now := int64(3_000)
	service.nowMS = func() int64 {
		now++
		return now
	}

	allowed, err := service.Emit(ctx, EmitInput{
		UserID: "activity-user-a", Type: TypeMention, DedupeKey: "scope:allowed",
		TeamID: "team-1", ChannelID: "channel-1", Title: "허용 이벤트",
	})
	if err != nil {
		t.Fatalf("emit allowed event: %v", err)
	}
	denied, err := service.Emit(ctx, EmitInput{
		UserID: "activity-user-a", Type: TypeMention, DedupeKey: "scope:denied",
		TeamID: "team-2", ChannelID: "channel-2", Title: "제한 밖 이벤트",
	})
	if err != nil {
		t.Fatalf("emit denied event: %v", err)
	}
	unscoped, err := service.Emit(ctx, EmitInput{
		UserID: "activity-user-a", Type: TypeSystemWarning, DedupeKey: "scope:unscoped",
		Title: "리소스 없는 이벤트",
	})
	if err != nil {
		t.Fatalf("emit unscoped event: %v", err)
	}

	unrestricted, err := service.List(ctx, "activity-user-a", ListOptions{})
	if err != nil || len(unrestricted.Events) != 3 {
		t.Fatalf("unrestricted list = %#v, %v", unrestricted, err)
	}
	principal := rbac.Principal{
		UserID:            "activity-user-a",
		CredentialID:      "scoped-key",
		Restricted:        true,
		AllowedTeamIDs:    map[string]struct{}{"team-1": {}},
		AllowedChannelIDs: map[string]struct{}{"channel-1": {}},
	}
	page, err := service.ListForPrincipal(ctx, principal, ListOptions{})
	if err != nil || len(page.Events) != 1 || page.Events[0].ID != allowed.ID {
		t.Fatalf("credential-scoped list = %#v, %v", page, err)
	}

	read := true
	if _, err := service.UpdateStateForPrincipal(ctx, principal, denied.ID, StatePatch{Read: &read}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("out-of-scope update error = %v", err)
	}
	count, err := service.MarkReadForPrincipal(ctx, principal, []string{allowed.ID, denied.ID, unscoped.ID})
	if err != nil || count != 1 {
		t.Fatalf("credential-scoped mark-read count = %d, %v", count, err)
	}
	var deniedReadAt, unscopedReadAt int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT
			(SELECT read_at FROM activity_events WHERE id=$1),
			(SELECT read_at FROM activity_events WHERE id=$2)
	`, denied.ID, unscoped.ID).Scan(&deniedReadAt, &unscopedReadAt); err != nil {
		t.Fatalf("read denied activity state: %v", err)
	}
	if deniedReadAt != 0 || unscopedReadAt != 0 {
		t.Fatalf("out-of-scope events changed: denied=%d unscoped=%d", deniedReadAt, unscopedReadAt)
	}
	completed := true
	updated, err := service.UpdateStateForPrincipal(ctx, principal, allowed.ID, StatePatch{Completed: &completed})
	if err != nil || updated.CompletedAt == 0 {
		t.Fatalf("in-scope update = %#v, %v", updated, err)
	}
}

func TestActivityEventsPostgresLiveTeamAndChannelAccess(t *testing.T) {
	db := newActivityEventsTestDB(t)
	ctx := activityEventsTestContext(t)
	seedActivityEventUsers(t, ctx, db)
	service := New(db)
	now := int64(4_000)
	service.nowMS = func() int64 {
		now++
		return now
	}

	channelEvent, err := service.Emit(ctx, EmitInput{
		UserID: "activity-user-a", Type: TypeMention, DedupeKey: "live:channel",
		TeamID: "team-1", ChannelID: "channel-1", Title: "채널 이벤트",
	})
	if err != nil {
		t.Fatalf("emit channel event: %v", err)
	}
	teamEvent, err := service.Emit(ctx, EmitInput{
		UserID: "activity-user-a", Type: TypeApprovalRequested, DedupeKey: "live:team",
		TeamID: "team-1", ResourceType: "approval", ResourceID: "approval-1", Title: "팀 이벤트",
	})
	if err != nil {
		t.Fatalf("emit team event: %v", err)
	}
	page, err := service.List(ctx, "activity-user-a", ListOptions{})
	if err != nil || len(page.Events) != 2 {
		t.Fatalf("initial live events = %#v, %v", page, err)
	}

	if _, err := db.Pool.Exec(ctx, `DELETE FROM team_members WHERE team_id='team-1' AND user_id='activity-user-a'`); err != nil {
		t.Fatalf("remove team membership: %v", err)
	}
	page, err = service.List(ctx, "activity-user-a", ListOptions{})
	if err != nil || len(page.Events) != 0 {
		t.Fatalf("events after team membership removal = %#v, %v", page, err)
	}
	read := true
	if _, err := service.UpdateState(ctx, "activity-user-a", channelEvent.ID, StatePatch{Read: &read}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("channel state after team membership removal error = %v", err)
	}
	count, err := service.MarkRead(ctx, "activity-user-a", []string{channelEvent.ID, teamEvent.ID})
	if err != nil || count != 0 {
		t.Fatalf("mark read after team membership removal = %d, %v", count, err)
	}

	if _, err := db.Pool.Exec(ctx, `INSERT INTO team_members (team_id,user_id,roles,create_at) VALUES ('team-1','activity-user-a','team_user',1)`); err != nil {
		t.Fatalf("restore team membership: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE channels SET delete_at=5000 WHERE id='channel-1'`); err != nil {
		t.Fatalf("archive channel: %v", err)
	}
	page, err = service.List(ctx, "activity-user-a", ListOptions{})
	if err != nil || len(page.Events) != 1 || page.Events[0].ID != teamEvent.ID {
		t.Fatalf("events after channel archive = %#v, %v", page, err)
	}
	if _, err := service.UpdateState(ctx, "activity-user-a", channelEvent.ID, StatePatch{Read: &read}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("channel state after archive error = %v", err)
	}
	count, err = service.MarkRead(ctx, "activity-user-a", []string{channelEvent.ID, teamEvent.ID})
	if err != nil || count != 1 {
		t.Fatalf("mark read after channel archive = %d, %v", count, err)
	}

	if _, err := db.Pool.Exec(ctx, `UPDATE teams SET delete_at=5001 WHERE id='team-1'`); err != nil {
		t.Fatalf("archive team: %v", err)
	}
	page, err = service.List(ctx, "activity-user-a", ListOptions{})
	if err != nil || len(page.Events) != 0 {
		t.Fatalf("events after team archive = %#v, %v", page, err)
	}
}

func TestActivityEventsPostgresReviewerPermissionRevocationFailsClosed(t *testing.T) {
	db := newActivityEventsTestDB(t)
	ctx := activityEventsTestContext(t)
	seedActivityEventUsers(t, ctx, db)
	service := New(db)
	service.nowMS = func() int64 { return 6_000 }
	allowed := true
	service.SetApprovalReviewAuthorizer(func(_ context.Context, userID, requestID string) (bool, error) {
		return allowed && userID == "activity-user-a" && requestID == "approval-review-1", nil
	})

	reviewEvent, err := service.Emit(ctx, EmitInput{
		UserID: "activity-user-a", Type: TypeApprovalRequested, DedupeKey: "approval-review-1",
		TeamID: "team-1", ChannelID: "channel-1", ResourceType: "approval_review",
		ResourceID: "approval-review-1", Title: "검토할 승인 요청",
	})
	if err != nil {
		t.Fatalf("emit approval review event: %v", err)
	}
	page, err := service.List(ctx, "activity-user-a", ListOptions{})
	if err != nil || len(page.Events) != 1 || page.Events[0].ID != reviewEvent.ID {
		t.Fatalf("authorized reviewer list = %#v, %v", page, err)
	}

	allowed = false
	page, err = service.List(ctx, "activity-user-a", ListOptions{})
	if err != nil || len(page.Events) != 0 {
		t.Fatalf("revoked reviewer list = %#v, %v", page, err)
	}
	read := true
	if _, err := service.UpdateState(ctx, "activity-user-a", reviewEvent.ID, StatePatch{Read: &read}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked reviewer state update error = %v", err)
	}
	count, err := service.MarkRead(ctx, "activity-user-a", []string{reviewEvent.ID})
	if err != nil || count != 0 {
		t.Fatalf("revoked reviewer mark-read = %d, %v", count, err)
	}
	var readAt int64
	if err := db.Pool.QueryRow(ctx, `SELECT read_at FROM activity_events WHERE id=$1`, reviewEvent.ID).Scan(&readAt); err != nil {
		t.Fatalf("read revoked reviewer event state: %v", err)
	}
	if readAt != 0 {
		t.Fatalf("revoked reviewer event mutated: read_at=%d", readAt)
	}

	service.SetApprovalReviewAuthorizer(nil)
	page, err = service.List(ctx, "activity-user-a", ListOptions{})
	if err != nil || len(page.Events) != 0 {
		t.Fatalf("unwired reviewer authorizer did not fail closed: %#v, %v", page, err)
	}
}

func newActivityEventsTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(activityEventsTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", activityEventsTestPostgresDSN)
	}
	ctx := activityEventsTestContext(t)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open activity-event test admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping activity-event test PostgreSQL: %v", err)
	}

	schemaName := "moyro_activity_events_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create activity-event test schema: %v", err)
	}

	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop activity-event test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse activity-event test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 4
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated activity-event test pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated activity-event test pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate activity-event test schema: %v", err)
	}
	return db
}

func seedActivityEventUsers(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES
			('activity-user-a', 'activity-user-a', 'activity-a@example.test', 'hash', 'system_user', 1, 1),
			('activity-user-b', 'activity-user-b', 'activity-b@example.test', 'hash', 'system_user', 1, 1);
		INSERT INTO teams (id, name, display_name, type, create_at, update_at)
		VALUES
			('team-1', 'team-1', 'Activity Team', 'O', 1, 1),
			('team-2', 'team-2', 'Other Activity Team', 'O', 1, 1);
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES
			('team-1', 'activity-user-a', 'team_user', 1),
			('team-1', 'activity-user-b', 'team_user', 1),
			('team-2', 'activity-user-a', 'team_user', 1),
			('team-2', 'activity-user-b', 'team_user', 1);
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES
			('channel-1', 'team-1', 'O', 'Activity Channel', 'activity-channel', 1, 1),
			('channel-2', 'team-2', 'O', 'Other Activity Channel', 'other-activity-channel', 1, 1);
		INSERT INTO channel_members (channel_id, user_id, roles, create_at)
		VALUES
			('channel-1', 'activity-user-a', 'channel_user', 1),
			('channel-1', 'activity-user-b', 'channel_user', 1),
			('channel-2', 'activity-user-a', 'channel_user', 1),
			('channel-2', 'activity-user-b', 'channel_user', 1)
	`); err != nil {
		t.Fatalf("seed activity-event users: %v", err)
	}
}

func activityEventsTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	return ctx
}
