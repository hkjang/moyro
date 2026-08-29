package scheduled

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/ws"
)

// Dispatcher is what the worker needs from posts.Service. Defined here so
// callers construct with a concrete *posts.Service without the worker
// pulling in the whole posts package surface.
type Dispatcher interface {
	Create(ctx context.Context, channelID, userID, rootID, message string, props map[string]any, fileIDs []string) (*posts.Post, error)
	UpdateFileIDs(ctx context.Context, postID string, fileIDs []string) error
}

// FileAssociator is optional — if set, the worker re-associates file ids
// with the new post id so they become downloadable through the post. We
// accept an interface so the worker doesn't import files.
type FileAssociator interface {
	AssociateWithPost(ctx context.Context, userID string, fileIDs []string, postID, channelID string) ([]string, error)
}

// Worker polls scheduled_posts on a 30s tick, claims due rows, and hands
// them to the posts service. Broadcasts the usual `posted` WS event so
// clients render the delivered message immediately, and emits a
// `scheduled_post_sent` event to the author's own sockets so the
// 예약됨 sidebar can drop the row without a refetch.
type Worker struct {
	svc       *Service
	posts     Dispatcher
	files     FileAssociator
	hub       *ws.Hub
	logger    *slog.Logger
	tickEvery time.Duration
	// batchLimit is the max rows claimed per tick. Keeps a single tick
	// from monopolising the DB if a backlog appears.
	batchLimit int
}

func NewWorker(svc *Service, dispatcher Dispatcher, files FileAssociator, hub *ws.Hub, logger *slog.Logger) *Worker {
	return &Worker{
		svc:        svc,
		posts:      dispatcher,
		files:      files,
		hub:        hub,
		logger:     logger,
		tickEvery:  30 * time.Second,
		batchLimit: 50,
	}
}

func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(w.tickEvery)
	defer t.Stop()
	// First tick after a short delay so a server that boots with due rows
	// doesn't wait a full minute before the user sees anything.
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
			w.logger.Warn("scheduled worker panic", "err", rec)
		}
	}()
	claimCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	due, err := w.svc.ClaimDue(claimCtx, time.Now().UnixMilli(), w.batchLimit)
	if err != nil {
		w.logger.Warn("scheduled claim", "err", err)
		return
	}
	for _, sp := range due {
		w.dispatch(ctx, sp)
	}
}

func (w *Worker) dispatch(ctx context.Context, sp *ScheduledPost) {
	sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	p, err := w.posts.Create(sendCtx, sp.ChannelID, sp.UserID, sp.RootID, sp.Message, sp.Props, sp.FileIDs)
	if err != nil {
		w.logger.Warn("scheduled dispatch create", "id", sp.ID, "err", err)
		_ = w.svc.MarkFailed(sendCtx, sp.ID, err.Error())
		return
	}
	// Reassociate files if any. Scheduled posts captured file_ids at
	// enqueue time; they stayed orphan until now, so we flip them to the
	// new post id just like the live createPost handler does.
	if w.files != nil && len(sp.FileIDs) > 0 {
		attached, aerr := w.files.AssociateWithPost(sendCtx, sp.UserID, sp.FileIDs, p.ID, p.ChannelID)
		if aerr != nil {
			w.logger.Warn("scheduled file associate", "post", p.ID, "err", aerr)
		} else {
			p.FileIDs = attached
			if err := w.posts.UpdateFileIDs(sendCtx, p.ID, attached); err != nil {
				w.logger.Warn("scheduled post update file_ids", "post", p.ID, "err", err)
			}
		}
	}

	raw, _ := json.Marshal(p)
	// Standard posted event to the channel.
	w.hub.Broadcast(ws.Event{
		Event: "posted",
		Data: map[string]any{
			"channel_id":   p.ChannelID,
			"channel_name": "",
			"post":         string(raw),
			"sender_name":  "",
			"team_id":      "",
			"mentions":     "[]",
		},
		Broadcast: ws.Broadcast{ChannelID: p.ChannelID},
	})
	// Author-scoped event so the 예약됨 sidebar drops this row live.
	w.hub.Broadcast(ws.Event{
		Event: "scheduled_post_sent",
		Data: map[string]any{
			"scheduled_post_id": sp.ID,
			"post_id":           p.ID,
			"channel_id":        p.ChannelID,
		},
		Broadcast: ws.Broadcast{UserID: sp.UserID},
	})

	if err := w.svc.MarkSent(sendCtx, sp.ID, time.Now().UnixMilli()); err != nil {
		w.logger.Warn("scheduled mark sent", "id", sp.ID, "err", err)
	}
}
