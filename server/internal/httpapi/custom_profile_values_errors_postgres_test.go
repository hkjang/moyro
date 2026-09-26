package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/customprofile"
	"github.com/hkjang/moyro/server/internal/store"
)

// TestCustomProfileValuesReadBackFaultIsNotSwallowed drives the three handlers
// that read a user's custom profile attribute map — GET
// /users/{userID}/custom_profile_attributes and the two PATCH paths that echo
// the post-write state back — over the real customprofile service and a real
// database.
//
// All three call the same Service.GetUserValues on the same input, so all
// three owe the caller the same answer when that read fails. The GET already
// answered 500; both PATCH paths dropped the error on the floor and wrote
// `200 null`, which a client cannot tell from "this user has no attributes
// set" — the profile form would blank every field it had just saved.
//
// The fault injection is the storage table itself: PatchUserValues documents
// an empty map as a no-op and returns before it opens a transaction, while
// GetUserValues always issues its SELECT. So with custom_profile_values
// dropped, a `{}` body exercises exactly one table access — the read-back —
// with no hand-built stand-in anywhere in the path.
func TestCustomProfileValuesReadBackFaultIsNotSwallowed(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	seedSidebarHandlerFixture(t, ctx, db)
	h := &handlers{customProf: customprofile.New(db)}

	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO custom_profile_fields (id, name, type, target_id, target_type, attrs, sort_order, create_at, update_at, delete_at)
		VALUES ('field-dept', 'Department', 'text', '', 'user', '{}'::jsonb, 1, 1, 1, 0)
	`); err != nil {
		t.Fatalf("seed custom profile field: %v", err)
	}

	// A healthy write still round-trips through both PATCH paths untouched.
	rr := httptest.NewRecorder()
	h.patchCustomProfileValuesGlobal(rr, customProfileValuesRequest(t, `{"field-dept":"Support"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("global patch: status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	if got := decodeCustomProfileValues(t, rr); got["field-dept"] != `"Support"` {
		t.Fatalf("global patch body = %v, want the stored value echoed back", got)
	}
	rr = httptest.NewRecorder()
	h.patchUserCustomProfileValues(rr, customProfileValuesRequest(t, `{"field-dept":"Billing"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("user patch: status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	if got := decodeCustomProfileValues(t, rr); got["field-dept"] != `"Billing"` {
		t.Fatalf("user patch body = %v, want the stored value echoed back", got)
	}

	// An empty patch on a healthy database is still 200 with the current map,
	// so the fault below is the only thing the assertions can be reacting to.
	rr = httptest.NewRecorder()
	h.patchCustomProfileValuesGlobal(rr, customProfileValuesRequest(t, `{}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("empty global patch: status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	if got := decodeCustomProfileValues(t, rr); got["field-dept"] != `"Billing"` {
		t.Fatalf("empty global patch body = %v, want the unchanged stored map", got)
	}

	// Pull the value table out from under the service. Every read of the map
	// now fails for a reason that is not "this user has nothing stored".
	if _, err := db.Pool.Exec(ctx, `DROP TABLE custom_profile_values`); err != nil {
		t.Fatalf("drop custom_profile_values: %v", err)
	}

	// The sibling GET is the contract the two PATCH paths have to match.
	rr = httptest.NewRecorder()
	h.getUserCustomProfileValues(rr, customProfileValuesRequest(t, ""))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("get under storage fault: status = %d, want 500 (body %s)", rr.Code, rr.Body.String())
	}
	if id := decodeErrorID(t, rr); id != "api.custom_profile.values.get.app_error" {
		t.Fatalf("get error id = %q, want api.custom_profile.values.get.app_error", id)
	}

	for _, tc := range []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"global", h.patchCustomProfileValuesGlobal},
		{"user", h.patchUserCustomProfileValues},
	} {
		t.Run(tc.name+" patch reports the read-back fault", func(t *testing.T) {
			rr := httptest.NewRecorder()
			tc.handler(rr, customProfileValuesRequest(t, `{}`))
			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500 (body %s)", rr.Code, rr.Body.String())
			}
			if id := decodeErrorID(t, rr); id != "api.custom_profile.values.get.app_error" {
				t.Fatalf("error id = %q, want the same id the sibling GET answers with", id)
			}
		})
	}
}

// customProfileValuesRequest builds a self-scoped request against
// /users/user-a/custom_profile_attributes. actor == target, so
// requireUserParamAccess passes without an auth service. An empty body string
// means "no body at all", for the GET.
func customProfileValuesRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	method := http.MethodPatch
	var reader *strings.Reader
	if body == "" {
		method = http.MethodGet
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/api/v4/users/user-a/custom_profile_attributes", reader)
	ctx := context.WithValue(req.Context(), userIDKey, "user-a")
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("userID", "user-a")
	return req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, routeCtx))
}

func decodeCustomProfileValues(t *testing.T, rr *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode values body %q: %v", rr.Body.String(), err)
	}
	out := map[string]string{}
	for k, v := range raw {
		out[k] = string(v)
	}
	return out
}

func decodeErrorID(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var body apiError
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rr.Body.String(), err)
	}
	return body.ID
}
