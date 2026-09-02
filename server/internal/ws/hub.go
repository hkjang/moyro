package ws

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

type Event struct {
	Event     string         `json:"event"`
	Data      map[string]any `json:"data"`
	Broadcast Broadcast      `json:"broadcast"`
	Seq       int64          `json:"seq"`
}

type Broadcast struct {
	ChannelID     string   `json:"channel_id,omitempty"`
	UserID        string   `json:"user_id,omitempty"`
	TeamID        string   `json:"team_id,omitempty"`
	SubjectUserID string   `json:"subject_user_id,omitempty"`
	OmitUsers     []string `json:"omit_users,omitempty"`
}

type Client struct {
	UserID string
	Conn   *websocket.Conn
	Send   chan []byte

	// credential/authenticate are intentionally private: they exist only so
	// the connection writer can periodically revalidate the exact session
	// that opened this socket. They must never be serialized or logged.
	credential   string
	authenticate TokenAuthenticator
}

// Publisher is an optional sink that `Broadcast` pushes every event to
// in addition to the local fanout. Used by the Redis pub/sub adapter to
// cross-propagate events to other instances behind a load balancer.
// Implementations must be non-blocking — the hub calls Publish from its
// Run loop and a slow publisher would stall local delivery.
type Publisher interface {
	Publish(ev Event)
}

// AudienceResolver returns the users allowed to receive a channel-, team-, or
// subject-user-scoped event. Scoped events fail closed when authorization
// cannot be resolved. A user target narrows this audience; it never bypasses it.
//
// `candidates` is the set of user ids the caller could actually deliver to.
// Resolvers must treat it as an upper bound and never return an id outside it,
// which lets a database-backed implementation answer the membership question
// for a handful of connected users instead of the whole directory. Callers
// never pass an empty slice for a scoped event: fan-out returns early when no
// candidate exists, so an empty slice means "no recipients", not "everyone".
type AudienceResolver func(ctx context.Context, broadcast Broadcast, candidates []string) (map[string]struct{}, error)

type Hub struct {
	mu      sync.RWMutex
	clients map[*Client]struct{}
	byUser  map[string]map[*Client]struct{}
	reg     chan *Client
	unreg   chan *Client
	bcast   chan Event
	seq     int64

	// OnConnect fires once per user when their first client registers.
	// OnDisconnect fires when the last client for a user goes away. Both
	// are invoked in goroutines so a slow callback doesn't stall the hub.
	OnConnect    func(userID string)
	OnDisconnect func(userID string)

	// pub is the optional cross-instance publisher. Set via SetPublisher;
	// read under mu.RLock so hot-swapping during tests is safe.
	pub Publisher

	// audience enforces the authorization boundary for channel/team events.
	audience AudienceResolver

	// deliveries carries sequenced, already-marshalled events from the Run
	// loop to the delivery goroutine. Authorizing an event requires a
	// membership lookup, so doing it inside Run would let a slow database
	// stall client registration and, through the bcast channel, every request
	// that publishes an event.
	deliveries chan delivery

	// Counters for behaviour that is otherwise invisible: the hub drops
	// events rather than blocking, and an operator needs to see that happen.
	// Read through Stats; published as metrics by the process that owns Run.
	droppedEvents    atomic.Int64
	droppedSends     atomic.Int64
	audienceFailures atomic.Int64
}

// delivery is one sequenced event with its encoded frame, queued for fan-out.
type delivery struct {
	event Event
	raw   []byte
}

// deliveryQueueSize bounds how far delivery may lag behind publication before
// the hub starts shedding events. It is deliberately larger than the publish
// queue: a transient database stall should be absorbed here rather than
// propagate back into request handling.
const deliveryQueueSize = 1024

// HubStats reports hub activity that has no other observable surface.
type HubStats struct {
	// Clients is the number of open sockets, not distinct users.
	Clients int
	// Users is the number of distinct users with at least one open socket.
	Users int
	// DroppedEvents counts events discarded because delivery fell too far
	// behind. A non-zero value means some clients missed events.
	DroppedEvents int64
	// DroppedSends counts per-socket writes discarded because that client's
	// send buffer was full — a slow or stalled consumer.
	DroppedSends int64
	// AudienceFailures counts events dropped because authorization could not
	// be resolved. These are fail-closed drops, not delivery failures.
	AudienceFailures int64
}

// Stats snapshots the hub's counters. Safe to call from any goroutine.
func (h *Hub) Stats() HubStats {
	h.mu.RLock()
	clients := len(h.clients)
	users := len(h.byUser)
	h.mu.RUnlock()
	return HubStats{
		Clients:          clients,
		Users:            users,
		DroppedEvents:    h.droppedEvents.Load(),
		DroppedSends:     h.droppedSends.Load(),
		AudienceFailures: h.audienceFailures.Load(),
	}
}

