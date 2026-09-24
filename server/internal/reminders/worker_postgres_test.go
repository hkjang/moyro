package reminders

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/hkjang/moyro/server/internal/ws"
)

// The worker that claimed a reminder can stop before stamping delivered_at —
// a deploy, a crash, a lost database connection. The next worker must pick the
// abandoned claim back up and deliver it instead of leaving it in flight
// forever.
func TestWorkerRedeliversAReminderLeftInFlightByAStoppedWorker(t *testing.T) {
	db := newReminderTestDB(t)
	ctx := reminderTestContext(t)
	seedReminderTestFixtures(t, ctx, db)
	svc := New(db)
	post := createReminderTestPost(t, ctx, db, "잊지 말 것")

	rem, err := svc.Create(ctx, reminderTestUserID, post.ID, time.Now().UTC().UnixMilli()-1_000)
	if err != nil {
		t.Fatalf("create reminder: %v", err)
	}
	claimed, err := svc.ClaimDue(ctx, time.Now().UTC().UnixMilli(), 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("first worker claim = %d, %v", len(claimed), err)
	}

	// That worker is gone; its lease ran out while nothing delivered the row.
	abandonReminderClaim(t, ctx, db, rem.ID)

	client := startReminderTestWorkerHub(t, ctx, db)
	worker := NewWorker(svc, posts.New(db), client.hub, discardLogger())
	worker.tick(ctx)

	payload := awaitReminderEvent(t, client.client)
	if payload["reminder_id"] != rem.ID || payload["post_id"] != post.ID || payload["channel_id"] != reminderTestChannelID {
		t.Fatalf("redelivered reminder payload = %#v", payload)
	}
	if payload["excerpt"] != "잊지 말 것" {
		t.Fatalf("redelivered reminder excerpt = %#v", payload["excerpt"])
	}

	var deliveredAt, claimedAt int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT delivered_at, claimed_at FROM post_reminders WHERE id=$1
	`, rem.ID).Scan(&deliveredAt, &claimedAt); err != nil {
		t.Fatalf("read redelivered reminder row: %v", err)
	}
	if deliveredAt <= 0 || claimedAt != 0 {
		t.Fatalf("redelivered reminder row = (delivered_at %d, claimed_at %d)", deliveredAt, claimedAt)
	}
}

// Shutdown cancels the context the dispatch loop runs on. The broadcast has
// already left by the time delivery is stamped, so the stamp must still land —
// otherwise the lease expires and a later worker broadcasts the same reminder
// a second time.
func TestWorkerStampsDeliveryWhenTheDispatchContextIsAlreadyDone(t *testing.T) {
	db := newReminderTestDB(t)
	ctx := reminderTestContext(t)
	seedReminderTestFixtures(t, ctx, db)
	svc := New(db)
	post := createReminderTestPost(t, ctx, db, "종료 중 전달")

	rem, err := svc.Create(ctx, reminderTestUserID, post.ID, time.Now().UTC().UnixMilli()-1_000)
	if err != nil {
		t.Fatalf("create reminder: %v", err)
	}
	claimed, err := svc.ClaimDue(ctx, time.Now().UTC().UnixMilli(), 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim due reminders = %d, %v", len(claimed), err)
	}

	client := startReminderTestWorkerHub(t, ctx, db)
	worker := NewWorker(svc, posts.New(db), client.hub, discardLogger())

	stopped, cancel := context.WithCancel(ctx)
	cancel()
	worker.fire(stopped, claimed[0])

	var deliveredAt, claimedAt int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT delivered_at, claimed_at FROM post_reminders WHERE id=$1
	`, rem.ID).Scan(&deliveredAt, &claimedAt); err != nil {
		t.Fatalf("read reminder row: %v", err)
	}
	if deliveredAt <= 0 || claimedAt != 0 {
		t.Fatalf("reminder row after shutdown dispatch = (delivered_at %d, claimed_at %d), want a delivery stamp and no lease", deliveredAt, claimedAt)
	}
}

type reminderTestClient struct {
	hub    *ws.Hub
	client *ws.Client
}

// startReminderTestWorkerHub runs a hub wired with the production audience
// resolver and one registered socket for the reminder owner.
func startReminderTestWorkerHub(t *testing.T, ctx context.Context, db *store.DB) reminderTestClient {
	t.Helper()
	hub := ws.NewHub()
	hub.SetAudienceResolver(ws.DatabaseAudienceResolver(db))
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go hub.Run(runCtx)

	client := &ws.Client{UserID: reminderTestUserID, Send: make(chan []byte, 4)}
	hub.Register(client)
	deadline := time.Now().Add(2 * time.Second)
	for hub.ClientCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if hub.ClientCount() != 1 {
		t.Fatalf("registered websocket clients = %d, want 1", hub.ClientCount())
	}
	return reminderTestClient{hub: hub, client: client}
}

func awaitReminderEvent(t *testing.T, client *ws.Client) map[string]any {
	t.Helper()
	select {
	case raw := <-client.Send:
		var envelope struct {
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("decode reminder event %s: %v", raw, err)
		}
		if envelope.Event != "reminder_fired" {
			t.Fatalf("websocket event = %q, want reminder_fired", envelope.Event)
		}
		return envelope.Data
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the redelivered reminder")
		return nil
	}
}

func abandonReminderClaim(t *testing.T, ctx context.Context, db *store.DB, id string) {
	t.Helper()
	expired := time.Now().UTC().UnixMilli() - claimLeaseDuration.Milliseconds() - 1_000
	tag, err := db.Pool.Exec(ctx, `
		UPDATE post_reminders SET claimed_at=$1 WHERE id=$2 AND delivered_at = -1
	`, expired, id)
	if err != nil {
		t.Fatalf("expire reminder claim %s: %v", id, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("expired %d in-flight reminder rows, want 1", tag.RowsAffected())
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
