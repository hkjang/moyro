package scheduled

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/application/postcommand"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/ws"
	"github.com/jackc/pgx/v5"
)

func TestWorkerDispatchCreatesAndFinalizesWithFreshContexts(t *testing.T) {
	sp := testScheduledPost()
	post := testDeliveredPost()
	store := &workerStoreStub{}
	dispatcher := &workerDispatcherStub{}
	commands := &workerCommandStub{}
	dispatcher.get = func(ctx context.Context, scheduledPostID string) (*posts.Post, error) {
		assertActiveContext(t, ctx)
		if scheduledPostID != sp.ID {
			t.Fatalf("lookup scheduled id = %q", scheduledPostID)
		}
		return nil, pgx.ErrNoRows
	}
	commands.execute = func(ctx context.Context, command postcommand.Command) (*posts.Post, error) {
		assertActiveContext(t, ctx)
		if command.Source != postcommand.SourceScheduled || command.ScheduledPostID != sp.ID ||
			command.ChannelID != sp.ChannelID || command.ActorID != sp.UserID ||
			command.RootID != sp.RootID || command.Message != sp.Message ||
			!reflect.DeepEqual(command.Props, sp.Props) || !reflect.DeepEqual(command.FileIDs, sp.FileIDs) {
			t.Fatalf("post command = %#v", command)
		}
		return post, nil
	}
	store.markSent = func(ctx context.Context, id, claimToken, resultPostID string, _ int64) error {
		assertActiveContext(t, ctx)
		if id != sp.ID || claimToken != sp.ClaimToken || resultPostID != post.ID {
			t.Fatalf("mark sent identity = (%q, %q, %q)", id, claimToken, resultPostID)
		}
		store.sent = true
		return nil
	}
	broadcaster := &workerBroadcasterStub{beforeBroadcast: func() {
		if !store.sent {
			t.Fatal("worker broadcast before claim finalization")
		}
	}}
	worker := NewWorker(store, dispatcher, commands, nil, broadcaster, nil)

	parent, cancel := context.WithCancel(context.Background())
	cancel()
	worker.dispatch(parent, sp)

	if commands.calls != 1 || store.markSentCalls != 1 || store.markFailedCalls != 0 {
		t.Fatalf("calls command=%d sent=%d failed=%d", commands.calls, store.markSentCalls, store.markFailedCalls)
	}
	if len(broadcaster.events) != 1 || broadcaster.events[0].Event != "scheduled_post_sent" {
		t.Fatalf("broadcast events = %#v", broadcaster.events)
	}
}

func TestWorkerDispatchUsesExistingScheduledPost(t *testing.T) {
	sp := testScheduledPost()
	post := testDeliveredPost()
	store := &workerStoreStub{}
	dispatcher := &workerDispatcherStub{
		get: func(context.Context, string) (*posts.Post, error) { return post, nil },
	}
	commands := &workerCommandStub{execute: func(context.Context, postcommand.Command) (*posts.Post, error) {
		t.Fatal("replay path created a duplicate post")
		return nil, nil
	}}
	worker := NewWorker(store, dispatcher, commands, nil, &workerBroadcasterStub{}, nil)

	worker.dispatch(context.Background(), sp)

	if commands.calls != 0 || store.markSentCalls != 1 || store.markFailedCalls != 0 {
		t.Fatalf("calls command=%d sent=%d failed=%d", commands.calls, store.markSentCalls, store.markFailedCalls)
	}
}

func TestWorkerDispatchResolvesUnknownCreateOutcome(t *testing.T) {
	sp := testScheduledPost()
	post := testDeliveredPost()
	lookupCalls := 0
	store := &workerStoreStub{}
	dispatcher := &workerDispatcherStub{
		get: func(context.Context, string) (*posts.Post, error) {
			lookupCalls++
			if lookupCalls == 1 {
				return nil, pgx.ErrNoRows
			}
			return post, nil
		},
	}
	commands := &workerCommandStub{execute: func(context.Context, postcommand.Command) (*posts.Post, error) {
		return nil, errors.New("commit response lost")
	}}
	worker := NewWorker(store, dispatcher, commands, nil, &workerBroadcasterStub{}, nil)

	worker.dispatch(context.Background(), sp)

	if lookupCalls != 2 || commands.calls != 1 || store.markSentCalls != 1 || store.markFailedCalls != 0 {
		t.Fatalf("calls lookup=%d command=%d sent=%d failed=%d", lookupCalls, commands.calls, store.markSentCalls, store.markFailedCalls)
	}
}

