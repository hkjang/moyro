package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hkjang/moyro/server/internal/rbac"
)

type fixedOperationsReader struct{ value nativeOperationsSnapshot }

func (r fixedOperationsReader) Snapshot(context.Context) nativeOperationsSnapshot { return r.value }

type operationsRBACRepository struct{ effective map[string]struct{} }

func (r operationsRBACRepository) EffectivePermissions(context.Context, string, rbac.Scope) (map[string]struct{}, error) {
	return r.effective, nil
}
func (operationsRBACRepository) ListPermissions(context.Context) ([]rbac.Permission, error) {
	return nil, nil
}
func (operationsRBACRepository) GetRole(context.Context, string) (rbac.Role, error) {
	return rbac.Role{}, rbac.ErrNotFound
}
func (operationsRBACRepository) ListRoles(context.Context) ([]rbac.Role, error) { return nil, nil }
func (operationsRBACRepository) ReplaceRolePermissions(context.Context, string, []string, string, *int64, int64) (rbac.Role, error) {
	return rbac.Role{}, nil
}

func TestNativeAdminOperationsRequiresManageSystemAndReturnsNoStoreSnapshot(t *testing.T) {
	for _, test := range []struct {
		name        string
		permissions map[string]struct{}
		wantStatus  int
	}{
		{name: "system administrator", permissions: map[string]struct{}{rbac.PermissionManageSystem: {}}, wantStatus: http.StatusOK},
		{name: "settings administrator", permissions: map[string]struct{}{rbac.PermissionManageSettings: {}}, wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			authorizer, err := rbac.New(operationsRBACRepository{effective: test.permissions})
			if err != nil {
				t.Fatal(err)
			}
			h := &handlers{
				native: &nativeServices{rbac: authorizer},
				operations: fixedOperationsReader{value: nativeOperationsSnapshot{
					CheckedAt: 42,
					Database:  databaseOperationalStatus{State: operationalReady},
				}},
			}
			handler := h.nativeRequire(rbac.PermissionManageSystem)(http.HandlerFunc(h.getNativeAdminOperations))
			request := httptest.NewRequest(http.MethodGet, "/api/moyro/v1/admin/operations", nil)
			request = request.WithContext(setPrincipalOnContext(request.Context(), rbac.UserPrincipal("admin-1")))
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if test.wantStatus != http.StatusOK {
				return
			}
			if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Fatalf("Cache-Control = %q", got)
			}
			var response nativeOperationsSnapshot
			if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
				t.Fatal(err)
			}
			if response.CheckedAt != 42 || response.Database.State != operationalReady {
				t.Fatalf("snapshot = %#v", response)
			}
		})
	}
}

func TestNativeAdminOperationsUnavailableDoesNotClaimHealth(t *testing.T) {
	recorder := httptest.NewRecorder()
	(&handlers{}).getNativeAdminOperations(recorder, httptest.NewRequest(http.MethodGet, "/api/moyro/v1/admin/operations", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestNilOperationsReaderKeepsEverySubsystemUnknown(t *testing.T) {
	var reader *postgresOperationsReader
	snapshot := reader.Snapshot(context.Background())
	if snapshot.Database.State != operationalUnknown || snapshot.Workers.State != operationalUnknown ||
		snapshot.Webhooks.State != operationalUnknown || snapshot.Storage.State != operationalUnknown {
		t.Fatalf("nil reader claimed an operational state: %#v", snapshot)
	}
	if snapshot.Workers.RuntimeObservable || snapshot.Webhooks.RuntimeObservable {
		t.Fatalf("nil reader claimed runtime observability: %#v", snapshot)
	}
}
