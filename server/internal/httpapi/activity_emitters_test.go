package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/hkjang/moyro/server/internal/activityevents"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/ws"
)

type recordingActivityEmitter struct {
	inputs []activityevents.EmitInput
	err    error
}

func (e *recordingActivityEmitter) Emit(_ context.Context, input activityevents.EmitInput) (*activityevents.Event, error) {
	if e.err != nil {
		return nil, e.err
	}
	e.inputs = append(e.inputs, input)
	return &activityevents.Event{
		ID: "event-" + input.UserID, UserID: input.UserID, Type: input.Type,
		ActorID: input.ActorID, TeamID: input.TeamID, ChannelID: input.ChannelID,
		PostID: input.PostID, ResourceType: input.ResourceType, ResourceID: input.ResourceID,
		Title: input.Title, Summary: input.Summary, CreateAt: 10, UpdateAt: 10,
	}, nil
}

type recordingActivityBroadcaster struct{ events []ws.Event }

func (b *recordingActivityBroadcaster) Broadcast(event ws.Event) {
	b.events = append(b.events, event)
}

type fakePostActivityChannels struct {
	channel *channels.Channel
	members []channels.Member
}

func (f fakePostActivityChannels) Get(context.Context, string) (*channels.Channel, error) {
	return f.channel, nil
}

func (f fakePostActivityChannels) ListMembers(context.Context, string) ([]channels.Member, error) {
	return f.members, nil
}

type fakePostActivityPosts struct{ thread *posts.PostList }

func (f fakePostActivityPosts) ListThread(context.Context, string) (*posts.PostList, error) {
	return f.thread, nil
}

func TestRealtimeActivityEmitterPersistsBeforeUserScopedBroadcast(t *testing.T) {
	store := &recordingActivityEmitter{}
	broadcasts := &recordingActivityBroadcaster{}
	emitter := &realtimeActivityEmitter{next: store, events: broadcasts}
	input := activityevents.EmitInput{
		UserID: "recipient", Type: activityevents.TypeMention, DedupeKey: "post-1",
		TeamID: "team-1", ChannelID: "channel-1", Title: "새 멘션", Summary: "안전한 요약",
	}
	event, err := emitter.Emit(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != "event-recipient" || len(store.inputs) != 1 {
		t.Fatalf("persisted event = %#v, inputs=%#v", event, store.inputs)
	}
	if len(broadcasts.events) != 1 || broadcasts.events[0].Event != "activity_event" ||
		broadcasts.events[0].Broadcast.UserID != "recipient" ||
		broadcasts.events[0].Broadcast.TeamID != "team-1" ||
		broadcasts.events[0].Broadcast.ChannelID != "channel-1" {
		t.Fatalf("broadcasts = %#v", broadcasts.events)
	}
	view, ok := broadcasts.events[0].Data["event"].(activityEventView)
	if !ok || view.ID != event.ID || view.Title != input.Title {
		t.Fatalf("safe websocket view = %#v", broadcasts.events[0].Data["event"])
	}

	store.err = errors.New("database unavailable")
	if _, err := emitter.Emit(context.Background(), input); err == nil {
		t.Fatal("expected persistence failure")
	}
	if len(broadcasts.events) != 1 {
		t.Fatal("failed persistence must not broadcast")
	}
}

func TestPostActivityEmitterUsesMembershipAndOneHighestPriorityEvent(t *testing.T) {
	store := &recordingActivityEmitter{}
	emitter := &postActivityEmitter{
		channels: fakePostActivityChannels{
			channel: &channels.Channel{ID: "channel-1", TeamID: "team-1", Type: "G", DisplayName: "운영 대화"},
			members: []channels.Member{
				{UserID: "author"}, {UserID: "mentioned"}, {UserID: "participant"}, {UserID: "direct-only"},
			},
		},
		posts: fakePostActivityPosts{thread: &posts.PostList{Posts: map[string]*posts.Post{
			"root": {ID: "root", UserID: "participant"},
			"gone": {ID: "gone", UserID: "not-a-member"},
		}}},
		events: store,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	post := &posts.Post{
		ID: "post-1", ChannelID: "channel-1", UserID: "author", RootID: "root",
		Message: strings.Repeat("한", 300),
	}
	emitter.PostCreated(context.Background(), post, []string{"mentioned", "not-a-member"})

	byUser := map[string]activityevents.EmitInput{}
	for _, input := range store.inputs {
		if _, duplicate := byUser[input.UserID]; duplicate {
			t.Fatalf("duplicate recipient event: %#v", store.inputs)
		}
		byUser[input.UserID] = input
		if input.DedupeKey != post.ID || input.PostID != post.ID || input.ResourceID != post.ID {
			t.Fatalf("source contract = %#v", input)
		}
		if got := len([]rune(strings.TrimSuffix(input.Summary, "…"))); got != 280 {
			t.Fatalf("summary rune length = %d", got)
		}
	}
	if len(byUser) != 3 {
		t.Fatalf("recipients = %#v", byUser)
	}
	if byUser["mentioned"].Type != activityevents.TypeMention {
		t.Fatalf("mentioned event = %#v", byUser["mentioned"])
	}
	// Group/direct conversation is higher priority than a generic thread reply.
	if byUser["participant"].Type != activityevents.TypeDirectMessage || byUser["direct-only"].Type != activityevents.TypeDirectMessage {
		t.Fatalf("direct events = %#v", byUser)
	}
	if _, leaked := byUser["not-a-member"]; leaked {
		t.Fatal("non-member received an activity event")
	}
}