func TestWorkerReplayRepairsFileAssociations(t *testing.T) {
	sp := testScheduledPost()
	sp.FileIDs = []string{"file-1", "file-foreign"}
	post := testDeliveredPost()
	store := &workerStoreStub{}
	dispatcher := &workerDispatcherStub{
		get: func(context.Context, string) (*posts.Post, error) { return post, nil },
		update: func(ctx context.Context, postID string, fileIDs []string) error {
			assertActiveContext(t, ctx)
			if postID != post.ID || !reflect.DeepEqual(fileIDs, []string{"file-1"}) {
				t.Fatalf("updated files = %q %#v", postID, fileIDs)
			}
			return nil
		},
	}
	files := &workerFileStub{attached: []string{"file-1"}}
	commands := &workerCommandStub{execute: func(context.Context, postcommand.Command) (*posts.Post, error) {
		t.Fatal("replay path executed a duplicate command")
		return nil, nil
	}}
	worker := NewWorker(store, dispatcher, commands, files, &workerBroadcasterStub{}, nil)

	worker.dispatch(context.Background(), sp)

	if files.calls != 1 || files.userID != sp.UserID || files.postID != post.ID ||
		files.channelID != post.ChannelID || !reflect.DeepEqual(files.fileIDs, sp.FileIDs) {
		t.Fatalf("file association = %#v", files)
	}
	if !reflect.DeepEqual(post.FileIDs, []string{"file-1"}) {
		t.Fatalf("post file ids = %#v", post.FileIDs)
	}
	if store.markSentCalls != 1 || store.markFailedCalls != 0 {
		t.Fatalf("calls sent=%d failed=%d", store.markSentCalls, store.markFailedCalls)
	}
}

func TestWorkerReplayClearsStaleFileIDsWhenNoneAreAttachable(t *testing.T) {
	sp := testScheduledPost()
	sp.FileIDs = []string{"file-foreign"}
	post := testDeliveredPost()
	post.FileIDs = append([]string(nil), sp.FileIDs...)
	store := &workerStoreStub{}
	updated := false
	dispatcher := &workerDispatcherStub{
		get: func(context.Context, string) (*posts.Post, error) { return post, nil },
		update: func(_ context.Context, postID string, fileIDs []string) error {
			updated = true
			if postID != post.ID || len(fileIDs) != 0 {
				t.Fatalf("updated files = %q %#v", postID, fileIDs)
			}
			return nil
		},
	}
	worker := NewWorker(store, dispatcher, &workerCommandStub{}, &workerFileStub{}, &workerBroadcasterStub{}, nil)

	worker.dispatch(context.Background(), sp)

	if !updated || len(post.FileIDs) != 0 {
		t.Fatalf("updated=%v post file ids=%#v", updated, post.FileIDs)
	}
	if store.markSentCalls != 1 || store.markFailedCalls != 0 {
		t.Fatalf("calls sent=%d failed=%d", store.markSentCalls, store.markFailedCalls)
	}
}

func TestWorkerDispatchFailureUsesClaimCAS(t *testing.T) {
	sp := testScheduledPost()
	store := &workerStoreStub{}
	dispatcher := &workerDispatcherStub{
		get: func(context.Context, string) (*posts.Post, error) {
			return nil, errors.New("lookup unavailable")
		},
	}
	store.markFailed = func(ctx context.Context, id, claimToken, code, text string, attempts int, _ int64) error {
		assertActiveContext(t, ctx)
		if id != sp.ID || claimToken != sp.ClaimToken || code != "post_create_failed" || attempts != sp.AttemptCount {
			t.Fatalf("mark failed identity = (%q, %q, %q, %d)", id, claimToken, code, attempts)
		}
		if !strings.Contains(text, "lookup unavailable") {
			t.Fatalf("failure text = %q", text)
		}
		return nil
	}
	broadcaster := &workerBroadcasterStub{}
	worker := NewWorker(store, dispatcher, &workerCommandStub{}, nil, broadcaster, nil)

	parent, cancel := context.WithCancel(context.Background())
	cancel()
	worker.dispatch(parent, sp)

	if store.markFailedCalls != 1 || store.markSentCalls != 0 || len(broadcaster.events) != 0 {
		t.Fatalf("calls failed=%d sent=%d events=%d", store.markFailedCalls, store.markSentCalls, len(broadcaster.events))
	}
}

func TestWorkerDoesNotBroadcastStaleClaim(t *testing.T) {
	sp := testScheduledPost()
	store := &workerStoreStub{markSent: func(context.Context, string, string, string, int64) error {
		return ErrStaleClaim
	}}
	dispatcher := &workerDispatcherStub{
		get: func(context.Context, string) (*posts.Post, error) { return testDeliveredPost(), nil },
	}
	broadcaster := &workerBroadcasterStub{}
	worker := NewWorker(store, dispatcher, &workerCommandStub{}, nil, broadcaster, nil)

	worker.dispatch(context.Background(), sp)

	if len(broadcaster.events) != 0 {
		t.Fatalf("stale claim broadcast %d events", len(broadcaster.events))
	}
}

