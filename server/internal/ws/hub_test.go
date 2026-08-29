package ws

import (
	"context"
	"errors"
	"testing"
)

func testClient(userID string, capacity int) *Client {
	return &Client{UserID: userID, Send: make(chan []byte, capacity)}
}

func attachTestClient(h *Hub, client *Client) {
	h.clients[client] = struct{}{}
	h.byUser[client.UserID] = map[*Client]struct{}{client: {}}
}

func TestFanoutFiltersScopedEventsByAudience(t *testing.T) {
	h := NewHub()
	allowed := testClient("member", 1)
	denied := testClient("outsider", 1)
	attachTestClient(h, allowed)
	attachTestClient(h, denied)
	h.SetAudienceResolver(func(context.Context, Broadcast) (map[string]struct{}, error) {
		return map[string]struct{}{"member": {}}, nil
	})

	h.fanout(context.Background(), Event{Broadcast: Broadcast{ChannelID: "private"}}, []byte("event"))

	select {
	case <-allowed.Send:
	default:
		t.Fatal("channel member did not receive scoped event")
	}
	select {
	case <-denied.Send:
		t.Fatal("non-member received scoped event")
	default:
	}
}

func TestFanoutFailsClosedForScopedEvent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resolver AudienceResolver
	}{
		{name: "missing resolver"},
		{name: "resolver error", resolver: func(context.Context, Broadcast) (map[string]struct{}, error) {
			return nil, errors.New("database unavailable")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHub()
			client := testClient("member", 1)
			attachTestClient(h, client)
			if tc.resolver != nil {
				h.SetAudienceResolver(tc.resolver)
			}

			h.fanout(context.Background(), Event{Broadcast: Broadcast{TeamID: "team"}}, []byte("event"))
			select {
			case <-client.Send:
				t.Fatal("scoped event was delivered without a valid audience")
			default:
			}
		})
	}
}

func TestFanoutKeepsUserTargetedAndGlobalSemantics(t *testing.T) {
	h := NewHub()
	first := testClient("first", 2)
	second := testClient("second", 2)
	attachTestClient(h, first)
	attachTestClient(h, second)

	h.fanout(context.Background(), Event{Broadcast: Broadcast{UserID: "first"}}, []byte("private"))
	if got := <-first.Send; string(got) != "private" {
		t.Fatalf("target payload = %q", got)
	}
	select {
	case <-second.Send:
		t.Fatal("non-target received user event")
	default:
	}

	h.fanout(context.Background(), Event{}, []byte("global"))
	if got := <-first.Send; string(got) != "global" {
		t.Fatalf("first global payload = %q", got)
	}
	if got := <-second.Send; string(got) != "global" {
		t.Fatalf("second global payload = %q", got)
	}
}