func NewHub() *Hub {
	return &Hub{
		clients:    map[*Client]struct{}{},
		byUser:     map[string]map[*Client]struct{}{},
		reg:        make(chan *Client, 16),
		unreg:      make(chan *Client, 16),
		bcast:      make(chan Event, 256),
		deliveries: make(chan delivery, deliveryQueueSize),
	}
}

// IsOnline returns true if the user has at least one open client. Useful
// for synthesising statuses on the auth path without a DB hit.
func (h *Hub) IsOnline(userID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.byUser[userID]) > 0
}

// ClientCount reports the number of currently connected sockets (NOT
// distinct users — a single user with three tabs counts as three). Used
// by the Prometheus `moyro_ws_clients` gauge.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// SetPublisher wires a cross-instance publisher (typically the Redis
// fan-out). Safe to call once at startup, before Run begins delivering.
// Passing nil clears the publisher.
func (h *Hub) SetPublisher(p Publisher) {
	h.mu.Lock()
	h.pub = p
	h.mu.Unlock()
}

// SetAudienceResolver installs the membership lookup used during fan-out.
func (h *Hub) SetAudienceResolver(resolver AudienceResolver) {
	h.mu.Lock()
	h.audience = resolver
	h.mu.Unlock()
}

// CanPublish verifies that an actor is currently inside the audience of a
// scoped event. Client-originated actions use this before enqueueing so an
// authenticated user cannot spoof a channel id they do not belong to. A
// missing or failed resolver is denied rather than treated as global access.
func (h *Hub) CanPublish(ctx context.Context, userID string, broadcast Broadcast) (bool, error) {
	if userID == "" || (broadcast.ChannelID == "" && broadcast.TeamID == "") {
		return false, nil
	}
	h.mu.RLock()
	resolver := h.audience
	h.mu.RUnlock()
	if resolver == nil {
		return false, nil
	}
	// Only this one actor's membership is in question, so the resolver never
	// has to consider anybody else.
	audience, err := resolver(ctx, broadcast, []string{userID})
	if err != nil {
		return false, err
	}
	_, allowed := audience[userID]
	return allowed, nil
}

// InjectEvent pushes an event from an external source (Redis subscriber)
// onto the local broadcast channel, bypassing the publisher so we don't
// re-ship it back to Redis and cause a loop. Non-blocking; drops the
// event if the bcast queue is full (matches the trySend drop policy).
func (h *Hub) InjectEvent(ev Event) {
	if !validSensitiveEventScope(ev) {
		return
	}
	select {
	case h.bcast <- ev:
	default:
	}
}

// KickUser closes every open socket for the given user. Used by
// deactivation + session-revoke so a logged-in attacker loses their live
// WS stream the moment their session is killed, not at the next read
// timeout. The socket's write side is closed via the Conn — the Send
// channel is closed by the hub's Unregister path after the peer EOFs.
func (h *Hub) KickUser(userID string) int {
	h.mu.RLock()
	set := h.byUser[userID]
	conns := make([]*websocket.Conn, 0, len(set))
	for c := range set {
		conns = append(conns, c.Conn)
	}
	h.mu.RUnlock()
	for _, c := range conns {
		_ = c.Close()
	}
	return len(conns)
}

func (h *Hub) Register(c *Client)   { h.reg <- c }
func (h *Hub) Unregister(c *Client) { h.unreg <- c }

// Broadcast publishes a locally-originated event to other instances (if a
// Publisher is set) and enqueues it for local fanout. Remote events
// injected via InjectEvent skip the publish step so we don't loop.
func (h *Hub) Broadcast(ev Event) {
	if !validSensitiveEventScope(ev) {
		return
	}
	h.mu.RLock()
	pub := h.pub
	h.mu.RUnlock()
	if pub != nil {
		// Fire-and-forget to avoid blocking the caller on a slow Redis
		// connection — the subscriber side is reliable enough and a
		// single dropped event on a flaky network is preferable to
		// stalling every /posts request.
		go pub.Publish(ev)
	}
	h.bcast <- ev
}

