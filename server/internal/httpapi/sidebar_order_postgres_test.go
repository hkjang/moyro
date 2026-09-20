package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/sidebar"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/hkjang/moyro/server/internal/ws"
)

func sidebarOrderFixture(t *testing.T) (*store.DB, *handlers, []*ws.Client) {
	t.Helper()
	db := newOperationsTestDB(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	seedSidebarHandlerFixture(t, ctx, db)
	hub := ws.NewHub()
	hub.SetAudienceResolver(ws.DatabaseAudienceResolver(db))
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); hub.Run(runCtx) }()
	t.Cleanup(func() { stop(); <-done })
	clients := []*ws.Client{
		{UserID: "user-a", Send: make(chan []byte, 8)},
		{UserID: "user-a", Send: make(chan []byte, 8)},
		{UserID: "user-b", Send: make(chan []byte, 8)},
	}
	for _, c := range clients {
		hub.Register(c)
	}
	deadline := time.Now().Add(3 * time.Second)
	for hub.ClientCount() != len(clients) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if hub.ClientCount() != len(clients) {
		t.Fatal("WebSocket clients were not registered")
	}
	return db, &handlers{sidebar: sidebar.New(db), hub: hub}, clients
}

func assertSidebarOrderEvent(t *testing.T, clients []*ws.Client, want []string) {
	t.Helper()
	for _, c := range clients[:2] {
		select {
		case raw := <-c.Send:
			var event struct {
				Event string `json:"event"`
				Data  struct {
					TeamID string   `json:"team_id"`
					Order  []string `json:"order"`
				} `json:"data"`
				Broadcast ws.Broadcast `json:"broadcast"`
			}
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if event.Event != "sidebar_category_order_updated" || event.Data.TeamID != "team-main" || event.Broadcast.UserID != "user-a" || !reflect.DeepEqual(event.Data.Order, want) {
				t.Fatalf("event = %s, want order %v targeted at user-a", raw, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("missing sidebar order event")
		}
	}
	select {
	case raw := <-clients[2].Send:
		t.Fatalf("other user received %s", raw)
	case <-time.After(50 * time.Millisecond):
	}
}

func sidebarOrderResponse(t *testing.T, rr *httptest.ResponseRecorder) []string {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	var order []string
	if err := json.Unmarshal(rr.Body.Bytes(), &order); err != nil {
		t.Fatal(err)
	}
	if order == nil {
		t.Fatal("expected JSON array, not null")
	}
	return order
}

func TestSidebarOrderReturnsStoredOrderPostgres(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		name := "mixed partial order"
		if fresh {
			name = "empty first request"
		}
		t.Run(name, func(t *testing.T) {
			db, h, clients := sidebarOrderFixture(t)
			ctx := context.Background()
			input := []string{}
			if !fresh {
				_, err := db.Pool.Exec(ctx, `
     INSERT INTO users (id,username,email,password_hash,create_at,update_at) VALUES ('user-b','user-b','b@example.test','hash',1,1);
     INSERT INTO teams (id,name,display_name,create_at,update_at) VALUES ('team-other','team-other','Other',1,1);
     INSERT INTO sidebar_categories (id,user_id,team_id,type,display_name,sort_order,create_at,update_at) VALUES
     ('fav','user-a','team-main','favorites','Favorites',0,1,1),
     ('channels','user-a','team-main','channels','Channels',10,2,1),
     ('dm','user-a','team-main','direct_messages','Direct Messages',20,3,1),
     ('first','user-a','team-main','custom','First',30,4,1),
     ('second','user-a','team-main','custom','Second',40,5,1),
     ('foreign-user','user-b','team-main','custom','Foreign user',70,6,1),
     ('foreign-team','user-a','team-other','custom','Foreign team',80,7,1)`)
				if err != nil {
					t.Fatal(err)
				}
				input = []string{"second", "first", "second", "ghost", "foreign-user", "foreign-team", ""}
			}
			// An empty PUT must also return all existing rows on the second pass.
			for _, payload := range [][]string{input, {}} {
				rr := httptest.NewRecorder()
				h.updateSidebarCategoryOrder(rr, sidebarCategoriesRequest(t, http.MethodPut, "order", payload))
				got := sidebarOrderResponse(t, rr)
				get := httptest.NewRecorder()
				h.listSidebarCategoryOrder(get, sidebarCategoriesRequest(t, http.MethodGet, "order", nil))
				if want := sidebarOrderResponse(t, get); !reflect.DeepEqual(got, want) {
					t.Fatalf("PUT order %v != GET order %v", got, want)
				}
				rows, err := db.Pool.Query(ctx, `SELECT id FROM sidebar_categories WHERE user_id='user-a' AND team_id='team-main' ORDER BY sort_order,create_at`)
				if err != nil {
					t.Fatal(err)
				}
				stored := []string{}
				for rows.Next() {
					var id string
					if err := rows.Scan(&id); err != nil {
						t.Fatal(err)
					}
					stored = append(stored, id)
				}
				rows.Close()
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, stored) {
					t.Fatalf("PUT %v != DB %v", got, stored)
				}
				if fresh && len(got) != 3 {
					t.Fatalf("fresh sidebar has %d defaults", len(got))
				}
				if !fresh {
					if want := []string{"fav", "second", "channels", "first", "dm"}; !reflect.DeepEqual(got, want) {
						t.Fatalf("order = %v, want %v", got, want)
					}
					for id, want := range map[string]int{"fav": 0, "channels": 10, "dm": 20, "second": 0, "first": 10, "foreign-user": 70, "foreign-team": 80} {
						var sortOrder int
						var updateAt int64
						if err := db.Pool.QueryRow(ctx, `SELECT sort_order,update_at FROM sidebar_categories WHERE id=$1`, id).Scan(&sortOrder, &updateAt); err != nil {
							t.Fatal(err)
						}
						if sortOrder != want {
							t.Fatalf("%s sort_order=%d, want %d", id, sortOrder, want)
						}
						if id != "first" && id != "second" && updateAt != 1 {
							t.Fatalf("omitted/foreign row %s was updated", id)
						}
					}
				}
				assertSidebarOrderEvent(t, clients, got)
			}
		})
	}
}

func TestSidebarOrderErrorsDoNotPublishPostgres(t *testing.T) {
	db, h, clients := sidebarOrderFixture(t)
	if _, err := db.Pool.Exec(context.Background(), `DROP TABLE sidebar_categories CASCADE`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		body   any
		status int
		id     string
	}{
		{"update fails", []string{"first"}, 500, "api.sidebar.order.app_error"},
		// UpdateOrder returns without querying for an empty slice, so only the
		// subsequent Order call can encounter this database failure.
		{"read fails after empty update", []string{}, 500, "api.sidebar.order.app_error"},
		{"invalid shape", map[string]string{"order": "first"}, 400, "api.sidebar.order.invalid_body"},
		{"item limit", make([]string, maxBulkItems+1), 400, "api.sidebar.order.too_many"},
		{"body limit", []string{strings.Repeat("x", 2<<20)}, 413, "api.sidebar.order.invalid_body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.updateSidebarCategoryOrder(rr, sidebarCategoriesRequest(t, http.MethodPut, "order", tc.body))
			var body apiError
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if rr.Code != tc.status || body.ID != tc.id {
				t.Fatalf("response = %d %s", rr.Code, rr.Body.String())
			}
			for _, c := range clients {
				select {
				case raw := <-c.Send:
					t.Fatalf("error published event: %s", raw)
				case <-time.After(50 * time.Millisecond):
				}
			}
		})
	}
}
