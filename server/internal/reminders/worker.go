package reminders

import (
	"context"
	"log/slog"
	"time"

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
	tickEvery  time.Duration
	batchLimit int
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
		if len(excerpt) > 140 {
			excerpt = excerpt[:140] + "…"
		}
	}

	w.hub.Broadcast(ws.Event{
		Event: "reminder_fired",
		Data: map[string]any{
			"reminder_id": r.ID,
			"post_id":     r.PostID,
			"channel_id":  channelID,
			"excerpt":     excerpt,
			"remind_at":   r.RemindAt,
		},
		Broadcast: ws.Broadcast{UserID: r.UserID},
	})

	if err := w.svc.MarkDelivered(fireCtx, r.ID, time.Now().UnixMilli()); err != nil {
		w.logger.Warn("reminder mark delivered", "id", r.ID, "err", err)
	}
}
