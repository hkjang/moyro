package postcommand

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/auth"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/ws"
)

type fakeChannels struct {
	member        bool
	memberErr     error
	counters      []channels.Counter
	bumpedChannel string
	bumpedAuthor  string
	bumpedIDs     []string
}

func (f *fakeChannels) IsMember(context.Context, string, string) (bool, error) {
	return f.member, f.memberErr
}

func (f *fakeChannels) BumpUnread(_ context.Context, channelID, authorID string, mentionedIDs []string) ([]channels.Counter, error) {
	f.bumpedChannel = channelID
	f.bumpedAuthor = authorID
	f.bumpedIDs = append([]string(nil), mentionedIDs...)
	return f.counters, nil
}

type createCall struct {
	channelID string
	userID    string
	rootID    string
	message   string
	props     map[string]any
	fileIDs   []string
}

type fakePosts struct {
	root             *posts.Post
	getErr           error
	createErr        error
	created          createCall
	scheduledPostID  string
	updatedFileIDs   []string
	updatedFilePost  string
	linkMetadata     []posts.LinkPreview
	linkMetadataPost string
}

func (f *fakePosts) CreateScheduled(ctx context.Context, scheduledPostID, channelID, userID, rootID, message string, props map[string]any, fileIDs []string) (*posts.Post, error) {
	f.scheduledPostID = scheduledPostID
	return f.Create(ctx, channelID, userID, rootID, message, props, fileIDs)
}

func (f *fakePosts) Get(_ context.Context, id string) (*posts.Post, error) {
	if f.root != nil && (f.root.ID == "" || f.root.ID == id) {
		return f.root, f.getErr
	}
	return nil, f.getErr
}

func (f *fakePosts) Create(_ context.Context, channelID, userID, rootID, message string, props map[string]any, fileIDs []string) (*posts.Post, error) {
	f.created = createCall{
		channelID: channelID, userID: userID, rootID: rootID, message: message,
		props: cloneMap(props), fileIDs: append([]string(nil), fileIDs...),
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &posts.Post{
		ID: "post-1", ChannelID: channelID, UserID: userID, RootID: rootID,
		Message: message, Props: cloneMap(props), FileIDs: append([]string(nil), fileIDs...),
		CreateAt: 100, UpdateAt: 100, LinkMetadata: []posts.LinkPreview{},
	}, nil
}

func (f *fakePosts) UpdateFileIDs(_ context.Context, postID string, fileIDs []string) error {
	f.updatedFilePost = postID
	f.updatedFileIDs = append([]string(nil), fileIDs...)
	return nil
}

func (f *fakePosts) UpdateLinkMetadata(_ context.Context, postID string, previews []posts.LinkPreview) error {
	f.linkMetadataPost = postID
	f.linkMetadata = append([]posts.LinkPreview(nil), previews...)
	return nil
}

type fakeFiles struct {
	attached []string
	ownerID  string
	fileIDs  []string
	postID   string
	channel  string
}

func (f *fakeFiles) AssociateWithPost(_ context.Context, ownerID string, fileIDs []string, postID, channelID string) ([]string, error) {
	f.ownerID = ownerID
	f.fileIDs = append([]string(nil), fileIDs...)
	f.postID = postID
	f.channel = channelID
	return append([]string(nil), f.attached...), nil
}

type fakeUsers struct {
	resolved       map[string]string
	resolvedNames  []string
	authorUsername string
}

func (f *fakeUsers) UserIDsByUsernames(_ context.Context, names []string) (map[string]string, error) {
	f.resolvedNames = append([]string(nil), names...)
	return f.resolved, nil
}

func (f *fakeUsers) UserByID(context.Context, string) (*auth.User, error) {
	return &auth.User{Username: f.authorUsername}, nil
}

type fakeBots struct{ bot bool }

func (f *fakeBots) IsBot(context.Context, string) (bool, error) { return f.bot, nil }

type fakePlugins struct {
	modified  []byte
	rejected  bool
	reason    string
	willInput []byte
	hasInput  chan []byte
}

func (f *fakePlugins) MessageWillBePosted(_ context.Context, post []byte) ([]byte, bool, string) {
	f.willInput = append([]byte(nil), post...)
	return f.modified, f.rejected, f.reason
}

func (f *fakePlugins) MessageHasBeenPosted(_ context.Context, post []byte) {
	if f.hasInput != nil {
		f.hasInput <- append([]byte(nil), post...)
	}
}

type fakeEvents struct {
	mu     sync.Mutex
	events []ws.Event
}

func (f *fakeEvents) Broadcast(event ws.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
}

func (f *fakeEvents) snapshot() []ws.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ws.Event(nil), f.events...)
}

