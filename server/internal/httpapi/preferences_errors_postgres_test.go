package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/preferences"
	"github.com/hkjang/moyro/server/internal/store"
)

// TestGetPreferenceByNameReports404OnlyForMissingRows drives the real handler
// over the real preferences service and database. A (category, name) that has
// no row is 404 with the error id official clients key off; a storage fault is
// 500. The handler used to map every service error to 404, so a dead database
// looked to a client exactly like "you never saved a theme".
func TestGetPreferenceByNameReports404OnlyForMissingRows(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	seedSidebarHandlerFixture(t, ctx, db)
	h := &handlers{prefs: preferences.New(db)}

	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO preferences (user_id, category, name, value, update_at)
		VALUES ('user-a', 'display_settings', 'theme', '{"type":"Indigo"}', 1)
	`); err != nil {
		t.Fatalf("seed preference row: %v", err)
	}

	// An existing row still answers 200 with the stored value.
	rr := httptest.NewRecorder()
	h.getPreferenceByName(rr, preferenceByNameRequest(t, "display_settings", "theme"))
	if rr.Code != http.StatusOK {
		t.Fatalf("existing row: status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	var got preferences.Preference
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode preference: %v", err)
	}
	if want := (preferences.Preference{UserID: "user-a", Category: "display_settings", Name: "theme", Value: `{"type":"Indigo"}`}); got != want {
		t.Fatalf("preference = %+v, want %+v", got, want)
	}

	// A missing row keeps the exact 404 contract clients rely on.
	rr = httptest.NewRecorder()
	h.getPreferenceByName(rr, preferenceByNameRequest(t, "display_settings", "no_such_name"))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing row: status = %d, want 404 (body %s)", rr.Code, rr.Body.String())
	}
	var body apiError
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode 404 body: %v", err)
	}
	if body.ID != "api.preference.get.not_found" || body.Message != "preference not found" {
		t.Fatalf("404 body = %+v, want the unchanged not_found id and message", body)
	}

	// Pull the table out from under the service so GetByName fails for a
	// reason that is not "no such row".
	if _, err := db.Pool.Exec(ctx, `DROP TABLE preferences CASCADE`); err != nil {
		t.Fatalf("drop preferences: %v", err)
	}
	rr = httptest.NewRecorder()
	h.getPreferenceByName(rr, preferenceByNameRequest(t, "display_settings", "theme"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("storage fault: status = %d, want 500 (body %s)", rr.Code, rr.Body.String())
	}
}

// preferenceByNameRequest builds a self-scoped GET against
// /users/user-a/preferences/{category}/name/{name}. actor == target, so
// requireUserParamAccess passes without an auth service.
func preferenceByNameRequest(t *testing.T, category, name string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v4/users/user-a/preferences/"+category+"/name/"+name, nil)
	ctx := context.WithValue(req.Context(), userIDKey, "user-a")
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("userID", "user-a")
	routeCtx.URLParams.Add("category", category)
	routeCtx.URLParams.Add("name", name)
	return req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, routeCtx))
}
