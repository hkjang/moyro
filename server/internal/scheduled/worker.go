package scheduled

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hkjang/moyro/server/internal/application/postcommand"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/ws"
	"github.com/jackc/pgx/v5"
)

const (
	postLookupTimeout = 5 * time.Second
	postCreateTimeout = 15 * time.Second
	fileUpdateTimeout = 10 * time.Second
	finalizeTimeout   = 5 * time.Second
)

// Dispatcher is the duplicate-post-safe scheduled subset of posts.Service.
type Dispatcher interface {
	GetByScheduledPost(ctx context.Context, scheduledPostID string) (*posts.Post, error)
	UpdateFileIDs(ctx context.Context, postID string, fileIDs []string) error
}

// CommandExecutor applies the common post lifecycle while keeping the worker
// independent from the concrete application service.
type CommandExecutor interface {
	Execute(ctx context.Context, command postcommand.Command) (*posts.Post, error)
}

type ScheduledStore interface {
	ClaimDue(ctx context.Context, now int64, limit int) ([]*ScheduledPost, error)
	MarkSent(ctx context.Context, id, claimToken, resultPostID string, sentAt int64) error
	MarkFailed(ctx context.Context, id, claimToken, errCode, errText string, attemptCount int, failedAt int64) error
}

type Broadcaster interface {
	Broadcast(event ws.Event)
}

// FileAssociator is optional — if set, the worker re-associates file ids
// with the new post id so they become downloadable through the post. We
// accept an interface so the worker doesn't import files.
type FileAssociator interface {
	AssociateWithPost(ctx context.Context, userID string, fileIDs []string, postID, channelID string) ([]string, error)
}

// Worker polls scheduled_posts on a 30s tick, claims due rows, and hands them
// to the common post command service. The command emits the usual `posted`
// event; the worker emits `scheduled_post_sent` after the claim-token CAS so
// the author's 예약됨 sidebar can drop the row without a refetch.
type Worker struct {
	svc          ScheduledStore
	posts        Dispatcher
	postCommands CommandExecutor
	files        FileAssociator
	hub          Broadcaster
	logger       *slog.Logger
	tickEvery    time.Duration
	// batchLimit is the max rows claimed per tick. Keeps a single tick
	// from monopolising the DB if a backlog appears.
	batchLimit int
}

func NewWorker(svc ScheduledStore, dispatcher Dispatcher, postCommands CommandExecutor, files FileAssociator, hub Broadcaster, logger *slog.Logger) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{
		svc:          svc,
		posts:        dispatcher,
		postCommands: postCommands,
		files:        files,
		hub:          hub,
		logger:       logger,
		tickEvery:    30 * time.Second,
		batchLimit:   50,
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
	p, created, err := w.resolveOrCreate(ctx, sp)
	if err != nil {
		w.logger.Warn("scheduled dispatch create", "id", sp.ID, "err", err)
		w.markFailed(ctx, sp, "post_create_failed", err)
		return
	}

	// The common command associates files on a fresh create. On replay, repair
	// an interrupted create that committed the post before file association.
	if !created && w.files != nil && len(sp.FileIDs) > 0 {
		fileCtx, cancel := freshWorkerContext(ctx, fileUpdateTimeout)
		attached, aerr := w.files.AssociateWithPost(fileCtx, sp.UserID, sp.FileIDs, p.ID, p.ChannelID)
		if aerr != nil {
			w.logger.Warn("scheduled file associate", "post", p.ID, "err", aerr)
		} else {
			// AssociateWithPost returns the canonical requested subset even when
			// an earlier attempt already committed the associations. Always write
			// it back, including an empty slice, to remove stale foreign ids.
			p.FileIDs = attached
			if err := w.posts.UpdateFileIDs(fileCtx, p.ID, attached); err != nil {
				w.logger.Warn("scheduled post update file_ids", "post", p.ID, "err", err)
			}
		}
		cancel()
	}

	// Finalize on a context independent from create/file timeouts. Only the
	// current claim token may transition the row or emit success events.
	finalizeCtx, cancel := freshWorkerContext(ctx, finalizeTimeout)
	err = w.svc.MarkSent(finalizeCtx, sp.ID, sp.ClaimToken, p.ID, time.Now().UnixMilli())
	cancel()
	if err != nil {
		if errors.Is(err, ErrStaleClaim) {
			w.logger.Warn("scheduled stale claim", "id", sp.ID, "post", p.ID)
		} else {
			w.logger.Warn("scheduled mark sent", "id", sp.ID, "err", err)
		}
		return
	}

	if w.hub == nil {
		return
	}
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
}

func (w *Worker) resolveOrCreate(ctx context.Context, sp *ScheduledPost) (*posts.Post, bool, error) {
	lookupCtx, cancel := freshWorkerContext(ctx, postLookupTimeout)
	p, err := w.posts.GetByScheduledPost(lookupCtx, sp.ID)
	cancel()
	if err == nil {
		return p, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("lookup scheduled result: %w", err)
	}

	createCtx, cancel := freshWorkerContext(ctx, postCreateTimeout)
	p, err = w.postCommands.Execute(createCtx, postcommand.Command{
		Source:          postcommand.SourceScheduled,
		ActorID:         sp.UserID,
		ChannelID:       sp.ChannelID,
		RootID:          sp.RootID,
		Message:         sp.Message,
		Props:           sp.Props,
		FileIDs:         sp.FileIDs,
		ScheduledPostID: sp.ID,
	})
	cancel()
	if err == nil {
		return p, true, nil
	}

	// A timeout or unique violation can mean another lease committed first.
	// Resolve once more on a fresh context before recording a retry.
	replayCtx, replayCancel := freshWorkerContext(ctx, postLookupTimeout)
	replayed, replayErr := w.posts.GetByScheduledPost(replayCtx, sp.ID)
	replayCancel()
	if replayErr == nil {
		return replayed, false, nil
	}
	return nil, false, fmt.Errorf("create scheduled post: %w (replay lookup: %v)", err, replayErr)
}

func (w *Worker) markFailed(ctx context.Context, sp *ScheduledPost, code string, cause error) {
	finalizeCtx, cancel := freshWorkerContext(ctx, finalizeTimeout)
	defer cancel()
	if err := w.svc.MarkFailed(finalizeCtx, sp.ID, sp.ClaimToken, code, cause.Error(), sp.AttemptCount, time.Now().UnixMilli()); err != nil {
		if errors.Is(err, ErrStaleClaim) {
			w.logger.Warn("scheduled failure stale claim", "id", sp.ID)
			return
		}
		w.logger.Warn("scheduled mark failed", "id", sp.ID, "err", err)
	}
}

func freshWorkerContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), timeout)
}