func TestRetryDelayIsBounded(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 0, want: 30 * time.Second},
		{attempt: 1, want: 30 * time.Second},
		{attempt: 2, want: time.Minute},
		{attempt: 3, want: 2 * time.Minute},
		{attempt: 20, want: maximumRetryDelay},
	}
	for _, test := range tests {
		if got := retryDelay(test.attempt); got != test.want {
			t.Fatalf("retryDelay(%d) = %s, want %s", test.attempt, got, test.want)
		}
	}
}

func TestTruncateErrorPreservesUTF8(t *testing.T) {
	message := strings.Repeat("가", 2_000)
	truncated := truncateError(message)
	if len(truncated) > 4096 || !strings.HasPrefix(message, truncated) || strings.ToValidUTF8(truncated, "") != truncated {
		t.Fatalf("invalid truncated error: bytes=%d", len(truncated))
	}
}

type workerStoreStub struct {
	markSent        func(context.Context, string, string, string, int64) error
	markFailed      func(context.Context, string, string, string, string, int, int64) error
	markSentCalls   int
	markFailedCalls int
	sent            bool
}

func (s *workerStoreStub) ClaimDue(context.Context, int64, int) ([]*ScheduledPost, error) {
	return nil, nil
}

func (s *workerStoreStub) MarkSent(ctx context.Context, id, claimToken, resultPostID string, sentAt int64) error {
	s.markSentCalls++
	if s.markSent != nil {
		return s.markSent(ctx, id, claimToken, resultPostID, sentAt)
	}
	s.sent = true
	return nil
}

func (s *workerStoreStub) MarkFailed(ctx context.Context, id, claimToken, code, text string, attempts int, failedAt int64) error {
	s.markFailedCalls++
	if s.markFailed != nil {
		return s.markFailed(ctx, id, claimToken, code, text, attempts, failedAt)
	}
	return nil
}

type workerDispatcherStub struct {
	get      func(context.Context, string) (*posts.Post, error)
	update   func(context.Context, string, []string) error
	getCalls int
}

func (d *workerDispatcherStub) GetByScheduledPost(ctx context.Context, scheduledPostID string) (*posts.Post, error) {
	d.getCalls++
	if d.get == nil {
		return nil, pgx.ErrNoRows
	}
	return d.get(ctx, scheduledPostID)
}

func (d *workerDispatcherStub) UpdateFileIDs(ctx context.Context, postID string, fileIDs []string) error {
	if d.update != nil {
		return d.update(ctx, postID, fileIDs)
	}
	return nil
}

type workerCommandStub struct {
	execute func(context.Context, postcommand.Command) (*posts.Post, error)
	calls   int
}

type workerFileStub struct {
	attached  []string
	userID    string
	fileIDs   []string
	postID    string
	channelID string
	calls     int
}

func (s *workerFileStub) AssociateWithPost(_ context.Context, userID string, fileIDs []string, postID, channelID string) ([]string, error) {
	s.calls++
	s.userID = userID
	s.fileIDs = append([]string(nil), fileIDs...)
	s.postID = postID
	s.channelID = channelID
	return append([]string(nil), s.attached...), nil
}

func (s *workerCommandStub) Execute(ctx context.Context, command postcommand.Command) (*posts.Post, error) {
	s.calls++
	if s.execute == nil {
		return nil, errors.New("unexpected post command call")
	}
	return s.execute(ctx, command)
}

type workerBroadcasterStub struct {
	events          []ws.Event
	beforeBroadcast func()
}

func (b *workerBroadcasterStub) Broadcast(event ws.Event) {
	if b.beforeBroadcast != nil {
		b.beforeBroadcast()
	}
	b.events = append(b.events, event)
}

func testScheduledPost() *ScheduledPost {
	return &ScheduledPost{
		ID:           "scheduled-1",
		UserID:       "user-1",
		ChannelID:    "channel-1",
		Message:      "scheduled message",
		FileIDs:      []string{},
		Props:        map[string]any{},
		ClaimToken:   "claim-1",
		AttemptCount: 2,
	}
}

func testDeliveredPost() *posts.Post {
	return &posts.Post{
		ID:        "post-1",
		UserID:    "user-1",
		ChannelID: "channel-1",
		Message:   "scheduled message",
		FileIDs:   []string{},
		Props:     map[string]any{},
	}
}

func assertActiveContext(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := ctx.Err(); err != nil {
		t.Fatalf("worker operation inherited cancelled context: %v", err)
	}
}
