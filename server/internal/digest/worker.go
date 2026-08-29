// Package digest schedules + renders the daily "you have unread mentions"
// email. It is intentionally simple: a single goroutine, a 5-minute tick,
// a window gate on server-local 9 AM, and a per-user `last_digest_at` guard
// in `users.email_prefs` so we never double-send.
//
// Opt-out is honoured via `email_prefs->>'digest_enabled' = 'false'`.
// Users with empty email_prefs are considered opted in (default true).
package digest

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/hkjang/moyro/server/internal/email"
	"github.com/hkjang/moyro/server/internal/store"
)

type Worker struct {
	db      *store.DB
	sender  email.Sender
	logger  *slog.Logger
	baseURL string
	// hour is the server-local hour at which a digest becomes eligible.
	// 9 = 9 AM. Exposed as a field (not a const) for test time travel.
	hour int
	// tickEvery controls the scheduler tick. 5 min in prod; tests can
	// shorten this to speed up assertions.
	tickEvery time.Duration
}

func NewWorker(db *store.DB, sender email.Sender, logger *slog.Logger, baseURL string) *Worker {
	return &Worker{
		db:        db,
		sender:    sender,
		logger:    logger,
		baseURL:   baseURL,
		hour:      9,
		tickEvery: 5 * time.Minute,
	}
}

// Run blocks until ctx is cancelled. Caller typically does `go w.Run(ctx)`.
func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(w.tickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.tick(ctx); err != nil {
				w.logger.Warn("digest tick", "err", err)
			}
		}
	}
}

// tick decides whether to fire today and iterates candidate users. One DB
// roundtrip for the candidate list, then per-user one query + one send.
func (w *Worker) tick(ctx context.Context) error {
	now := time.Now()
	if now.Hour() != w.hour {
		return nil
	}
	// Today's 00:00 in server-local TZ, as ms epoch. We compare against
	// last_digest_at to skip users already processed this day.
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()

	// Candidate query: users who
	//   (a) have a real email (not empty),
	//   (b) haven't explicitly disabled digests,
	//   (c) have at least one channel_members row with mention_count>0,
	//   (d) last_digest_at is either unset or < today's start.
	// NOTE: we don't query last_activity_at here — presence is noisy and
	// the mention_count gate alone is a good enough signal for "they
	// missed something worth reading".
	rows, err := w.db.Pool.Query(ctx, `
		SELECT u.id, u.username, u.email
		FROM users u
		WHERE u.delete_at = 0
		  AND u.email <> ''
		  AND COALESCE((u.email_prefs ->> 'digest_enabled')::text, 'true') <> 'false'
		  AND COALESCE((u.email_prefs ->> 'last_digest_at')::bigint, 0) < $1
		  AND EXISTS (
		      SELECT 1 FROM channel_members cm
		      WHERE cm.user_id = u.id AND cm.mention_count > 0
		  )
	`, todayStart)
	if err != nil {
		return err
	}
	defer rows.Close()
	type candidate struct {
		ID, Username, Email string
	}
	var cands []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.ID, &c.Username, &c.Email); err != nil {
			return err
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, c := range cands {
		if err := w.processUser(ctx, c.ID, c.Username, c.Email); err != nil {
			w.logger.Warn("digest user", "user", c.ID, "err", err)
			// Don't update last_digest_at on failure — we'll retry on
			// next tick.
			continue
		}
	}
	return nil
}

func (w *Worker) processUser(ctx context.Context, userID, username, userEmail string) error {
	mentions, err := w.loadMentions(ctx, userID)
	if err != nil {
		return err
	}
	if len(mentions) == 0 {
		// Counter said we had something but the detail query found
		// nothing (deleted posts, races). Still stamp last_digest_at so
		// we don't loop all day.
		return w.stamp(ctx, userID)
	}
	subj, html, text, err := email.RenderDigest(email.DigestData{
		Recipient: username,
		BaseURL:   w.baseURL,
		Mentions:  mentions,
	})
	if err != nil {
		return err
	}
	sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := w.sender.Send(sendCtx, userEmail, subj, html, text); err != nil {
		return err
	}
	return w.stamp(ctx, userID)
}

// loadMentions returns up to 20 recent posts that mentioned the user
// (signals: the post appears in one of their channels AND that channel's
// mention_count is currently nonzero). The detail SQL approximates what
// the client sees in its sidebar badges.
func (w *Worker) loadMentions(ctx context.Context, userID string) ([]email.DigestMention, error) {
	rows, err := w.db.Pool.Query(ctx, `
		SELECT u.username, c.display_name, p.message, p.create_at
		FROM channel_members cm
		JOIN channels c ON c.id = cm.channel_id AND c.delete_at = 0
		JOIN posts p ON p.channel_id = cm.channel_id
		JOIN users u ON u.id = p.user_id
		WHERE cm.user_id = $1
		  AND cm.mention_count > 0
		  AND p.delete_at = 0
		  AND p.create_at > (SELECT COALESCE(cm2.last_viewed_at, 0)
		                    FROM channel_members cm2
		                    WHERE cm2.user_id = $1 AND cm2.channel_id = c.id)
		ORDER BY p.create_at DESC
		LIMIT 20
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []email.DigestMention{}
	for rows.Next() {
		var uname, ch, msg string
		var postedAt int64
		if err := rows.Scan(&uname, &ch, &msg, &postedAt); err != nil {
			return nil, err
		}
		excerpt := msg
		if len(excerpt) > 160 {
			excerpt = excerpt[:160] + "…"
		}
		out = append(out, email.DigestMention{
			Username:    uname,
			ChannelName: ch,
			Excerpt:     excerpt,
			PostedAt:    time.UnixMilli(postedAt).Local().Format("1/2 15:04"),
		})
	}
	return out, rows.Err()
}

// stamp writes last_digest_at = now into the user's email_prefs JSONB.
// jsonb_set is the cleanest way to mutate one key without rewriting the
// whole blob or clobbering other prefs.
func (w *Worker) stamp(ctx context.Context, userID string) error {
	val, _ := json.Marshal(time.Now().UnixMilli())
	_, err := w.db.Pool.Exec(ctx, `
		UPDATE users
		SET email_prefs = jsonb_set(
		    COALESCE(email_prefs, '{}'::jsonb),
		    '{last_digest_at}',
		    $2::jsonb,
		    true)
		WHERE id = $1
	`, userID, string(val))
	return err
}
