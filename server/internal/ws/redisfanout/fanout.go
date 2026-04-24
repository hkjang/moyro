// Package redisfanout connects the local `ws.Hub` to a Redis pub/sub
// channel so events created on one Moddle instance reach clients connected
// to sibling instances behind a load balancer. The wire format is an
// envelope (`{origin, event}`) rather than the raw `ws.Event` to keep the
// origin-instance identifier off the wire to browsers.
//
// Design:
//
//	local Broadcast(ev)
//	   ├── hub.bcast → fanout to local sockets
//	   └── Publisher.Publish(ev) → Redis PUBLISH envelope{Origin=me, Event=ev}
//
//	remote PUBLISH arrives
//	   └── Subscriber decodes envelope
//	          ├── if envelope.Origin == me → drop (our own echo)
//	          └── else → Hub.InjectEvent(ev) → fanout to local sockets
//
// Fail-open: if Redis is unreachable at startup, Dial returns (nil, err);
// the caller logs a warning and the server runs single-instance. Publish
// errors on a dialed-but-later-failing Redis are logged at most once per
// second and silently dropped — a dead Redis must never stall the hub.
package redisfanout

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/moddle/moddle/server/internal/ws"
)

// Channel is the Redis pub/sub channel name. Hard-coded so all Moddle
// instances pointing at the same Redis see each other without a config.
const Channel = "moddle.ws"

// envelope wraps a `ws.Event` for the wire so the origin-instance id can
// ride along without polluting the Event struct that goes out to browsers.
type envelope struct {
	Origin string   `json:"origin"`
	Event  ws.Event `json:"event"`
}

// Fanout holds the redis client, this instance's origin id, and the
// subscription goroutine's cancel handle. Hold onto one per process.
type Fanout struct {
	client *redis.Client
	origin string
	logger *slog.Logger

	// rate-limit the publish-error log to once per second
	lastErrLog atomic.Int64
}

// Dial opens a Redis connection, PINGs it to fail fast on a misconfigured
// URL, and returns a Fanout ready to Publish + Subscribe. The caller must
// call Close on shutdown. `url` is the `redis://` URL from cfg.RedisURL.
func Dial(ctx context.Context, url string, logger *slog.Logger) (*Fanout, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(opts)
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	return &Fanout{
		client: client,
		origin: uuid.NewString(),
		logger: logger,
	}, nil
}

// Publish serialises and PUBLISHes the event. Non-blocking from the
// caller's POV (the hub calls us in a goroutine). Errors are rate-limited
// to avoid log-spam when Redis goes down mid-flight.
func (f *Fanout) Publish(ev ws.Event) {
	if f == nil || f.client == nil {
		return
	}
	raw, err := json.Marshal(envelope{Origin: f.origin, Event: ev})
	if err != nil {
		// Marshalling a ws.Event should never fail, but don't crash.
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := f.client.Publish(ctx, Channel, raw).Err(); err != nil {
		f.logOnce("redis publish", err)
	}
}

// Subscribe starts a background goroutine that tails Channel and calls
// `inject` for every envelope whose origin differs from ours. The
// goroutine exits when ctx is cancelled. Returns the underlying
// PubSub so callers can also .Close() it on shutdown.
func (f *Fanout) Subscribe(ctx context.Context, inject func(ws.Event)) *redis.PubSub {
	sub := f.client.Subscribe(ctx, Channel)
	ch := sub.Channel()
	go func() {
		for {
			select {
			case <-ctx.Done():
				_ = sub.Close()
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				var env envelope
				if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
					f.logOnce("redis envelope decode", err)
					continue
				}
				if env.Origin == f.origin {
					// Our own echo — drop. Without this we'd deliver every
					// event twice on the origin instance.
					continue
				}
				inject(env.Event)
			}
		}
	}()
	return sub
}

// Close severs the Redis connection. Safe to call multiple times.
func (f *Fanout) Close() error {
	if f == nil || f.client == nil {
		return nil
	}
	return f.client.Close()
}

// Origin is exposed for tests that need to inspect the self-id.
func (f *Fanout) Origin() string { return f.origin }

// logOnce rate-limits error logging to at most one line per second per
// Fanout. Prevents log explosion when Redis is wedged and every post
// triggers a publish failure.
func (f *Fanout) logOnce(msg string, err error) {
	if f.logger == nil {
		return
	}
	now := time.Now().UnixMilli()
	prev := f.lastErrLog.Load()
	if now-prev < 1000 {
		return
	}
	if f.lastErrLog.CompareAndSwap(prev, now) {
		f.logger.Warn(msg, "err", err)
	}
}

// guard against accidental unused-sync import in earlier drafts; kept
// because a future enhancement (reconnect on dial errors) will want
// sync.Once.
var _ = sync.Once{}
