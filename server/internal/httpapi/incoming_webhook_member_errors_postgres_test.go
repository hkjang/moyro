package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/application/postcommand"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/hkjang/moyro/server/internal/webhooks"
)

// fireIncomingWebhook re-asks a question the post command is about to ask
// again. Before handing the payload to webhooks.Fire it calls
// channels.IsMember(hook.ChannelID, hook.CreatorID); Fire -> postcommand.Execute
// -> authorize then resolves the very same (ActorID=hook.CreatorID,
// ChannelID=hook.ChannelID) pair. The two readings used to disagree about what
// a database fault means: the pre-check folded err into the same 403
// api.webhook.incoming.fire.creator_not_member it gives a genuinely removed
// creator, while the Fire branch right below it already reports
// FailurePermissionCheck/FailureMembershipCheck as
// 500 api.webhook.incoming.fire.permission_check.
//
// 403 is the wrong answer for a dropped connection or a broken
// channel_members table: it tells an integration sender "this hook is finished"
// — a permanent verdict no sender retries — so every alert raised during a
// database blip is dropped on the floor rather than redelivered.
//
// This pins both halves through the production wiring: the real rbac, channels
// and posts services, the AuthorizeCreate closure copied from router.go, the
// real webhooks.IncomingService (no substitute executor), and the chi pattern
// copied from router.go. The fault case must be 500 under the id the handler
// already owns, and a creator who really did leave the channel must keep the
// 403 and its exact message.
func TestIncomingWebhookMemberCheckFaultIsNotForbidden(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	// user-maker created both hooks and belongs to chan-alpha. user-exile also
	// created a hook but was removed from the channel afterwards, which is the
	// case the 403 exists for.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES ('user-maker', 'user-maker', 'maker@example.test', 'hash', 'system_user', 1, 1),
		       ('user-exile', 'user-exile', 'exile@example.test', 'hash', 'system_user', 1, 1);
		INSERT INTO teams (id, name, display_name, type, create_at, update_at)
		VALUES ('team-main', 'team-main', 'Main Team', 'O', 1, 1);
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES ('team-main', 'user-maker', 'team_user', 1),
		       ('team-main', 'user-exile', 'team_user', 1);
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES ('chan-alpha', 'team-main', 'O', 'Alpha', 'alpha', 1, 1);
		INSERT INTO channel_members (channel_id, user_id, roles, create_at)
		VALUES ('chan-alpha', 'user-maker', 'channel_user', 1)
	`); err != nil {
		t.Fatalf("seed incoming webhook fixture: %v", err)
	}

	channelSvc := channels.New(db)
	postSvc := posts.New(db)
	rbacService, rbacServiceErr := rbac.NewPostgres(db.Pool)
	if rbacServiceErr != nil {
		t.Fatalf("rbac service: %v", rbacServiceErr)
	}
	// Copied from router.go:198-216 so the post command answers the membership
	// question exactly the way it does in production.
	postCommandSvc := postcommand.New(postcommand.Dependencies{
		Channels: channelSvc,
		Posts:    postSvc,
		AuthorizeCreate: func(ctx context.Context, actorID, channelID string) (postcommand.CreateAuthorization, error) {
			if rbacServiceErr != nil {
				return postcommand.CreateAuthorization{}, rbacServiceErr
			}
			preconditions, err := channelSvc.ReadPostPreconditions(ctx, channelID, actorID)
			if err != nil {
				return postcommand.CreateAuthorization{}, err
			}
			channel := preconditions.Channel
			if channel == nil || channel.DeleteAt != 0 || !preconditions.UserLive {
				return postcommand.CreateAuthorization{}, nil
			}
			allowed, err := rbacService.Allowed(ctx, rbac.UserPrincipal(actorID), rbac.PermissionCreatePost, rbac.Scope{
				TeamID: channel.TeamID, ChannelID: channel.ID,
			})
			if err != nil {
				return postcommand.CreateAuthorization{}, err
			}
			return postcommand.CreateAuthorization{Allowed: allowed, IsMember: preconditions.IsMember}, nil
		},
		Logger: slog.Default(),
	})
	incomingSvc := webhooks.NewIncoming(db, postCommandSvc)

	liveHook, err := incomingSvc.Create(ctx, "user-maker", "chan-alpha", "team-main", "Alerts", "alerts", "", true)
	if err != nil {
		t.Fatalf("create live hook: %v", err)
	}
	exileHook, err := incomingSvc.Create(ctx, "user-exile", "chan-alpha", "team-main", "Exiled", "exiled", "", true)
	if err != nil {
		t.Fatalf("create exile hook: %v", err)
	}

	h := &handlers{
		channels:     channelSvc,
		posts:        postSvc,
		postCommands: postCommandSvc,
		incoming:     incomingSvc,
		logger:       slog.Default(),
	}
	router := chi.NewRouter()
	// Pattern copied character for character from router.go:372. This route
	// lives outside /api/v4 and outside the auth chain, so no token is sent.
	router.Post("/hooks/{hookID}", h.fireIncomingWebhook)

	fire := func(hookID, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/hooks/"+hookID, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}
	decode := func(t *testing.T, recorder *httptest.ResponseRecorder) apiError {
		t.Helper()
		var payload apiError
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode error body %q: %v", recorder.Body.String(), err)
		}
		return payload
	}

	// Ordered deliberately: everything that needs channel_members intact runs
	// before the fault injection drops it.
	t.Run("a live hook still posts", func(t *testing.T) {
		recorder := fire(liveHook.ID, `{"text":"deploy finished"}`)
		if recorder.Code != 200 || strings.TrimSpace(recorder.Body.String()) != `{"status":"OK"}` {
			t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, strings.TrimSpace(recorder.Body.String()))
		}
		var stored int
		if err := db.Pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM posts WHERE channel_id='chan-alpha' AND user_id='user-maker' AND message='deploy finished'
		`).Scan(&stored); err != nil {
			t.Fatalf("count stored posts: %v", err)
		}
		if stored != 1 {
			t.Fatalf("stored posts = %d, want 1", stored)
		}
	})

	t.Run("a creator who left the channel is still refused", func(t *testing.T) {
		recorder := fire(exileHook.ID, `{"text":"from an exiled creator"}`)
		if recorder.Code != 403 {
			t.Fatalf("status = %d, want 403 (body %s)", recorder.Code, strings.TrimSpace(recorder.Body.String()))
		}
		body := decode(t, recorder)
		if body.ID != "api.webhook.incoming.fire.creator_not_member" || body.Message != "hook creator no longer a channel member" {
			t.Fatalf("body = %#v, want the unchanged creator_not_member refusal", body)
		}
	})

	t.Run("an unknown hook is still not found", func(t *testing.T) {
		recorder := fire("no-such-hook", `{"text":"nowhere"}`)
		if recorder.Code != 404 || decode(t, recorder).ID != "api.webhook.incoming.fire.not_found" {
			t.Fatalf("status = %d, want 404 not_found (body %s)", recorder.Code, strings.TrimSpace(recorder.Body.String()))
		}
	})

	// The hook row lives in incoming_webhooks, so h.incoming.Get keeps
	// succeeding and the request reaches the membership branch rather than
	// stopping at 404. The pre-check is the first thing to touch
	// channel_members, so Fire never runs — the fault is deterministic.
	if _, err := db.Pool.Exec(ctx, `DROP TABLE channel_members`); err != nil {
		t.Fatalf("drop channel_members: %v", err)
	}

	t.Run("a membership lookup fault reports a server error", func(t *testing.T) {
		recorder := fire(liveHook.ID, `{"text":"during the outage"}`)
		if recorder.Code != 500 {
			t.Fatalf("status = %d, want 500 (body %s)", recorder.Code, strings.TrimSpace(recorder.Body.String()))
		}
		if body := decode(t, recorder); body.ID != "api.webhook.incoming.fire.permission_check" {
			t.Fatalf("error id = %q, want api.webhook.incoming.fire.permission_check", body.ID)
		}
		var stored int
		if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM posts WHERE message='during the outage'`).Scan(&stored); err != nil {
			t.Fatalf("count stored posts: %v", err)
		}
		if stored != 0 {
			t.Fatalf("stored posts = %d, want none — the refused request must not write", stored)
		}
	})

	t.Run("an empty body is still a bad request", func(t *testing.T) {
		recorder := fire(liveHook.ID, ``)
		if recorder.Code != 400 || decode(t, recorder).ID != "api.webhook.incoming.fire.invalid_body" {
			t.Fatalf("status = %d, want 400 invalid_body (body %s)", recorder.Code, strings.TrimSpace(recorder.Body.String()))
		}
	})
}
