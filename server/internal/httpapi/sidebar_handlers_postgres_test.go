package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/sidebar"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/hkjang/moyro/server/internal/ws"
)

// TestBulkSidebarUpdateLeavesNothingBehindWhenOneItemFails drives the real
// handler over the real service and database: a bulk PUT whose second item
// is rejected answers with that item's status and the first item's row is
// untouched. The handler used to apply each item in its own transaction, so
// the first change was already committed when the 404 went out.
func TestBulkSidebarUpdateLeavesNothingBehindWhenOneItemFails(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	seedSidebarHandlerFixture(t, ctx, db)
	service := sidebar.New(db)
	h := &handlers{sidebar: service, hub: ws.NewHub()}

	if _, err := service.ListForTeam(ctx, "user-a", "team-main"); err != nil {
		t.Fatalf("bootstrap defaults: %v", err)
	}
	custom, err := service.Create(ctx, "user-a", "team-main", "Ops", []string{"chan-alpha"})
	if err != nil {
		t.Fatalf("create custom category: %v", err)
	}
	before := sidebarNameAndUpdateAt(t, ctx, db, custom.ID)

	renamed := *custom
	renamed.DisplayName = "Renamed"
	for _, tc := range []struct {
		name   string
		second sidebar.Category
		want   int
	}{
		{"missing category", sidebar.Category{ID: "no-such-category", DisplayName: "X"}, http.StatusNotFound},
		{"unknown sorting", sidebar.Category{ID: custom.ID, DisplayName: "Ops", Sorting: "bogus"}, http.StatusBadRequest},
	} {
		rr := httptest.NewRecorder()
		h.updateSidebarCategoriesBulk(rr, sidebarCategoriesRequest(t, http.MethodPut, "", []sidebar.Category{renamed, tc.second}))
		if rr.Code != tc.want {
			t.Fatalf("%s: status = %d, want %d (body %s)", tc.name, rr.Code, tc.want, rr.Body.String())
		}
		var body apiError
		if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
			t.Fatalf("%s: decode error body: %v", tc.name, err)
		}
		if body.ID != "api.sidebar.bulk.app_error" {
			t.Fatalf("%s: error id = %q, want api.sidebar.bulk.app_error", tc.name, body.ID)
		}
		if after := sidebarNameAndUpdateAt(t, ctx, db, custom.ID); after != before {
			t.Fatalf("%s: first item was applied although the batch failed: %+v -> %+v", tc.name, before, after)
		}
	}

	// The same array with a valid second item applies both and answers in
	// input order.
	second := *custom
	second.DisplayName = "Second"
	second.Collapsed = true
	rr := httptest.NewRecorder()
	h.updateSidebarCategoriesBulk(rr, sidebarCategoriesRequest(t, http.MethodPut, "", []sidebar.Category{renamed, second}))
	if rr.Code != http.StatusOK {
		t.Fatalf("valid batch: status = %d, body %s", rr.Code, rr.Body.String())
	}
	var out []sidebar.Category
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode bulk response: %v", err)
	}
	if len(out) != 2 || out[0].ID != custom.ID || out[1].ID != custom.ID {
		t.Fatalf("bulk response = %+v, want the two items in input order", out)
	}
	// Same id twice in one array: the later item wins, as before.
	if after := sidebarNameAndUpdateAt(t, ctx, db, custom.ID); after.DisplayName != "Second" {
		t.Fatalf("stored display_name = %q after a valid batch, want Second", after.DisplayName)
	}
}

// TestGetSidebarCategoryReports404OnlyForMissingRows pins the read handler's
// split: an id the caller cannot see is 404, but a storage fault is 500 —
// it used to be reported as 404 as if the category did not exist.
func TestGetSidebarCategoryReports404OnlyForMissingRows(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	seedSidebarHandlerFixture(t, ctx, db)
	h := &handlers{sidebar: sidebar.New(db)}

	rr := httptest.NewRecorder()
	h.getSidebarCategory(rr, sidebarCategoriesRequest(t, http.MethodGet, "no-such-category", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing category: status = %d, want 404 (body %s)", rr.Code, rr.Body.String())
	}

	// Pull the table out from under the service so Get fails for a reason
	// that is not "no such row".
	if _, err := db.Pool.Exec(ctx, `DROP TABLE sidebar_categories CASCADE`); err != nil {
		t.Fatalf("drop sidebar_categories: %v", err)
	}
	rr = httptest.NewRecorder()
	h.getSidebarCategory(rr, sidebarCategoriesRequest(t, http.MethodGet, "no-such-category", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("storage fault: status = %d, want 500 (body %s)", rr.Code, rr.Body.String())
	}
	var body apiError
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.ID != "api.sidebar.get.not_found" {
		t.Fatalf("error id = %q, want api.sidebar.get.not_found kept for clients", body.ID)
	}
}

type sidebarNameRow struct {
	DisplayName string
	UpdateAt    int64
}

func sidebarNameAndUpdateAt(t *testing.T, ctx context.Context, db *store.DB, id string) sidebarNameRow {
	t.Helper()
	var row sidebarNameRow
	if err := db.Pool.QueryRow(ctx, `SELECT display_name, update_at FROM sidebar_categories WHERE id=$1`, id).Scan(&row.DisplayName, &row.UpdateAt); err != nil {
		t.Fatalf("read category %s: %v", id, err)
	}
	return row
}

// sidebarCategoriesRequest builds a self-scoped request against
// /users/user-a/teams/team-main/channels/categories[/{categoryID}] with the
// given payload, if any, JSON-encoded as the body.
func sidebarCategoriesRequest(t *testing.T, method, categoryID string, payload any) *http.Request {
	t.Helper()
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			t.Fatalf("encode payload: %v", err)
		}
	}
	target := "/api/v4/users/user-a/teams/team-main/channels/categories"
	if categoryID != "" {
		target += "/" + categoryID
	}
	req := httptest.NewRequest(method, target, &body)
	ctx := context.WithValue(req.Context(), userIDKey, "user-a")
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("userID", "user-a")
	routeCtx.URLParams.Add("teamID", "team-main")
	if categoryID != "" {
		routeCtx.URLParams.Add("categoryID", categoryID)
	}
	return req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, routeCtx))
}

func seedSidebarHandlerFixture(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES ('user-a', 'user-a', 'a@example.test', 'hash', 'system_user', 1, 1);
		INSERT INTO teams (id, name, display_name, type, create_at, update_at)
		VALUES ('team-main', 'team-main', 'Main Team', 'O', 1, 1);
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES ('team-main', 'user-a', 'team_user', 1);
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES ('chan-alpha', 'team-main', 'O', 'Alpha', 'alpha', 1, 1);
		INSERT INTO channel_members (channel_id, user_id, roles, create_at)
		VALUES ('chan-alpha', 'user-a', 'channel_user', 1)
	`); err != nil {
		t.Fatalf("seed sidebar handler fixture: %v", err)
	}
}
