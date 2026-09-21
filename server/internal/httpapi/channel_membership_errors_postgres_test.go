package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/audit"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
)

func TestChannelViewsAndTimezonesMembershipErrorsPostgres(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	seedSidebarHandlerFixture(t, ctx, db)
	if _, err := db.Pool.Exec(ctx, `INSERT INTO users (id, username, email, password_hash, create_at, update_at)
 VALUES ('outsider', 'outsider', 'outsider@example.test', 'unused', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	h := &handlers{channels: channels.New(db), audit: audit.New(db, slog.Default())}
	endpoints := []struct {
		name, method, suffix string
		handler              http.HandlerFunc
		success              int
	}{
		{"list views", http.MethodGet, "views", h.listChannelViews, 200},
		{"create view", http.MethodPost, "views", h.createChannelView, 201},
		{"list timezones", http.MethodGet, "timezones", h.listChannelTimezones, 200},
	}
	var createdID string
	for _, endpoint := range endpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			for _, tc := range []struct {
				name, actor, channel string
				status               int
				message              string
			}{
				{"missing user", "", "chan-alpha", 401, "missing token"},
				{"nonmember", "outsider", "chan-alpha", 403, "not a channel member"},
				{"missing channel", "user-a", "missing", 403, "not a channel member"},
				{"member", "user-a", "chan-alpha", endpoint.success, ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					rr := invokeChannelScopeHandler(ctx, endpoint.handler, endpoint.method,
						"/api/v4/channels/"+tc.channel+"/"+endpoint.suffix, tc.actor, map[string]string{"channelID": tc.channel}, "")
					if rr.Code != tc.status {
						t.Fatalf("status = %d, want %d: %s", rr.Code, tc.status, rr.Body.String())
					}
					if tc.message != "" {
						id := "api.context.permissions.app_error"
						if tc.status == 401 {
							id = "api.context.session_expired.app_error"
						}
						assertMembershipAPIError(t, rr.Body.Bytes(), tc.status, id, tc.message)
					} else if endpoint.success == 200 {
						if strings.TrimSpace(rr.Body.String()) != "[]" {
							t.Fatalf("body = %s, want []", rr.Body.String())
						}
					} else {
						var view struct {
							ID        string `json:"id"`
							ChannelID string `json:"channel_id"`
							UserID    string `json:"user_id"`
							CreateAt  int64  `json:"create_at"`
							UpdateAt  int64  `json:"update_at"`
						}
						if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
							t.Fatal(err)
						}
						if !strings.HasPrefix(view.ID, "view-") || view.ChannelID != tc.channel || view.UserID != tc.actor || view.CreateAt <= 0 || view.UpdateAt != view.CreateAt {
							t.Fatalf("unexpected view: %+v", view)
						}
						createdID = view.ID
					}
				})
			}
		})
	}
	// Observe the real asynchronous success audit before inducing the outage.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var count int
		if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action=$1 AND actor_id='user-a' AND target=$2`, audit.ActionViewCreate, "chan-alpha:"+createdID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("successful create audit did not arrive")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Qualify the destructive operation and refuse any non-test schema.
	var schema string
	if err := db.Pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(schema, "moyro_operations_") {
		t.Fatalf("unsafe schema: %q", schema)
	}
	if _, err := db.Pool.Exec(ctx, "DROP TABLE "+pgx.Identifier{schema, "channel_members"}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range endpoints {
		t.Run(endpoint.name+" database failure", func(t *testing.T) {
			rr := invokeChannelScopeHandler(ctx, endpoint.handler, endpoint.method,
				"/api/v4/channels/chan-alpha/"+endpoint.suffix, "user-a", map[string]string{"channelID": "chan-alpha"}, "")
			if rr.Code != 500 {
				t.Fatalf("status = %d, want 500: %s", rr.Code, rr.Body.String())
			}
			assertMembershipAPIError(t, rr.Body.Bytes(), 500, "api.context.permissions.app_error", "failed to check channel membership")
		})
	}
	// Watch beyond LogAsync's three-second DB timeout for accidental success logs.
	deadline = time.Now().Add(3200 * time.Millisecond)
	for {
		var count int
		if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action=$1`, audit.ActionViewCreate).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("view creation audit count = %d, want only the successful request", count)
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func assertMembershipAPIError(t *testing.T, body []byte, status int, id, message string) {
	t.Helper()
	var got apiError
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got != (apiError{ID: id, Message: message, StatusCode: status}) {
		t.Fatalf("unexpected API error: %+v", got)
	}
}
