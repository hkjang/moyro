package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/pluginhost"
	"github.com/hkjang/moyro/server/internal/rbac"
)

type pluginPermissionRepository struct {
	effective map[string]map[string]struct{}
}

func (r *pluginPermissionRepository) EffectivePermissions(_ context.Context, userID string, _ rbac.Scope) (map[string]struct{}, error) {
	return r.effective[userID], nil
}
func (*pluginPermissionRepository) ListPermissions(context.Context) ([]rbac.Permission, error) {
	return nil, nil
}
func (*pluginPermissionRepository) GetRole(context.Context, string) (rbac.Role, error) {
	return rbac.Role{}, rbac.ErrNotFound
}
func (*pluginPermissionRepository) ListRoles(context.Context) ([]rbac.Role, error) {
	return nil, nil
}
func (*pluginPermissionRepository) ReplaceRolePermissions(context.Context, string, []string, string, *int64, int64) (rbac.Role, error) {
	return rbac.Role{}, rbac.ErrNotFound
}

func pluginManagementTestRouter(t *testing.T, effective map[string]map[string]struct{}, principal *rbac.Principal, withHost bool) http.Handler {
	t.Helper()
	service, err := rbac.New(&pluginPermissionRepository{effective: effective})
	if err != nil {
		t.Fatal(err)
	}
	h := &handlers{native: &nativeServices{rbac: service}}
	if withHost {
		h.host = pluginhost.New(t.TempDir(), nil)
	}
	router := chi.NewRouter()
	if principal != nil {
		router.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r.WithContext(setPrincipalOnContext(r.Context(), *principal)))
			})
		})
	}
	router.Get("/plugins/statuses", h.listPluginStatuses)
	router.Get("/plugins/webapp", h.listPluginWebapp)
	h.mountPluginManagementRoutes(router)
	return router
}

func TestPluginManagementRoutesRequireManagePlugins(t *testing.T) {
	principal := &rbac.Principal{UserID: "ordinary-user"}
	router := pluginManagementTestRouter(t, map[string]map[string]struct{}{
		principal.UserID: {},
	}, principal, true)

	tests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/plugins", ""},
		{http.MethodGet, "/plugins/capabilities", ""},
		{http.MethodPost, "/plugins", "bundle"},
		{http.MethodDelete, "/plugins/example", ""},
		{http.MethodPost, "/plugins/example/enable", ""},
		{http.MethodPost, "/plugins/example/disable", ""},
		{http.MethodGet, "/plugins/example/configuration", ""},
		{http.MethodPut, "/plugins/example/configuration", `{ "configuration": {} }`},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestPluginDiscoveryRoutesRemainAvailableToAuthenticatedUsers(t *testing.T) {
	principal := &rbac.Principal{UserID: "ordinary-user"}
	router := pluginManagementTestRouter(t, map[string]map[string]struct{}{
		principal.UserID: {},
	}, principal, true)
	for _, path := range []string{"/plugins/statuses", "/plugins/webapp"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200; body=%s", path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestPluginManagementRoutesAllowDelegatedPermission(t *testing.T) {
	principal := &rbac.Principal{UserID: "plugin-admin"}
	router := pluginManagementTestRouter(t, map[string]map[string]struct{}{
		principal.UserID: {rbac.PermissionManagePlugins: {}},
	}, principal, true)

	listRecorder := httptest.NewRecorder()
	router.ServeHTTP(listRecorder, httptest.NewRequest(http.MethodGet, "/plugins", nil))
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body=%s", listRecorder.Code, listRecorder.Body.String())
	}

	capabilityRecorder := httptest.NewRecorder()
	router.ServeHTTP(capabilityRecorder, httptest.NewRequest(http.MethodGet, "/plugins/capabilities", nil))
	if capabilityRecorder.Code != http.StatusOK {
		t.Fatalf("capability status = %d, want 200; body=%s", capabilityRecorder.Code, capabilityRecorder.Body.String())
	}
	var capabilities map[string]bool
	if err := json.NewDecoder(capabilityRecorder.Body).Decode(&capabilities); err != nil {
		t.Fatal(err)
	}
	if !capabilities["management_enabled"] || !capabilities["uploads_enabled"] {
		t.Fatalf("capabilities = %#v, want runtime management enabled", capabilities)
	}
}

func TestPluginManagementRoutesAcceptManageSystemRecoveryAuthority(t *testing.T) {
	principal := &rbac.Principal{UserID: "system-admin"}
	router := pluginManagementTestRouter(t, map[string]map[string]struct{}{
		principal.UserID: {rbac.PermissionManageSystem: {}},
	}, principal, true)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/plugins", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPluginManagementRoutesRejectMissingPrincipal(t *testing.T) {
	router := pluginManagementTestRouter(t, nil, nil, true)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/plugins", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPluginManagementCapabilitiesFailClosedWithoutRuntime(t *testing.T) {
	principal := &rbac.Principal{UserID: "plugin-admin"}
	router := pluginManagementTestRouter(t, map[string]map[string]struct{}{
		principal.UserID: {rbac.PermissionManagePlugins: {}},
	}, principal, false)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/plugins/capabilities", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	var capabilities map[string]bool
	if err := json.NewDecoder(recorder.Body).Decode(&capabilities); err != nil {
		t.Fatal(err)
	}
	if capabilities["management_enabled"] || capabilities["uploads_enabled"] {
		t.Fatalf("capabilities = %#v, want fail-closed runtime flags", capabilities)
	}
}
