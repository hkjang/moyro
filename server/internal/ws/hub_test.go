package ws

import (
	"context"
	"errors"
	"testing"
	"time"
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
	h.SetAudienceResolver(func(context.Context, Broadcast, []string) (map[string]struct{}, error) {
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
		{name: "nil audience", resolver: func(context.Context, Broadcast, []string) (map[string]struct{}, error) {
			return nil, nil
		}},
		{name: "resolver error", resolver: func(context.Context, Broadcast, []string) (map[string]struct{}, error) {
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
	h.SetAudienceResolver(func(_ context.Context, scope Broadcast, _ []string) (map[string]struct{}, error) {
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
		{name: "nil audience", resolver: func(context.Context, Broadcast, []string) (map[string]struct{}, error) {
			return nil, nil
		}},
		{name: "resolver error", resolver: func(context.Context, Broadcast, []string) (map[string]struct{}, error) {
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
	h.SetAudienceResolver(func(context.Context, Broadcast, []string) (map[string]struct{}, error) {
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
	h.SetAudienceResolver(func(_ context.Context, scope Broadcast, _ []string) (map[string]struct{}, error) {
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
		{name: "nil audience", resolver: func(context.Context, Broadcast, []string) (map[string]struct{}, error) {
			return nil, nil
		}},
		{name: "resolver error", resolver: func(context.Context, Broadcast, []string) (map[string]struct{}, error) {
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

// TestFanoutOffersOnlyConnectedUsersAsCandidates pins the contract that lets the
// database resolver skip the user directory: fan-out asks about the users this
// instance actually holds sockets for, de-duplicated across a user's tabs, and
// never about anybody else.
func TestFanoutOffersOnlyConnectedUsersAsCandidates(t *testing.T) {
	h := NewHub()
	firstTab := testClient("member", 1)
	secondTab := &Client{UserID: "member", Send: make(chan []byte, 1)}
	other := testClient("other", 1)
	attachTestClient(h, firstTab)
	h.clients[secondTab] = struct{}{}
	h.byUser["member"][secondTab] = struct{}{}
	attachTestClient(h, other)

	var seen []string
	h.SetAudienceResolver(func(_ context.Context, _ Broadcast, candidates []string) (map[string]struct{}, error) {
		seen = append([]string(nil), candidates...)
		return map[string]struct{}{"member": {}}, nil
	})

	h.fanout(context.Background(), Event{Broadcast: Broadcast{ChannelID: "private"}}, []byte("event"))

	if len(seen) != 2 {
		t.Fatalf("candidates = %v, want one entry per connected user", seen)
	}
	got := map[string]int{}
	for _, id := range seen {
		got[id]++
	}
	if got["member"] != 1 || got["other"] != 1 {
		t.Fatalf("candidates = %v, want exactly one entry each for member and other", seen)
	}

	// Both of the member's tabs still receive the event.
	for i, c := range []*Client{firstTab, secondTab} {
		select {
		case <-c.Send:
		default:
			t.Fatalf("member tab %d did not receive scoped event", i)
		}
	}
}

// TestFanoutNarrowsCandidatesToAUserTarget keeps a user-targeted scoped event
// from asking about every connected user just to authorize one recipient.
func TestFanoutNarrowsCandidatesToAUserTarget(t *testing.T) {
	h := NewHub()
	target := testClient("target", 1)
	bystander := testClient("bystander", 1)
	attachTestClient(h, target)
	attachTestClient(h, bystander)

	var seen []string
	h.SetAudienceResolver(func(_ context.Context, _ Broadcast, candidates []string) (map[string]struct{}, error) {
		seen = append([]string(nil), candidates...)
		return map[string]struct{}{"target": {}}, nil
	})

	h.fanout(context.Background(), Event{Broadcast: Broadcast{ChannelID: "private", UserID: "target"}}, []byte("event"))

	if len(seen) != 1 || seen[0] != "target" {
		t.Fatalf("candidates for user-targeted event = %v, want [target]", seen)
	}
	select {
	case <-target.Send:
	default:
		t.Fatal("targeted user did not receive scoped event")
	}
	select {
	case <-bystander.Send:
		t.Fatal("bystander received a user-targeted event")
	default:
	}
}

// TestFanoutSkipsResolutionWithNoConnectedClients proves a broadcast with no
// local sockets costs nothing: the resolver (and therefore the database) is
// never consulted.
func TestFanoutSkipsResolutionWithNoConnectedClients(t *testing.T) {
	h := NewHub()
	consulted := false
	h.SetAudienceResolver(func(context.Context, Broadcast, []string) (map[string]struct{}, error) {
		consulted = true
		return map[string]struct{}{}, nil
	})

	h.fanout(context.Background(), Event{Broadcast: Broadcast{ChannelID: "private"}}, []byte("event"))

	if consulted {
		t.Fatal("resolver was consulted with no connected clients")
	}
}

// TestCanPublishAsksOnlyAboutTheActor keeps the client-originated authorization
// check from resolving an entire channel audience to answer a question about a
// single user.
func TestCanPublishAsksOnlyAboutTheActor(t *testing.T) {
	h := NewHub()
	attachTestClient(h, testClient("actor", 1))
	attachTestClient(h, testClient("bystander", 1))

	var seen []string
	h.SetAudienceResolver(func(_ context.Context, _ Broadcast, candidates []string) (map[string]struct{}, error) {
		seen = append([]string(nil), candidates...)
		return map[string]struct{}{"actor": {}}, nil
	})

	allowed, err := h.CanPublish(context.Background(), "actor", Broadcast{ChannelID: "private"})
	if err != nil || !allowed {
		t.Fatalf("CanPublish = %v, %v", allowed, err)
	}
	if len(seen) != 1 || seen[0] != "actor" {
		t.Fatalf("CanPublish candidates = %v, want [actor]", seen)
	}

	denied, err := h.CanPublish(context.Background(), "bystander", Broadcast{ChannelID: "private"})
	if err != nil || denied {
		t.Fatalf("CanPublish for non-member = %v, %v", denied, err)
	}
}

// TestRunKeepsRegisteringWhileDeliveryIsBlocked is the reason fan-out no longer
// runs inside the Run loop: authorization needs a database round-trip, and a
// slow one must not stop clients from connecting or stall the publishers
// feeding the broadcast queue.
func TestRunKeepsRegisteringWhileDeliveryIsBlocked(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	h.SetAudienceResolver(func(context.Context, Broadcast, []string) (map[string]struct{}, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return map[string]struct{}{}, nil
	})

	// One connected client so the first event actually reaches the resolver.
	blocker := testClient("blocker", 1)
	h.mu.Lock()
	h.clients[blocker] = struct{}{}
	h.byUser["blocker"] = map[*Client]struct{}{blocker: {}}
	h.mu.Unlock()

	go h.Run(ctx)

	h.Broadcast(Event{Event: "first", Broadcast: Broadcast{ChannelID: "slow"}})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("delivery never reached the audience resolver")
	}

	// Delivery is now wedged inside the resolver. Registration must still work.
	latecomer := testClient("latecomer", 1)
	h.Register(latecomer)
	deadline := time.After(2 * time.Second)
	for {
		if h.IsOnline("latecomer") {
			break
		}
		select {
		case <-deadline:
			close(release)
			t.Fatal("registration stalled behind a blocked delivery")
		case <-time.After(5 * time.Millisecond):
		}
	}

	// Publishing must not block either: Run keeps draining the broadcast queue
	// and sheds into the delivery queue instead of applying back-pressure. Both
	// queues have to fill before shedding starts, so publish until the hub
	// reports a drop rather than guessing a count from the buffer sizes.
	published := make(chan struct{})
	go func() {
		defer close(published)
		for h.Stats().DroppedEvents == 0 {
			h.Broadcast(Event{Event: "flood", Broadcast: Broadcast{ChannelID: "slow"}})
		}
	}()
	select {
	case <-published:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("publishing blocked behind a stalled delivery goroutine")
	}

	close(release)
	if stats := h.Stats(); stats.DroppedEvents == 0 {
		t.Fatalf("shed events were not counted: %#v", stats)
	}
}

// TestStatsCountsFailClosedDropsAndSlowConsumers keeps the two silent drop
// paths observable: an unresolvable audience and a client that is not reading.
func TestStatsCountsFailClosedDropsAndSlowConsumers(t *testing.T) {
	h := NewHub()
	h.SetAudienceResolver(func(context.Context, Broadcast, []string) (map[string]struct{}, error) {
		return nil, errors.New("resolver unavailable")
	})
	attachTestClient(h, testClient("member", 1))
	h.fanout(context.Background(), Event{Broadcast: Broadcast{ChannelID: "private"}}, []byte("event"))
	if stats := h.Stats(); stats.AudienceFailures != 1 {
		t.Fatalf("audience failure not counted: %#v", stats)
	}

	// A client whose buffer is already full is a slow consumer, not a delivery
	// failure, and is counted separately.
	slow := NewHub()
	full := &Client{UserID: "slow", Send: make(chan []byte, 1)}
	full.Send <- []byte("backlog")
	slow.clients[full] = struct{}{}
	slow.byUser["slow"] = map[*Client]struct{}{full: {}}
	slow.fanout(context.Background(), Event{Broadcast: Broadcast{}}, []byte("event"))
	if stats := slow.Stats(); stats.DroppedSends != 1 {
		t.Fatalf("slow-consumer drop not counted: %#v", stats)
	}
	if stats := slow.Stats(); stats.Clients != 1 || stats.Users != 1 {
		t.Fatalf("connection stats = %#v", stats)
	}
}
