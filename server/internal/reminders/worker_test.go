package reminders

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/ws"
)

func TestReminderExcerptNormalizesWhitespaceAndTruncatesByRune(t *testing.T) {
	input := "  첫 줄\n\t" + strings.Repeat("한", 150)
	got := reminderExcerpt(input, 140)
	if strings.ContainsAny(got, "\n\t") {
		t.Fatalf("whitespace was not normalized: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated excerpt = %q", got)
	}
	if len([]rune(strings.TrimSuffix(got, "…"))) != 140 {
		t.Fatalf("visible rune length = %d", len([]rune(strings.TrimSuffix(got, "…"))))
	}
}

func TestReminderFiredEventScopesResolvedPostToChannel(t *testing.T) {
	reminder := &Reminder{ID: "reminder-1", UserID: "user-1", PostID: "post-1", RemindAt: 1234}
	resolved := reminderFiredEvent(reminder, "channel-1", "확인할 메시지")
	if resolved.Broadcast.UserID != "user-1" || resolved.Broadcast.ChannelID != "channel-1" {
		t.Fatalf("resolved reminder broadcast = %#v", resolved.Broadcast)
	}
	missing := reminderFiredEvent(reminder, "", "")
	if missing.Broadcast.UserID != "user-1" || missing.Broadcast.ChannelID != "" {
		t.Fatalf("missing-post reminder broadcast = %#v", missing.Broadcast)
	}
}

func TestReminderFiredEventStopsAfterChannelMembershipRevocation(t *testing.T) {
	hub := ws.NewHub()
	var member atomic.Bool
	member.Store(true)
	resolved := make(chan struct{}, 2)
	hub.SetAudienceResolver(func(context.Context, ws.Broadcast) (map[string]struct{}, error) {
		audience := map[string]struct{}{}
		if member.Load() {
			audience["user-1"] = struct{}{}
		}
		resolved <- struct{}{}
		return audience, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hub.Run(ctx)
	client := &ws.Client{UserID: "user-1", Send: make(chan []byte, 2)}
	hub.Register(client)
	deadline := time.Now().Add(time.Second)
	for hub.ClientCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if hub.ClientCount() != 1 {
		t.Fatalf("registered websocket clients = %d", hub.ClientCount())
	}

	event := reminderFiredEvent(&Reminder{ID: "reminder-1", UserID: "user-1", PostID: "post-1"}, "channel-1", "확인")
	hub.Broadcast(event)
	select {
	case <-resolved:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for live audience resolution")
	}
	select {
	case <-client.Send:
	case <-time.After(time.Second):
		t.Fatal("current member did not receive reminder")
	}

	member.Store(false)
	hub.Broadcast(event)
	select {
	case <-resolved:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for revoked audience resolution")
	}
	select {
	case raw := <-client.Send:
		t.Fatalf("revoked member received reminder: %s", raw)
	case <-time.After(100 * time.Millisecond):
	}
}
