package documents

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const documentsTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

func TestDocumentsPostgresAuthorizationRevisionStaleAndReplay(t *testing.T) {
	db := newDocumentsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	seedDocumentsFixture(t, ctx, db)
	service := New(db)
	service.nowMS = func() int64 { return 10_000 }

	source, err := service.Source(ctx, rbac.UserPrincipal("document-user-a"), "document-reply")
	if err != nil {
		t.Fatal(err)
	}
	if source.ThreadID != "document-root" || source.ChannelID != "document-channel" || source.TeamID != "document-team" || source.CursorAt <= 0 || len(source.Posts) != 2 {
		t.Fatalf("source = %#v", source)
	}
	originalRevision := source.CursorAt
	if _, err := service.Source(ctx, rbac.UserPrincipal("document-user-c"), "document-root"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-member source error = %v", err)
	}

	input := CreateInput{
		Title: "장애 대응 회의록", Body: "# 결론\n복구 절차를 고정합니다.",
		SourcePostID: "document-reply", SourceCursorAt: source.CursorAt,
		IdempotencyKey: "document-request-1",
	}
	document, replayed, err := service.Create(ctx, rbac.UserPrincipal("document-user-a"), input)
	if err != nil || replayed {
		t.Fatalf("create = %#v, replayed=%v, err=%v", document, replayed, err)
	}
	if document.SourceThreadID != "document-root" || document.Revision != 1 || document.Stale {
		t.Fatalf("created document = %#v", document)
	}
	replayedDocument, replayed, err := service.Create(ctx, rbac.UserPrincipal("document-user-a"), input)
	if err != nil || !replayed || replayedDocument.ID != document.ID {
		t.Fatalf("replay = %#v, replayed=%v, err=%v", replayedDocument, replayed, err)
	}
	changedReplay := input
	changedReplay.Body = "다른 본문"
	if _, _, err := service.Create(ctx, rbac.UserPrincipal("document-user-a"), changedReplay); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed replay error = %v", err)
	}

	visible, err := service.Get(ctx, rbac.UserPrincipal("document-user-b"), document.ID)
	if err != nil || visible.ID != document.ID {
		t.Fatalf("member get = %#v, %v", visible, err)
	}
	memberEdit := "멤버가 바꾸려는 제목"
	if _, err := service.Patch(ctx, rbac.UserPrincipal("document-user-b"), document.ID, PatchInput{
		Title: &memberEdit, ExpectedRevision: 1,
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-owner patch error = %v", err)
	}

	allowedKey := rbac.Principal{
		UserID: "document-user-a", Restricted: true,
		AllowedTeamIDs:    map[string]struct{}{"document-team": {}},
		AllowedChannelIDs: map[string]struct{}{"document-channel": {}},
	}
	if list, err := service.List(ctx, allowedKey, ListOptions{}); err != nil || len(list) != 1 {
		t.Fatalf("scoped list = %#v, %v", list, err)
	}
	deniedKey := allowedKey
	deniedKey.AllowedChannelIDs = map[string]struct{}{"other-document-channel": {}}
	if _, err := service.Get(ctx, deniedKey, document.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("out-of-scope get error = %v", err)
	}
	postService := posts.New(db)
	if pinned, err := postService.SetPinned(ctx, "document-root", true); err != nil || pinned == nil || !pinned.IsPinned {
		t.Fatalf("pin source metadata = %#v, %v", pinned, err)
	}
	if err := postService.UpdateFileIDs(ctx, "document-root", []string{"metadata-file"}); err != nil {
		t.Fatalf("attach source metadata: %v", err)
	}
	if updatedPost, err := postService.Update(ctx, "document-root", "document-user-a", "장애 대응 절차", map[string]any{"metadata_only": true}); err != nil || updatedPost == nil {
		t.Fatalf("update source props metadata = %#v, %v", updatedPost, err)
	}
	metadataSource, err := service.Source(ctx, rbac.UserPrincipal("document-user-a"), "document-root")
	if err != nil || metadataSource.CursorAt != originalRevision {
		t.Fatalf("metadata-only source revision = %#v, %v; want %d", metadataSource, err, originalRevision)
	}
	metadataDocument, err := service.Get(ctx, rbac.UserPrincipal("document-user-a"), document.ID)
	if err != nil || metadataDocument.Stale {
		t.Fatalf("metadata-only update made document stale: %#v, %v", metadataDocument, err)
	}

	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO posts (id,channel_id,user_id,root_id,message,create_at,update_at)
		VALUES ('document-new-reply','document-channel','document-user-b','document-root','새 답글',3,3)
	`); err != nil {
		t.Fatalf("insert new reply: %v", err)
	}
	stale, err := service.Get(ctx, rbac.UserPrincipal("document-user-a"), document.ID)
	if err != nil || !stale.Stale {
		t.Fatalf("stale document = %#v, %v", stale, err)
	}
	retryAfterChange, replayed, err := service.Create(ctx, rbac.UserPrincipal("document-user-a"), input)
	if err != nil || !replayed || retryAfterChange.ID != document.ID {
		t.Fatalf("response-lost retry after source change = %#v, replayed=%v, err=%v", retryAfterChange, replayed, err)
	}
	staleCreate := input
	staleCreate.IdempotencyKey = "document-stale-create"
	if _, _, err := service.Create(ctx, rbac.UserPrincipal("document-user-a"), staleCreate); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("new reply with lower timestamp did not change opaque source revision: %v", err)
	}
	refreshedBody := "# 최신 결론\n새 답글까지 반영했습니다."
	oldCursor := originalRevision
	if _, err := service.Patch(ctx, rbac.UserPrincipal("document-user-a"), document.ID, PatchInput{
		Body: &refreshedBody, SourceCursorAt: &oldCursor, ExpectedRevision: 1,
	}); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("old source cursor error = %v", err)
	}
	latestSource, err := service.Source(ctx, rbac.UserPrincipal("document-user-a"), "document-root")
	if err != nil || latestSource.CursorAt <= 0 || latestSource.CursorAt == originalRevision {
		t.Fatalf("latest source = %#v, %v", latestSource, err)
	}
	updated, err := service.Patch(ctx, rbac.UserPrincipal("document-user-a"), document.ID, PatchInput{
		Body: &refreshedBody, SourceCursorAt: &latestSource.CursorAt, ExpectedRevision: 1,
	})
	if err != nil || updated.Revision != 2 || updated.Stale || updated.Body != refreshedBody {
		t.Fatalf("updated document = %#v, %v", updated, err)
	}
	if _, err := db.Pool.Exec(ctx, `
		UPDATE posts SET message='같은 millisecond에 수정된 새 답글'
		WHERE id='document-new-reply'
	`); err != nil {
		t.Fatalf("same-ms source edit: %v", err)
	}
	sameMillisecondEdit, err := service.Get(ctx, rbac.UserPrincipal("document-user-a"), document.ID)
	if err != nil || !sameMillisecondEdit.Stale {
		t.Fatalf("same-ms edited document = %#v, %v", sameMillisecondEdit, err)
	}
	if _, err := service.Patch(ctx, rbac.UserPrincipal("document-user-a"), document.ID, PatchInput{
		Title: &memberEdit, ExpectedRevision: 1,
	}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale revision error = %v", err)
	}

	if _, err := db.Pool.Exec(ctx, `DELETE FROM channel_members WHERE channel_id='document-channel' AND user_id='document-user-b'`); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	if _, err := service.Get(ctx, rbac.UserPrincipal("document-user-b"), document.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed member get error = %v", err)
	}
	deleted, err := service.Delete(ctx, rbac.UserPrincipal("document-user-a"), document.ID, 2)
	if err != nil || deleted.DeleteAt == 0 || deleted.Revision != 3 {
		t.Fatalf("delete = %#v, %v", deleted, err)
	}
	if _, err := service.Get(ctx, rbac.UserPrincipal("document-user-a"), document.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted get error = %v", err)
	}
	currentSource, err := service.Source(ctx, rbac.UserPrincipal("document-user-a"), "document-root")
	if err != nil || currentSource.CursorAt == latestSource.CursorAt {
		t.Fatalf("same-ms edit source revision = %#v, %v", currentSource, err)
	}
	input.SourceCursorAt = currentSource.CursorAt
	if _, _, err := service.Create(ctx, rbac.UserPrincipal("document-user-a"), input); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("deleted replay error = %v", err)
	}
}

func TestDocumentsPostgresCreateReplaySurvivesPatch(t *testing.T) {
	db := newDocumentsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	seedDocumentsFixture(t, ctx, db)
	service := New(db)
	service.nowMS = func() int64 { return 20_000 }

	source, err := service.Source(ctx, rbac.UserPrincipal("document-user-a"), "document-root")
	if err != nil {
		t.Fatal(err)
	}
	input := CreateInput{
		Title: "원래 제목", Body: "원래 본문", SourcePostID: "document-root",
		SourceCursorAt: source.CursorAt, IdempotencyKey: "immutable-create-replay",
	}
	document, replayed, err := service.Create(ctx, rbac.UserPrincipal("document-user-a"), input)
	if err != nil || replayed {
		t.Fatalf("create = %#v, replayed=%v, err=%v", document, replayed, err)
	}
	originalFingerprint := document.CreateFingerprint
	if len(originalFingerprint) != sha256.Size*2 {
		t.Fatalf("create fingerprint length = %d", len(originalFingerprint))
	}
	patchedTitle, patchedBody := "수정된 제목", "수정된 본문"
	patched, err := service.Patch(ctx, rbac.UserPrincipal("document-user-a"), document.ID, PatchInput{
		Title: &patchedTitle, Body: &patchedBody, ExpectedRevision: document.Revision,
	})
	if err != nil || patched.Revision != 2 || patched.Title != patchedTitle || patched.Body != patchedBody {
		t.Fatalf("patch = %#v, %v", patched, err)
	}
	if patched.CreateFingerprint != originalFingerprint {
		t.Fatalf("patch changed create fingerprint: got %q, want %q", patched.CreateFingerprint, originalFingerprint)
	}
	replayedDocument, replayed, err := service.Create(ctx, rbac.UserPrincipal("document-user-a"), input)
	if err != nil || !replayed || replayedDocument.ID != document.ID || replayedDocument.Revision != 2 || replayedDocument.Body != patchedBody {
		t.Fatalf("replay after patch = %#v, replayed=%v, err=%v", replayedDocument, replayed, err)
	}
	conflicting := input
	conflicting.Body = "생성 요청 자체가 다른 본문"
	if _, _, err := service.Create(ctx, rbac.UserPrincipal("document-user-a"), conflicting); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed create replay error = %v", err)
	}
}

func TestDocumentsPostgresSourceCASSerializesWithReplyCreation(t *testing.T) {
	db := newDocumentsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	seedDocumentsFixture(t, ctx, db)
	documentService := New(db)
	source, err := documentService.Source(ctx, rbac.UserPrincipal("document-user-a"), "document-root")
	if err != nil {
		t.Fatal(err)
	}

	blocker, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if err := lockSourceThread(ctx, blocker, "document-root"); err != nil {
		t.Fatal(err)
	}

	type postResult struct {
		post *posts.Post
		err  error
	}
	replyDone := make(chan postResult, 1)
	go func() {
		post, createErr := posts.New(db).Create(ctx, "document-channel", "document-user-b", "document-root", "잠금 중 추가된 답글", nil, nil)
		replyDone <- postResult{post: post, err: createErr}
	}()
	waitForDocumentAdvisoryWaiters(t, ctx, db, 1)

	type createDocumentResult struct {
		document *Document
		replayed bool
		err      error
	}
	documentDone := make(chan createDocumentResult, 1)
	go func() {
		document, replayed, createErr := documentService.Create(ctx, rbac.UserPrincipal("document-user-a"), CreateInput{
			Title: "동시성 문서", Body: "답글 전 커서로 생성 시도", SourcePostID: "document-root",
			SourceCursorAt: source.CursorAt, IdempotencyKey: "concurrent-source-cas",
		})
		documentDone <- createDocumentResult{document: document, replayed: replayed, err: createErr}
	}()
	waitForDocumentAdvisoryWaiters(t, ctx, db, 2)
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	replyResult := <-replyDone
	if replyResult.err != nil || replyResult.post == nil || replyResult.post.RootID != "document-root" {
		t.Fatalf("concurrent reply = %#v, %v", replyResult.post, replyResult.err)
	}
	createResult := <-documentDone
	if !errors.Is(createResult.err, ErrSourceChanged) || createResult.document != nil || createResult.replayed {
		t.Fatalf("document CAS result = %#v, replayed=%v, err=%v", createResult.document, createResult.replayed, createResult.err)
	}
	var count int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM documents WHERE idempotency_key='concurrent-source-cas'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("document committed across source change: count=%d, err=%v", count, err)
	}
}

func TestPostsPostgresSourceMutationsUseDocumentThreadLock(t *testing.T) {
	db := newDocumentsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	seedDocumentsFixture(t, ctx, db)
	postService := posts.New(db)

	operations := []struct {
		name string
		run  func(context.Context) error
	}{
		{
			name: "edit",
			run: func(operationContext context.Context) error {
				updated, err := postService.Update(operationContext, "document-reply", "document-user-b", "잠금 뒤 수정", nil)
				if err == nil && updated == nil {
					return errors.New("edit did not update reply")
				}
				return err
			},
		},
		{
			name: "delete",
			run: func(operationContext context.Context) error {
				updated, err := postService.Delete(operationContext, "document-reply", "document-user-b")
				if err == nil && !updated {
					return errors.New("delete did not update reply")
				}
				return err
			},
		},
		{
			name: "restore",
			run: func(operationContext context.Context) error {
				updated, err := postService.Restore(operationContext, "document-reply")
				if err == nil && !updated {
					return errors.New("restore did not update reply")
				}
				return err
			},
		},
		{
			name: "move",
			run: func(operationContext context.Context) error {
				// A reply identifier must still canonicalize to and move the full thread.
				updated, err := postService.MoveThread(operationContext, "document-reply", "other-document-channel")
				if err == nil && updated != 2 {
					return fmt.Errorf("move updated %d posts, want 2", updated)
				}
				return err
			},
		},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			blocker, err := db.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if err := lockSourceThread(ctx, blocker, "document-root"); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- operation.run(ctx) }()
			waitForDocumentAdvisoryWaiters(t, ctx, db, 1)
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func waitForDocumentAdvisoryWaiters(t *testing.T, ctx context.Context, db *store.DB, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var applicationName string
		if err := db.Pool.QueryRow(ctx, `SHOW application_name`).Scan(&applicationName); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := db.Pool.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM pg_stat_activity
			WHERE application_name=$1 AND wait_event_type='Lock' AND wait_event='advisory'
		`, applicationName).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d source-thread advisory lock waiters", want)
}

func newDocumentsTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(documentsTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", documentsTestPostgresDSN)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	schemaName := "moyro_documents_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create schema: %v", err)
	}
	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE")
		adminPool.Close()
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.ConnConfig.RuntimeParams["application_name"] = schemaName
	config.MaxConns = 4
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedDocumentsFixture(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id,username,email,password_hash,roles,create_at,update_at) VALUES
			('document-user-a','document-user-a','document-a@example.test','hash','system_user',1,1),
			('document-user-b','document-user-b','document-b@example.test','hash','system_user',1,1),
			('document-user-c','document-user-c','document-c@example.test','hash','system_user',1,1);
		INSERT INTO teams (id,name,display_name,type,create_at,update_at) VALUES
			('document-team','document-team','Document Team','O',1,1),
			('other-document-team','other-document-team','Other Team','O',1,1);
		INSERT INTO team_members (team_id,user_id,roles,create_at) VALUES
			('document-team','document-user-a','team_user',1),
			('document-team','document-user-b','team_user',1),
			('other-document-team','document-user-a','team_user',1);
		INSERT INTO channels (id,team_id,type,display_name,name,create_at,update_at) VALUES
			('document-channel','document-team','P','Document Channel','document-channel',1,1),
			('other-document-channel','other-document-team','P','Other Channel','other-document-channel',1,1);
		INSERT INTO channel_members (channel_id,user_id,roles,create_at) VALUES
			('document-channel','document-user-a','channel_user',1),
			('document-channel','document-user-b','channel_user',1),
			('other-document-channel','document-user-a','channel_user',1);
		INSERT INTO posts (id,channel_id,user_id,root_id,message,create_at,update_at) VALUES
			('document-root','document-channel','document-user-a','','장애 대응 절차',1,100),
			('document-reply','document-channel','document-user-b','document-root','복구 순서를 확정',2,2),
			('other-document-root','other-document-channel','document-user-a','','비공개 다른 문서',3,3);
	`); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
}
