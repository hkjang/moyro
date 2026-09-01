package knowledge

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const knowledgeTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

func TestKnowledgePostgresAuthorizationAndStableSources(t *testing.T) {
	db := newKnowledgeTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	seedKnowledgeFixture(t, ctx, db)
	service := New(db)

	search := SearchInput{Query: "runbook", TeamID: "knowledge-team", Limit: 20}
	result, err := service.Search(ctx, rbac.UserPrincipal("knowledge-user-a"), search)
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalHits != 2 || len(result.Sources) != 2 {
		t.Fatalf("search result = %#v", result)
	}
	var message, document *Source
	for index := range result.Sources {
		source := &result.Sources[index]
		switch source.Kind {
		case "message":
			message = source
		case "document":
			document = source
		default:
			t.Fatalf("unexpected source kind %q", source.Kind)
		}
	}
	if message == nil || message.Ref != "M1" || message.ID != "knowledge-post" || message.PostID != message.ID || message.DocumentID != "" {
		t.Fatalf("message source = %#v", message)
	}
	if document == nil || document.Ref != "D1" || document.ID != "knowledge-document" || document.DocumentID != document.ID || document.PostID != "" {
		t.Fatalf("document source = %#v", document)
	}
	second, err := service.Search(ctx, rbac.UserPrincipal("knowledge-user-a"), search)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, second) {
		t.Fatalf("source identifiers are not stable:\nfirst=%#v\nsecond=%#v", result, second)
	}

	// Team membership alone never grants access to a channel's search rows.
	withoutChannel, err := service.Search(ctx, rbac.UserPrincipal("knowledge-user-b"), search)
	if err != nil || withoutChannel.TotalHits != 0 || len(withoutChannel.Sources) != 0 {
		t.Fatalf("team-only member result = %#v, %v", withoutChannel, err)
	}
	deniedPrincipal := rbac.Principal{
		UserID: "knowledge-user-a", Restricted: true,
		AllowedTeamIDs: map[string]struct{}{"knowledge-team": {}},
		AllowedChannelIDs: map[string]struct{}{
			"knowledge-hidden-channel": {},
		},
	}
	if _, err := service.Search(ctx, deniedPrincipal, SearchInput{
		Query: "runbook", TeamID: "knowledge-team", ChannelID: "knowledge-channel",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("out-of-scope search error = %v", err)
	}

	// Authorization is re-evaluated for every request, including after a
	// membership has been revoked.
	if _, err := db.Pool.Exec(ctx, `
		DELETE FROM channel_members
		WHERE channel_id='knowledge-channel' AND user_id='knowledge-user-a'
	`); err != nil {
		t.Fatal(err)
	}
	revoked, err := service.Search(ctx, rbac.UserPrincipal("knowledge-user-a"), search)
	if err != nil || revoked.TotalHits != 0 || len(revoked.Sources) != 0 {
		t.Fatalf("revoked member result = %#v, %v", revoked, err)
	}
}

func newKnowledgeTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(knowledgeTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", knowledgeTestPostgresDSN)
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
	schemaName := "moyro_knowledge_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

func seedKnowledgeFixture(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id,username,email,password_hash,roles,create_at,update_at) VALUES
			('knowledge-user-a','knowledge-user-a','knowledge-a@example.test','hash','system_user',1,1),
			('knowledge-user-b','knowledge-user-b','knowledge-b@example.test','hash','system_user',1,1);
		INSERT INTO teams (id,name,display_name,type,create_at,update_at) VALUES
			('knowledge-team','knowledge-team','Knowledge Team','O',1,1);
		INSERT INTO team_members (team_id,user_id,roles,create_at) VALUES
			('knowledge-team','knowledge-user-a','team_user',1),
			('knowledge-team','knowledge-user-b','team_user',1);
		INSERT INTO channels (id,team_id,type,display_name,name,create_at,update_at) VALUES
			('knowledge-channel','knowledge-team','P','Knowledge Channel','knowledge-channel',1,1),
			('knowledge-hidden-channel','knowledge-team','P','Hidden Channel','knowledge-hidden-channel',1,1);
		INSERT INTO channel_members (channel_id,user_id,roles,create_at) VALUES
			('knowledge-channel','knowledge-user-a','channel_user',1);
		INSERT INTO posts (id,channel_id,user_id,root_id,message,create_at,update_at) VALUES
			('knowledge-post','knowledge-channel','knowledge-user-a','','offline runbook answer',2,2),
			('knowledge-hidden-post','knowledge-hidden-channel','knowledge-user-a','','secret runbook content',3,3),
			('knowledge-deleted-post','knowledge-channel','knowledge-user-a','','deleted runbook content',3,3);
		UPDATE posts SET delete_at=4 WHERE id='knowledge-deleted-post';
		INSERT INTO documents (
			id,title,body,created_by,team_id,channel_id,source_thread_id,
			source_cursor_at,idempotency_key,revision,create_at,update_at,delete_at
		) VALUES
			('knowledge-document','Runbook document','verified runbook procedure','knowledge-user-a',
			 'knowledge-team','knowledge-channel','knowledge-post',2,'knowledge-key-1',1,5,5,0),
			('knowledge-hidden-document','Secret runbook','hidden runbook procedure','knowledge-user-a',
			 'knowledge-team','knowledge-hidden-channel','knowledge-hidden-post',3,'knowledge-key-2',1,5,5,0);
		INSERT INTO document_sources (
			document_id,post_id,position,captured_update_at,captured_content_digest
		) VALUES
			('knowledge-document','knowledge-post',0,2,md5('offline runbook answer')),
			('knowledge-hidden-document','knowledge-hidden-post',0,3,md5('secret runbook content'));
	`); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
}
