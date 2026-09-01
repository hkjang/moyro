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
		{name: "nil audience", resolver: func(context.Context, Broadcast) (map[string]struct{}, error) {
			return nil, nil
		}},
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

func TestFanoutFiltersSubjectUserEventsByAudience(t *testing.T) {
	h := NewHub()
	regular := testClient("regular", 1)
	sharedGuest := testClient("shared-guest", 1)
	unsharedGuest := testClient("unshared-guest", 1)
	attachTestClient(h, regular)
	attachTestClient(h, sharedGuest)
	attachTestClient(h, unsharedGuest)
	h.SetAudienceResolver(func(_ context.Context, scope Broadcast) (map[string]struct{}, error) {
		if scope.SubjectUserID != "subject" {
			t.Fatalf("subject scope = %#v", scope)
		}
		return map[string]struct{}{"regular": {}, "shared-guest": {}}, nil
	})

	h.fanout(context.Background(), Event{
		Event:     "status_change",
		Data:      map[string]any{"user_id": "subject"},
		Broadcast: Broadcast{SubjectUserID: "subject"},
	}, []byte("presence"))

	for _, client := range []*Client{regular, sharedGuest} {
		select {
		case got := <-client.Send:
			if string(got) != "presence" {
				t.Fatalf("payload = %q", got)
			}
		default:
			t.Fatalf("allowed user %q did not receive subject event", client.UserID)
		}
	}
	select {
	case <-unsharedGuest.Send:
		t.Fatal("unshared guest received subject event")
	default:
	}
}

func TestFanoutFailsClosedForSubjectUserEvent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resolver AudienceResolver
	}{
		{name: "missing resolver"},
		{name: "nil audience", resolver: func(context.Context, Broadcast) (map[string]struct{}, error) {
			return nil, nil
		}},
		{name: "resolver error", resolver: func(context.Context, Broadcast) (map[string]struct{}, error) {
			return nil, errors.New("database unavailable")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHub()
			client := testClient("regular", 1)
			attachTestClient(h, client)
			if tc.resolver != nil {
				h.SetAudienceResolver(tc.resolver)
			}

			h.fanout(context.Background(), Event{
				Event: "status_change", Data: map[string]any{"user_id": "subject"},
				Broadcast: Broadcast{SubjectUserID: "subject"},
			}, []byte("event"))
			select {
			case <-client.Send:
				t.Fatal("subject event was delivered without a valid audience")
			default:
			}
		})
	}
}

func TestFanoutRejectsSensitiveEventWithoutMatchingSubjectScope(t *testing.T) {
	h := NewHub()
	client := testClient("regular", 2)
	attachTestClient(h, client)
	h.SetAudienceResolver(func(context.Context, Broadcast) (map[string]struct{}, error) {
		return map[string]struct{}{"regular": {}}, nil
	})

	for _, event := range []Event{
		{Event: "status_change", Data: map[string]any{"user_id": "subject"}},
		{
			Event: "custom_status_changed", Data: map[string]any{"user_id": "different-subject"},
			Broadcast: Broadcast{SubjectUserID: "subject"},
		},
	} {
		h.fanout(context.Background(), event, []byte("sensitive"))
	}
	select {
	case got := <-client.Send:
		t.Fatalf("invalid sensitive event was delivered: %q", got)
	default:
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

func TestFanoutIntersectsUserTargetWithLiveAudience(t *testing.T) {
	h := NewHub()
	target := testClient("target", 2)
	other := testClient("other", 2)
	attachTestClient(h, target)
	attachTestClient(h, other)

	member := true
	h.SetAudienceResolver(func(_ context.Context, scope Broadcast) (map[string]struct{}, error) {
		if scope.UserID != "target" || scope.ChannelID != "channel" || scope.TeamID != "team" {
			t.Fatalf("resolver scope = %#v", scope)
		}
		if member {
			return map[string]struct{}{"target": {}, "other": {}}, nil
		}
		return map[string]struct{}{}, nil
	})

	scoped := Event{Broadcast: Broadcast{UserID: "target", ChannelID: "channel", TeamID: "team"}}
	h.fanout(context.Background(), scoped, []byte("before-revocation"))
	if got := <-target.Send; string(got) != "before-revocation" {
		t.Fatalf("target payload = %q", got)
	}
	select {
	case <-other.Send:
		t.Fatal("non-target audience member received user-targeted event")
	default:
	}

	member = false
	h.fanout(context.Background(), scoped, []byte("after-revocation"))
	select {
	case got := <-target.Send:
		t.Fatalf("revoked target received scoped event %q", got)
	default:
	}
}

func TestFanoutFailsClosedForScopedUserTarget(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resolver AudienceResolver
	}{
		{name: "missing resolver"},
		{name: "nil audience", resolver: func(context.Context, Broadcast) (map[string]struct{}, error) {
			return nil, nil
		}},
		{name: "resolver error", resolver: func(context.Context, Broadcast) (map[string]struct{}, error) {
			return nil, errors.New("database unavailable")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHub()
			target := testClient("target", 1)
			attachTestClient(h, target)
			if tc.resolver != nil {
				h.SetAudienceResolver(tc.resolver)
			}

			h.fanout(context.Background(), Event{Broadcast: Broadcast{UserID: "target", ChannelID: "channel"}}, []byte("event"))
			select {
			case <-target.Send:
				t.Fatal("scoped user event was delivered without a valid audience")
			default:
			}
		})
	}
}
