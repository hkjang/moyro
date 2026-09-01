package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hkjang/moyro/server/internal/rbac"
)

func TestInboxPreferencesRestrictedCredentialRequiresReadAndWriteGrants(t *testing.T) {
	tests := []struct {
		name       string
		permission string
		grants     map[string]struct{}
		effective  map[string]struct{}
		want       bool
	}{
		{name: "read missing", permission: rbac.PermissionMCPRead, grants: map[string]struct{}{}, effective: map[string]struct{}{rbac.PermissionMCPRead: {}}, want: false},
		{name: "write does not inherit read", permission: rbac.PermissionMCPWrite, grants: map[string]struct{}{rbac.PermissionMCPRead: {}}, effective: map[string]struct{}{rbac.PermissionMCPWrite: {}}, want: false},
		{name: "owner role revoked", permission: rbac.PermissionMCPRead, grants: map[string]struct{}{rbac.PermissionMCPRead: {}}, effective: map[string]struct{}{}, want: false},
		{name: "read grant intersects owner role", permission: rbac.PermissionMCPRead, grants: map[string]struct{}{rbac.PermissionMCPRead: {}}, effective: map[string]struct{}{rbac.PermissionMCPRead: {}}, want: true},
		{name: "write grant intersects owner role", permission: rbac.PermissionMCPWrite, grants: map[string]struct{}{rbac.PermissionMCPWrite: {}}, effective: map[string]struct{}{rbac.PermissionMCPWrite: {}}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, err := rbac.New(&knowledgePermissionRepository{effective: test.effective})
			if err != nil {
				t.Fatal(err)
			}
			h := &handlers{native: &nativeServices{rbac: service}}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/moyro/v1/me/inbox-preferences", nil)
			request = request.WithContext(setPrincipalOnContext(request.Context(), rbac.Principal{
				UserID: "user-1", CredentialID: "key-1", Restricted: true, GrantedPermissions: test.grants,
			}))
			if got := h.requireInboxPreferenceGrant(recorder, request, test.permission); got != test.want {
				t.Fatalf("allowed = %v, want %v", got, test.want)
			}
			if !test.want && recorder.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
			}
		})
	}
}

func TestInboxPreferencesUnrestrictedPrincipalPreservesBrowserAndPATAccess(t *testing.T) {
	for _, principal := range []rbac.Principal{
		{},
		{UserID: "user-1"},
		{UserID: "user-1", CredentialID: "pat-1"},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		if principal.UserID != "" {
			request = request.WithContext(setPrincipalOnContext(request.Context(), principal))
		}
		if !(&handlers{}).requireInboxPreferenceGrant(recorder, request, rbac.PermissionMCPWrite) {
			t.Fatalf("unrestricted principal was rejected: %#v", principal)
		}
	}
}
