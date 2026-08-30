package httpapi

import (
	"context"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/hkjang/moyro/server/internal/activityevents"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/ws"
)

type activityBroadcaster interface {
	Broadcast(ws.Event)
}

// realtimeActivityEmitter persists first and only then notifies the owning
// user's sockets. The WebSocket payload uses the same explicit allow-list DTO
// as HTTP, so producer-private fields can never be exposed accidentally.
type realtimeActivityEmitter struct {
	next   activityevents.Emitter
	events activityBroadcaster
}

func (e *realtimeActivityEmitter) Emit(ctx context.Context, input activityevents.EmitInput) (*activityevents.Event, error) {
	event, err := e.next.Emit(ctx, input)
	if err != nil {
		return nil, err
	}
	if e.events != nil {
		e.events.Broadcast(ws.Event{
			Event: "activity_event",
			Data:  map[string]any{"event": activityEventResponse(*event)},
			Broadcast: ws.Broadcast{
				UserID: event.UserID, TeamID: event.TeamID, ChannelID: event.ChannelID,
			},
		})
	}
	return event, nil
}

type postActivityChannels interface {
	Get(context.Context, string) (*channels.Channel, error)
	ListMembers(context.Context, string) ([]channels.Member, error)
}

type postActivityPosts interface {
	ListThread(context.Context, string) (*posts.PostList, error)
}

// postActivityEmitter converts a committed message into at most one durable
// inbox item per recipient. Priority is mention, then direct message, then
// thread reply. Every candidate is intersected with the live channel member
// set before anything is persisted.
type postActivityEmitter struct {
	channels postActivityChannels
	posts    postActivityPosts
	events   activityevents.Emitter
	logger   *slog.Logger
}

func (e *postActivityEmitter) PostCreated(ctx context.Context, post *posts.Post, mentionedUserIDs []string) {
	if e == nil || e.events == nil || e.channels == nil || post == nil {
		return
	}
	channel, err := e.channels.Get(ctx, post.ChannelID)
	if err != nil || channel == nil || channel.DeleteAt != 0 {
		e.logFailure("channel", post, err)
		return
	}
	members, err := e.channels.ListMembers(ctx, post.ChannelID)
	if err != nil {
		e.logFailure("members", post, err)
		return
	}
	memberSet := make(map[string]struct{}, len(members))
	for _, member := range members {
		memberSet[member.UserID] = struct{}{}
	}
	recipients := make(map[string]activityevents.EventType)
	for _, userID := range mentionedUserIDs {
		if userID != post.UserID {
			if _, allowed := memberSet[userID]; allowed {
				recipients[userID] = activityevents.TypeMention
			}
		}
	}
	if channel.Type == "D" || channel.Type == "G" {
		for _, member := range members {
			if member.UserID == post.UserID {
				continue
			}
			if _, higherPriority := recipients[member.UserID]; !higherPriority {
				recipients[member.UserID] = activityevents.TypeDirectMessage
			}
		}
	}
	if post.RootID != "" && e.posts != nil {
		thread, threadErr := e.posts.ListThread(ctx, post.RootID)
		if threadErr != nil {
			e.logFailure("thread", post, threadErr)
		} else if thread != nil {
			for _, item := range thread.Posts {
				userID := item.UserID
				if userID == "" || userID == post.UserID {
					continue
				}
				if _, allowed := memberSet[userID]; !allowed {
					continue
				}
				if _, higherPriority := recipients[userID]; !higherPriority {
					recipients[userID] = activityevents.TypeThreadReply
				}
			}
		}
	}

	for userID, eventType := range recipients {
		input := activityevents.EmitInput{
			UserID: userID, Type: eventType, DedupeKey: post.ID,
			ActorID: post.UserID, TeamID: channel.TeamID, ChannelID: post.ChannelID,
			PostID: post.ID, ResourceType: "post", ResourceID: post.ID,
			Title:   activityPostTitle(eventType, channel.DisplayName),
			Summary: activityExcerpt(post.Message, 280),
		}
		if _, err := e.events.Emit(ctx, input); err != nil {
			e.logFailure("emit", post, err)
		}
	}
}

func activityPostTitle(eventType activityevents.EventType, channelName string) string {
	channelName = strings.TrimSpace(channelName)
	if channelName == "" {
		channelName = "대화"
	}
	switch eventType {
	case activityevents.TypeMention:
		return channelName + "에서 나를 멘션했습니다"
	case activityevents.TypeDirectMessage:
		return channelName + "에 새 메시지가 있습니다"
	case activityevents.TypeThreadReply:
		return channelName + " 스레드에 새 답글이 있습니다"
	default:
		return channelName + "에 새 소식이 있습니다"
	}
}

func activityExcerpt(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit])) + "…"
}

func (e *postActivityEmitter) logFailure(stage string, post *posts.Post, err error) {
	if e.logger == nil || err == nil {
		return
	}
	e.logger.Warn("post activity event", "stage", stage, "post_id", post.ID, "channel_id", post.ChannelID, "err", err)
}