// Run owns hub state. It assigns each event its sequence number, encodes it,
// and hands it to a single delivery goroutine; registration and unregistration
// therefore stay responsive no matter how long authorization takes. Delivery
// order is preserved because exactly one goroutine drains the queue.
func (h *Hub) Run(ctx context.Context) {
	deliveryDone := make(chan struct{})
	go func() {
		defer close(deliveryDone)
		h.runDeliveries(ctx)
	}()
	defer func() {
		close(h.deliveries)
		<-deliveryDone
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case c := <-h.reg:
			h.mu.Lock()
			h.clients[c] = struct{}{}
			set := h.byUser[c.UserID]
			firstForUser := false
			if set == nil {
				set = map[*Client]struct{}{}
				h.byUser[c.UserID] = set
				firstForUser = true
			}
			set[c] = struct{}{}
			cb := h.OnConnect
			h.mu.Unlock()
			if firstForUser && cb != nil {
				go cb(c.UserID)
			}
		case c := <-h.unreg:
			h.mu.Lock()
			delete(h.clients, c)
			lastForUser := false
			if set := h.byUser[c.UserID]; set != nil {
				delete(set, c)
				if len(set) == 0 {
					delete(h.byUser, c.UserID)
					lastForUser = true
				}
			}
			close(c.Send)
			cb := h.OnDisconnect
			h.mu.Unlock()
			if lastForUser && cb != nil {
				go cb(c.UserID)
			}
		case ev := <-h.bcast:
			h.seq++
			ev.Seq = h.seq
			raw, err := json.Marshal(ev)
			if err != nil {
				h.droppedEvents.Add(1)
				continue
			}
			select {
			case h.deliveries <- delivery{event: ev, raw: raw}:
			default:
				// Delivery is saturated. Shedding here keeps publication
				// bounded; the alternative is back-pressure that reaches
				// every request handler that posts a message.
				h.droppedEvents.Add(1)
			}
		}
	}
}

// runDeliveries fans out queued events one at a time, preserving the order Run
// assigned. It exits when the queue is closed or the context is cancelled.
func (h *Hub) runDeliveries(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case item, ok := <-h.deliveries:
			if !ok {
				return
			}
			h.fanout(ctx, item.event, item.raw)
		}
	}
}

func (h *Hub) fanout(ctx context.Context, ev Event, raw []byte) {
	// Defense in depth for direct fanout tests and any future internal caller
	// that bypasses Broadcast/InjectEvent. Presence payloads without an exact
	// subject scope must never fall back to the empty-Broadcast global meaning.
	if !validSensitiveEventScope(ev) {
		return
	}
	scoped := ev.Broadcast.ChannelID != "" || ev.Broadcast.TeamID != "" || ev.Broadcast.SubjectUserID != ""

	// Snapshot the resolver and the users we could deliver to before doing any
	// authorization work, so the lock is never held across the resolver call.
	h.mu.RLock()
	audienceResolver := h.audience
	var candidates []string
	if scoped {
		if ev.Broadcast.UserID != "" {
			candidates = []string{ev.Broadcast.UserID}
		} else {
			candidates = make([]string, 0, len(h.byUser))
			for userID := range h.byUser {
				candidates = append(candidates, userID)
			}
		}
	}
	h.mu.RUnlock()

	var audience map[string]struct{}
	if scoped {
		if audienceResolver == nil {
			h.audienceFailures.Add(1)
			return
		}
		// Nobody this instance can reach is a candidate, so there is no
		// authorization question to ask.
		if len(candidates) == 0 {
			return
		}
		resolved, err := audienceResolver(ctx, ev.Broadcast, candidates)
		if err != nil {
			h.audienceFailures.Add(1)
			return
		}
		audience = resolved
	}

	// A client that registers between the snapshot and delivery is absent from
	// the resolved audience and therefore skipped. Missing a single event on a
	// brand-new socket is the fail-closed direction, and the client's reconnect
	// reconciliation restores the state either way.
	h.mu.RLock()
	defer h.mu.RUnlock()

	omit := map[string]struct{}{}
	for _, id := range ev.Broadcast.OmitUsers {
		omit[id] = struct{}{}
	}

	if ev.Broadcast.UserID != "" {
		if scoped {
			if _, allowed := audience[ev.Broadcast.UserID]; !allowed {
				return
			}
		}
		for c := range h.byUser[ev.Broadcast.UserID] {
			h.trySend(c, raw)
		}
		return
	}
	for c := range h.clients {
		if _, skip := omit[c.UserID]; skip {
			continue
		}
		if scoped {
			if _, allowed := audience[c.UserID]; !allowed {
				continue
			}
		}
		h.trySend(c, raw)
	}
}

func validSensitiveEventScope(ev Event) bool {
	switch ev.Event {
	case "status_change", "custom_status_changed":
		subject, ok := ev.Data["user_id"].(string)
		return ok && subject != "" && subject == ev.Broadcast.SubjectUserID
	default:
		return true
	}
}

func (h *Hub) trySend(c *Client, raw []byte) {
	select {
	case c.Send <- raw:
	default:
		// Drop rather than block on a slow consumer. The client reconciles
		// on reconnect, so a missed frame is recoverable; a blocked fan-out
		// is not.
		h.droppedSends.Add(1)
	}
}