type fakeOutgoing struct {
	post     *posts.Post
	username string
}

type auditCall struct {
	actorID string
	action  string
	target  string
	payload map[string]any
}

type fakeAudit struct{ calls []auditCall }

func (f *fakeAudit) LogAsync(actorID, action, target string, payload map[string]any) {
	f.calls = append(f.calls, auditCall{
		actorID: actorID,
		action:  action,
		target:  target,
		payload: cloneMap(payload),
	})
}

func (f *fakeOutgoing) Dispatch(_ context.Context, post *posts.Post, username string) {
	f.post = post
	f.username = username
}

func TestExecutePreservesRESTLifecycle(t *testing.T) {
	channelSvc := &fakeChannels{
		member: true,
		counters: []channels.Counter{
			{UserID: "user-alice", MsgCount: 2, MentionCount: 1, Desktop: "all"},
			{UserID: "user-other", MsgCount: 4, MentionCount: 0, Desktop: "mentions"},
		},
	}
	postStore := &fakePosts{}
	fileSvc := &fakeFiles{attached: []string{"file-ok"}}
	userSvc := &fakeUsers{
		resolved:       map[string]string{"alice": "user-alice", "bob": "user-bob"},
		authorUsername: "author",
	}
	pluginOutput, err := json.Marshal(posts.Post{
		Message: "changed @alice @alice @bob",
		Props: map[string]any{
			"plugin":               "kept",
			"approval_request_id":  "plugin-spoof",
			"scheduled_post_id":    "plugin-spoof",
			"webhook_depth":        99,
			"_moyro_credential_id": "plugin-spoof",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	pluginSvc := &fakePlugins{modified: pluginOutput, hasInput: make(chan []byte, 1)}
	events := &fakeEvents{}
	outgoing := &fakeOutgoing{}
	auditLog := &fakeAudit{}
	metricCount := 0
	service := New(Dependencies{
		Channels: channelSvc, Posts: postStore, Files: fileSvc, Users: userSvc,
		Bots: &fakeBots{}, Plugins: pluginSvc, Events: events, Outgoing: outgoing, Audit: auditLog,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		IncrementPostCount: func() { metricCount++ },
	})

	post, err := service.Execute(context.Background(), Command{
		Source: SourceREST, ActorID: "author-id", ChannelID: "channel-1",
		Message: "original", FileIDs: []string{"file-ok", "file-foreign"},
		CredentialID: "session-1",
		Props: map[string]any{
			"client":              "kept before plugin replacement",
			"approval_request_id": "client-spoof",
			"from_mcp":            true,
			"webhook_depth":       99,
			"_moyro_private":      "client-spoof",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if post.Message != "changed @alice @alice @bob" {
		t.Fatalf("post message = %q", post.Message)
	}
	if want := map[string]any{"plugin": "kept"}; !reflect.DeepEqual(post.Props, want) {
		t.Fatalf("post props = %#v, want %#v", post.Props, want)
	}
	if !reflect.DeepEqual(post.FileIDs, []string{"file-ok"}) {
		t.Fatalf("post file ids = %#v", post.FileIDs)
	}
	if metricCount != 1 {
		t.Fatalf("post metric count = %d, want 1", metricCount)
	}
	if postStore.created.message != post.Message || !reflect.DeepEqual(postStore.created.props, post.Props) {
		t.Fatalf("create call = %#v", postStore.created)
	}
	if !reflect.DeepEqual(postStore.updatedFileIDs, []string{"file-ok"}) || postStore.updatedFilePost != post.ID {
		t.Fatalf("updated files = %q %#v", postStore.updatedFilePost, postStore.updatedFileIDs)
	}
	if fileSvc.ownerID != "author-id" || fileSvc.postID != post.ID || fileSvc.channel != "channel-1" {
		t.Fatalf("file association = %#v", fileSvc)
	}
	if !reflect.DeepEqual(userSvc.resolvedNames, []string{"alice", "bob"}) {
		t.Fatalf("resolved names = %#v", userSvc.resolvedNames)
	}
	if !reflect.DeepEqual(channelSvc.bumpedIDs, []string{"user-alice", "user-bob"}) {
		t.Fatalf("bumped mention ids = %#v", channelSvc.bumpedIDs)
	}
	if outgoing.post != post || outgoing.username != "author" {
		t.Fatalf("outgoing dispatch = %#v, %q", outgoing.post, outgoing.username)
	}
	if want := []auditCall{{
		actorID: "author-id", action: "post.create", target: post.ID,
		payload: map[string]any{"source": "rest", "channel_id": "channel-1", "credential_id": "session-1"},
	}}; !reflect.DeepEqual(auditLog.calls, want) {
		t.Fatalf("audit calls = %#v, want %#v", auditLog.calls, want)
	}

	var provisional posts.Post
	if err := json.Unmarshal(pluginSvc.willInput, &provisional); err != nil {
		t.Fatal(err)
	}
	if provisional.FileIDs != nil {
		t.Fatalf("provisional file ids = %#v, want nil for existing hook contract", provisional.FileIDs)
	}
	if _, exists := provisional.Props["approval_request_id"]; exists {
		t.Fatal("untrusted approval_request_id reached the pre-post hook")
	}
	if _, exists := provisional.Props["_moyro_private"]; exists {
		t.Fatal("untrusted _moyro_ prop reached the pre-post hook")
	}
	if _, exists := provisional.Props["from_mcp"]; exists {
		t.Fatal("untrusted MCP provenance reached the pre-post hook")
	}

	select {
	case raw := <-pluginSvc.hasInput:
		var hooked posts.Post
		if err := json.Unmarshal(raw, &hooked); err != nil {
			t.Fatal(err)
		}
		if hooked.ID != post.ID || !reflect.DeepEqual(hooked.FileIDs, []string{"file-ok"}) {
			t.Fatalf("post-hook payload = %#v", hooked)
		}
	case <-time.After(time.Second):
		t.Fatal("post-persist hook was not called")
	}

	gotEvents := events.snapshot()
	if len(gotEvents) != 5 {
		t.Fatalf("events = %#v, want posted + 2 mentions + 2 unread updates", gotEvents)
	}
	if gotEvents[0].Event != "posted" || gotEvents[0].Broadcast.ChannelID != post.ChannelID || gotEvents[0].Data["mentions"] != `["user-alice","user-bob"]` {
		t.Fatalf("posted event = %#v", gotEvents[0])
	}
	if gotEvents[1].Event != "mention" || gotEvents[1].Broadcast.UserID != "user-alice" {
		t.Fatalf("first mention event = %#v", gotEvents[1])
	}
	if gotEvents[3].Event != "unread_updated" || gotEvents[3].Data["is_mention"] != true {
		t.Fatalf("mentioned unread event = %#v", gotEvents[3])
	}
	if gotEvents[4].Event != "unread_updated" || gotEvents[4].Data["is_mention"] != false {
		t.Fatalf("ordinary unread event = %#v", gotEvents[4])
	}
}

func TestExecuteAppliesTrustedMCPMetadataAfterPluginHooks(t *testing.T) {
	pluginOutput, err := json.Marshal(posts.Post{
		Message: "plugin changed",
		Props: map[string]any{
			"plugin":              "kept",
			"from_mcp":            false,
			"approval_request_id": "plugin-spoof",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	pluginSvc := &fakePlugins{modified: pluginOutput}
	postStore := &fakePosts{}
	service := New(Dependencies{
		Channels: &fakeChannels{member: true}, Posts: postStore, Plugins: pluginSvc,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	post, err := service.Execute(context.Background(), Command{
		Source: SourceMCP, ActorID: "user-1", ChannelID: "channel-1", Message: "original",
		ApprovalRequestID: " approval-1 ",
		Props: map[string]any{
			"client":              "kept before plugin replacement",
			"from_mcp":            false,
			"approval_request_id": "client-spoof",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	wantProps := map[string]any{
		"plugin": "kept", "from_mcp": true, "approval_request_id": "approval-1",
	}
	if !reflect.DeepEqual(post.Props, wantProps) {
		t.Fatalf("post props = %#v, want %#v", post.Props, wantProps)
	}
	if !reflect.DeepEqual(postStore.created.props, wantProps) {
		t.Fatalf("create props = %#v, want %#v", postStore.created.props, wantProps)
	}
	var provisional posts.Post
	if err := json.Unmarshal(pluginSvc.willInput, &provisional); err != nil {
		t.Fatal(err)
	}
	if provisional.Props["from_mcp"] != true || provisional.Props["approval_request_id"] != "approval-1" {
		t.Fatalf("trusted provisional props = %#v", provisional.Props)
	}
}

func TestExecuteUsesScheduledPersistenceMetadata(t *testing.T) {
	postStore := &fakePosts{}
	service := New(Dependencies{
		Channels: &fakeChannels{member: true}, Posts: postStore,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	post, err := service.Execute(context.Background(), Command{
		Source: SourceScheduled, ActorID: "user-1", ChannelID: "channel-1",
		Message: "scheduled", ScheduledPostID: " scheduled-1 ", FileIDs: []string{"file-1"},
		Props: map[string]any{"scheduled_post_id": "client-spoof", "kept": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if postStore.scheduledPostID != "scheduled-1" {
		t.Fatalf("scheduled post id = %q", postStore.scheduledPostID)
	}
	if post.Props["kept"] != true {
		t.Fatalf("post props = %#v", post.Props)
	}
	if _, exists := post.Props["scheduled_post_id"]; exists {
		t.Fatalf("scheduled id leaked into user props: %#v", post.Props)
	}
}

func TestExecutePreservesIncomingWebhookPresentationMetadata(t *testing.T) {
	pluginOutput, err := json.Marshal(posts.Post{
		Message: "plugin changed",
		Props: map[string]any{
			"from_webhook": "false", "override_username": "spoof", "override_icon_url": "spoof",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := &fakeEvents{}
	service := New(Dependencies{
		Channels: &fakeChannels{member: true}, Posts: &fakePosts{},
		Plugins: &fakePlugins{modified: pluginOutput}, Events: events,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	post, err := service.Execute(context.Background(), Command{
		Source: SourceIncomingWebhook, ActorID: "hook-owner", ChannelID: "channel-1",
		Message: "incoming", OverrideUsername: "Build Bot", OverrideIconURL: "https://icons.test/bot.png",
		SenderName: "configured-hook-name", WebhookDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantProps := map[string]any{
		"from_webhook": "true", "override_username": "Build Bot", "override_icon_url": "https://icons.test/bot.png",
		"webhook_depth": 2,
	}
	if !reflect.DeepEqual(post.Props, wantProps) {
		t.Fatalf("post props = %#v, want %#v", post.Props, wantProps)
	}
	posted := events.snapshot()
	if len(posted) != 1 || posted[0].Event != "posted" ||
		posted[0].Data["sender_name"] != "configured-hook-name" || posted[0].Data["from_webhook"] != "true" {
		t.Fatalf("incoming posted event = %#v", posted)
	}
}

func TestExecutePreservesMeCommandMetadata(t *testing.T) {
	service := New(Dependencies{
		Channels: &fakeChannels{member: true}, Posts: &fakePosts{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	post, err := service.Execute(context.Background(), Command{
		Source: SourceSlashCommand, SlashCommand: "me", ActorID: "user-1",
		ChannelID: "channel-1", Message: "*waves*",
		Props: map[string]any{"from_me_command": false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if post.Props["from_me_command"] != true {
		t.Fatalf("post props = %#v", post.Props)
	}
}

func TestExecuteEnforcesCreatePermissionBeforeMembership(t *testing.T) {
	permissionErr := errors.New("permission store unavailable")
	tests := []struct {
		name     string
		allowed  bool
		err      error
		wantCode FailureCode
		wantText string
	}{
		{name: "check failure", err: permissionErr, wantCode: FailurePermissionCheck, wantText: permissionErr.Error()},
		{name: "denied", wantCode: FailurePermissionDenied, wantText: "create_post permission is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			service := New(Dependencies{
				Channels: &fakeChannels{member: true}, Posts: &fakePosts{},
				AuthorizeCreate: func(_ context.Context, actorID, channelID string) (bool, error) {
					called = true
					if actorID != "user-1" || channelID != "channel-1" {
						t.Fatalf("authorization scope = (%q, %q)", actorID, channelID)
					}
					return test.allowed, test.err
				},
				Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			_, err := service.Execute(context.Background(), Command{
				ActorID: "user-1", ChannelID: "channel-1", Message: "blocked",
			})
			if !called || FailureCodeOf(err) != test.wantCode || err == nil || err.Error() != test.wantText {
				t.Fatalf("called=%v error=%v code=%q", called, err, FailureCodeOf(err))
			}
		})
	}
}

func TestExecuteContinuesAfterCreatePermissionAllowed(t *testing.T) {
	postStore := &fakePosts{}
	service := New(Dependencies{
		Channels: &fakeChannels{member: true}, Posts: postStore,
		AuthorizeCreate: func(context.Context, string, string) (bool, error) { return true, nil },
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	post, err := service.Execute(context.Background(), Command{
		ActorID: "user-1", ChannelID: "channel-1", Message: "allowed",
	})
	if err != nil || post == nil || postStore.created.message != "allowed" {
		t.Fatalf("post=%#v create=%#v error=%v", post, postStore.created, err)
	}
}

func TestExecuteClassifiesFailuresForTransportMapping(t *testing.T) {
	databaseError := errors.New("database unavailable")
	saveError := errors.New("insert failed")
	tests := []struct {
		name     string
		channels *fakeChannels
		posts    *fakePosts
		plugins  *fakePlugins
		command  Command
		wantCode FailureCode
		wantText string
	}{
		{
			name: "membership lookup", channels: &fakeChannels{memberErr: databaseError}, posts: &fakePosts{},
			command: Command{ActorID: "u", ChannelID: "c"}, wantCode: FailureMembershipCheck, wantText: databaseError.Error(),
		},
		{
			name: "not member", channels: &fakeChannels{}, posts: &fakePosts{},
			command: Command{ActorID: "u", ChannelID: "c"}, wantCode: FailureNotMember, wantText: "not a channel member",
		},
		{
			name: "invalid root", channels: &fakeChannels{member: true}, posts: &fakePosts{root: &posts.Post{ID: "root", ChannelID: "other"}},
			command: Command{ActorID: "u", ChannelID: "c", RootID: "root"}, wantCode: FailureInvalidRoot, wantText: posts.ErrInvalidRoot.Error(),
		},
		{
			name: "plugin rejection", channels: &fakeChannels{member: true}, posts: &fakePosts{}, plugins: &fakePlugins{rejected: true},
			command: Command{ActorID: "u", ChannelID: "c"}, wantCode: FailurePluginRejected, wantText: "post rejected by plugin",
		},
		{
			name: "persistence root race", channels: &fakeChannels{member: true}, posts: &fakePosts{createErr: posts.ErrInvalidRoot},
			command: Command{ActorID: "u", ChannelID: "c"}, wantCode: FailureInvalidRoot, wantText: posts.ErrInvalidRoot.Error(),
		},
		{
			name: "persistence failure", channels: &fakeChannels{member: true}, posts: &fakePosts{createErr: saveError},
			command: Command{ActorID: "u", ChannelID: "c"}, wantCode: FailureSave, wantText: saveError.Error(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies := Dependencies{
				Channels: test.channels, Posts: test.posts,
				Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			}
			if test.plugins != nil {
				dependencies.Plugins = test.plugins
			}
			service := New(dependencies)
			_, err := service.Execute(context.Background(), test.command)
			if err == nil {
				t.Fatal("Execute returned nil error")
			}
			if got := FailureCodeOf(err); got != test.wantCode {
				t.Fatalf("failure code = %q, want %q", got, test.wantCode)
			}
			if err.Error() != test.wantText {
				t.Fatalf("error = %q, want %q", err, test.wantText)
			}
		})
	}
}

func TestExtractMentionsPreservesFirstSeenOrder(t *testing.T) {
	got := ExtractMentions("@alice hi @bob, @alice and @build.bot")
	want := []string{"alice", "bob", "build.bot"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mentions = %#v, want %#v", got, want)
	}
	if got := ExtractMentions("no handles"); got != nil {
		t.Fatalf("no-match result = %#v, want nil", got)
	}
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
