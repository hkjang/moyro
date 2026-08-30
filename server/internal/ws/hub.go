package ws

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/gorilla/websocket"
)

type Event struct {
	Event     string         `json:"event"`
	Data      map[string]any `json:"data"`
	Broadcast Broadcast      `json:"broadcast"`
	Seq       int64          `json:"seq"`
}

type Broadcast struct {
	ChannelID string   `json:"channel_id,omitempty"`
	UserID    string   `json:"user_id,omitempty"`
	TeamID    string   `json:"team_id,omitempty"`
	OmitUsers []string `json:"omit_users,omitempty"`
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

// AudienceResolver returns the users allowed to receive a channel- or
// team-scoped event. Scoped events fail closed when membership cannot be
// resolved. A user target narrows this audience; it never bypasses it.
type AudienceResolver func(context.Context, Broadcast) (map[string]struct{}, error)

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
}

func NewHub() *Hub {
	return &Hub{
		clients: map[*Client]struct{}{},
		byUser:  map[string]map[*Client]struct{}{},
		reg:     make(chan *Client, 16),
		unreg:   make(chan *Client, 16),
		bcast:   make(chan Event, 256),
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

// InjectEvent pushes an event from an external source (Redis subscriber)
// onto the local broadcast channel, bypassing the publisher so we don't
// re-ship it back to Redis and cause a loop. Non-blocking; drops the
// event if the bcast queue is full (matches the trySend drop policy).
func (h *Hub) InjectEvent(ev Event) {
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

func (h *Hub) Run(ctx context.Context) {
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
			raw, _ := json.Marshal(ev)
			h.fanout(ctx, ev, raw)
		}
	}
}

func (h *Hub) fanout(ctx context.Context, ev Event, raw []byte) {
	h.mu.RLock()
	audienceResolver := h.audience
	h.mu.RUnlock()

	var audience map[string]struct{}
	scoped := ev.Broadcast.ChannelID != "" || ev.Broadcast.TeamID != ""
	if scoped {
		if audienceResolver == nil {
			return
		}
		resolved, err := audienceResolver(ctx, ev.Broadcast)
		if err != nil {
			return
		}
		audience = resolved
	}

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
			trySend(c, raw)
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
		trySend(c, raw)
	}
}

func trySend(c *Client, raw []byte) {
	select {
	case c.Send <- raw:
	default:
		// drop slow consumer
	}
}
