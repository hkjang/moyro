package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/auth"
	eventmail "github.com/hkjang/moyro/server/internal/mail"
	"github.com/hkjang/moyro/server/internal/store"
)

// Every attempt is written down, sent or failed, with the recipient and the
// subject but never the body; and the recipient directory is the account
// table, minus people who turned event mail off.
func TestMailDeliveryLogAndDirectory(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	users := auth.New(db, []byte("test-secret-test-secret-test-secret"), time.Hour, nil)
	reviewer, err := users.Register(ctx, "reviewer", "reviewer@corp.example", "password-123456")
	if err != nil {
		t.Fatal(err)
	}
	quiet, err := users.Register(ctx, "quiet", "quiet@corp.example", "password-123456")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE users SET email_prefs = '{"events_enabled": false}'::jsonb WHERE id = $1`, quiet.ID); err != nil {
		t.Fatal(err)
	}

	directory := mailDirectory{db: db}
	addresses, err := directory.LookupEmails(ctx, []string{reviewer.ID, quiet.ID, "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 1 || addresses[reviewer.ID] != "reviewer@corp.example" {
		t.Fatalf("directory = %v, want only the reviewer", addresses)
	}

	recorder := &mailSendRecorder{}
	service := eventmail.New(db, directory, nil)
	service.SetSender(recorder.send)
	service.SetBundleWindow(10 * time.Millisecond)
	config := eventmail.Default()
	config.Enabled, config.SMTPHost, config.FromAddress = true, "relay.corp.example", "moyro@corp.example"
	service.Configure(config)

	notify := func(recipient string) {
		service.Notify(ctx, eventmail.Notification{
			Event: eventmail.EventApprovalRequested, RecipientID: recipient, ActorID: "requester",
			Subject: "[moyro] 검토할 승인 요청: 운영 공지", Lines: []string{"검토할 승인 요청: 운영 공지", "비밀 본문"},
			ResourceType: "approval_review", ResourceID: "request-1",
		})
	}
	notify(reviewer.ID)
	notify(quiet.ID)
	service.Wait()
	recorder.fail = errors.New("smtp connect: dial tcp: connection refused")
	notify(reviewer.ID)
	service.Wait()

	page, err := service.Deliveries(ctx, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Summary.Total != 2 || page.Summary.Status[eventmail.StatusSent] != 1 || page.Summary.Status[eventmail.StatusFailed] != 1 {
		t.Fatalf("deliveries = %+v", page)
	}
	failed, sent := page.Items[0], page.Items[1]
	if failed.Status != eventmail.StatusFailed || failed.Attempts != 2 || failed.ErrorMessage == "" || failed.Recipient != "reviewer@corp.example" {
		t.Fatalf("failed delivery = %+v", failed)
	}
	if sent.Status != eventmail.StatusSent || sent.Attempts != 1 || sent.Event != eventmail.EventApprovalRequested ||
		sent.RecipientID != reviewer.ID || sent.Subject != "[moyro] 검토할 승인 요청: 운영 공지" || sent.ResourceID != "request-1" {
		t.Fatalf("sent delivery = %+v", sent)
	}
	var columns []string
	rows, err := db.Pool.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_name = 'mail_deliveries'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	rows.Close()
	for _, name := range columns {
		if name == "body" || name == "text" || name == "html" {
			t.Fatalf("delivery log keeps message bodies: %v", columns)
		}
	}
	filtered, err := service.Deliveries(ctx, eventmail.StatusFailed, 10)
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].ID != failed.ID {
		t.Fatalf("status filter = %+v, %v", filtered, err)
	}
}
