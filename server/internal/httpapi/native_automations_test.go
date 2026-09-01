package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hkjang/moyro/server/internal/rbac"
)

func TestAutomationHandlersRejectRestrictedKeysWithoutExactGrant(t *testing.T) {
	t.Parallel()
	api := newAutomationHTTP(nil, nil, nil, nil, &handlers{})
	for _, test := range []struct {
		name, method string
		handle       http.HandlerFunc
	}{
		{name: "list needs read", method: http.MethodGet, handle: api.list},
		{name: "create needs write", method: http.MethodPost, handle: api.create},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(test.method, "/api/moyro/v1/me/automation-rules", nil)
			request = request.WithContext(setPrincipalOnContext(request.Context(), rbac.Principal{
				UserID: "user-1", CredentialID: "key-1", Restricted: true,
				GrantedPermissions: map[string]struct{}{rbac.PermissionUseAI: {}},
			}))
			response := httptest.NewRecorder()
			test.handle(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
			}
		})
	}
}
