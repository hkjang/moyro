package reminders

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hkjang/moyro/server/internal/activityevents"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/ws"
)

// PostResolver returns the minimum post fields the worker needs for the
// reminder payload. Kept as an interface so the worker doesn't pin a
// concrete dependency on posts.Service at type-level.
type PostResolver interface {
	Get(ctx context.Context, postID string) (*posts.Post, error)
}

// Worker polls post_reminders on a 30s tick. Each claimed row gets a
// reminder_fired WS event delivered to the owning user's sockets.
type Worker struct {
	svc        *Service
	posts      PostResolver
	hub        *ws.Hub
	logger     *slog.Logger
	activity   activityevents.Emitter
	tickEvery  time.Duration
	batchLimit int
}

// SetActivityEmitter enables the durable integrated inbox without changing
// the long-standing worker constructor used by tests and embedders.
func (w *Worker) SetActivityEmitter(emitter activityevents.Emitter) {
	w.activity = emitter
}

func NewWorker(svc *Service, resolver PostResolver, hub *ws.Hub, logger *slog.Logger) *Worker {
	return &Worker{
		svc:        svc,
		posts:      resolver,
		hub:        hub,
		logger:     logger,
		tickEvery:  30 * time.Second,
		batchLimit: 50,
	}
}

func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(w.tickEvery)
	defer t.Stop()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			w.tick(ctx)
		case <-t.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			w.logger.Warn("reminders worker panic", "err", rec)
		}
	}()
	claimCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	due, err := w.svc.ClaimDue(claimCtx, time.Now().UnixMilli(), w.batchLimit)
	if err != nil {
		w.logger.Warn("reminders claim", "err", err)
		return
	}
	for _, r := range due {
		w.fire(ctx, r)
	}
}

func (w *Worker) fire(ctx context.Context, r *Reminder) {
	fireCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	excerpt := ""
	channelID := ""
	p, err := w.posts.Get(fireCtx, r.PostID)
	if err != nil || p == nil {
		// Post vanished; deliver an empty payload so the client can still
		// dismiss the row. The post_id stays so a user curious about it
		// can look it up; the client should gracefully handle missing.
	} else {
		channelID = p.ChannelID
		excerpt = p.Message
		excerpt = reminderExcerpt(excerpt, 140)
	}

	if w.activity != nil {
		if _, err := w.activity.Emit(fireCtx, activityevents.EmitInput{
			UserID: r.UserID, Type: activityevents.TypeReminderFired, DedupeKey: r.ID,
			ChannelID: channelID, PostID: r.PostID,
			ResourceType: "reminder", ResourceID: r.ID,
			Title: "메시지 리마인더가 도착했습니다", Summary: excerpt,
		}); err != nil {
			w.logger.Warn("reminder activity event", "id", r.ID, "err", err)
		}
	}

	w.hub.Broadcast(reminderFiredEvent(r, channelID, excerpt))

	if err := w.svc.MarkDelivered(fireCtx, r.ID, time.Now().UnixMilli()); err != nil {
		w.logger.Warn("reminder mark delivered", "id", r.ID, "err", err)
	}
}

func reminderFiredEvent(r *Reminder, channelID, excerpt string) ws.Event {
	broadcast := ws.Broadcast{UserID: r.UserID}
	if channelID != "" {
		// A resolved post makes this channel-scoped data. Hub fanout intersects
		// the owner target with current membership and fails closed on lookup
		// errors, so a revoked member cannot receive the reminder payload.
		broadcast.ChannelID = channelID
	}
	return ws.Event{
		Event: "reminder_fired",
		Data: map[string]any{
			"reminder_id": r.ID,
			"post_id":     r.PostID,
			"channel_id":  channelID,
			"excerpt":     excerpt,
			"remind_at":   r.RemindAt,
		},
		Broadcast: broadcast,
	}
}

func reminderExcerpt(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit])) + "…"
}
