package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/auth"
	"github.com/hkjang/moyro/server/internal/store"
)

func TestGuestNamedUserLookupHidesNonSharedAccountsPostgres(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate guest directory schema: %v", err)
	}

	now := time.Now().UnixMilli()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users
			(id, username, email, password_hash, roles, create_at, update_at, guest_expires_at, guest_file_download)
		VALUES
			('directory-guest', 'directory-guest', 'directory-guest@example.test', 'unused', 'system_guest', $1, $1, $2, FALSE),
			('directory-hidden', 'directory-hidden', 'directory-hidden@example.test', 'unused', 'system_user', $1, $1, 0, TRUE),
			('directory-regular', 'directory-regular', 'directory-regular@example.test', 'unused', 'system_user', $1, $1, 0, TRUE)
	`, now, now+int64(time.Hour/time.Millisecond)); err != nil {
		t.Fatalf("seed guest directory fixture: %v", err)
	}

	h := &handlers{auth: auth.New(db, []byte("guest-directory-test-signing-key"), time.Hour, nil)}
	tests := []struct {
		name       string
		param      string
		value      string
		path       string
		handler    func(http.ResponseWriter, *http.Request)
		wantCode   string
		missingVal string
	}{
		{
			name: "username", param: "username", value: "directory-hidden", path: "/api/v4/users/username/directory-hidden",
			handler: h.getUserByUsername, wantCode: "api.user.get.not_found", missingVal: "does-not-exist",
		},
		{
			name: "email", param: "email", value: "directory-hidden@example.test", path: "/api/v4/users/email/directory-hidden@example.test",
			handler: h.getUserByEmail, wantCode: "api.user.get_by_email.not_found", missingVal: "missing@example.test",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hidden := invokeNamedUserLookup(ctx, test.handler, test.path, test.param, test.value, "directory-guest")
			missing := invokeNamedUserLookup(ctx, test.handler, test.path, test.param, test.missingVal, "directory-guest")
			for label, response := range map[string]*httptest.ResponseRecorder{"hidden": hidden, "missing": missing} {
				if response.Code != http.StatusNotFound {
					t.Fatalf("%s status = %d, want 404; body=%s", label, response.Code, response.Body.String())
				}
				var body apiError
				if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.ID != test.wantCode || body.Message != "user not found" {
					t.Fatalf("%s error = %#v, want indistinguishable not-found response", label, body)
				}
			}

			regular := invokeNamedUserLookup(ctx, test.handler, test.path, test.param, test.value, "directory-regular")
			if regular.Code != http.StatusOK {
				t.Fatalf("regular user status = %d, want 200; body=%s", regular.Code, regular.Body.String())
			}
		})
	}
}

func invokeNamedUserLookup(
	ctx context.Context,
	handler func(http.ResponseWriter, *http.Request),
	path, param, value, actorID string,
) *httptest.ResponseRecorder {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add(param, value)
	requestContext := context.WithValue(ctx, chi.RouteCtxKey, routeContext)
	requestContext = context.WithValue(requestContext, userIDKey, actorID)
	r := httptest.NewRequest(http.MethodGet, path, nil).WithContext(requestContext)
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}
