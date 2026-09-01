package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hkjang/moyro/server/internal/documents"
	"github.com/hkjang/moyro/server/internal/rbac"
)

func TestDecodeNativeKnowledgeJSONIsStrictAndBounded(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		body    string
		limit   int64
		wantErr bool
	}{
		{name: "valid", body: `{"query":"장애 대응","team_id":"team-1"}`, limit: 1024},
		{name: "unknown field", body: `{"query":"장애","team_id":"team-1","raw_sources":true}`, limit: 1024, wantErr: true},
		{name: "trailing object", body: `{"query":"장애","team_id":"team-1"}{}`, limit: 1024, wantErr: true},
		{name: "empty", body: ``, limit: 1024, wantErr: true},
		{name: "over limit", body: `{"query":"` + strings.Repeat("가", 100) + `","team_id":"team-1"}`, limit: 32, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodPost, "/api/moyro/v1/me/knowledge/search", strings.NewReader(test.body))
			response := httptest.NewRecorder()
			var decoded nativeKnowledgeSearchRequest
			err := decodeNativeKnowledgeJSON(response, request, test.limit, &decoded)
			if (err != nil) != test.wantErr {
				t.Fatalf("decode error = %v, wantErr=%v", err, test.wantErr)
			}
		})
	}
}

type knowledgePermissionRepository struct {
	effective map[string]struct{}
}

func (repository *knowledgePermissionRepository) EffectivePermissions(context.Context, string, rbac.Scope) (map[string]struct{}, error) {
	return repository.effective, nil
}

func (*knowledgePermissionRepository) ListPermissions(context.Context) ([]rbac.Permission, error) {
	return nil, nil
}

func (*knowledgePermissionRepository) GetRole(context.Context, string) (rbac.Role, error) {
	return rbac.Role{}, rbac.ErrNotFound
}

func (*knowledgePermissionRepository) ListRoles(context.Context) ([]rbac.Role, error) {
	return nil, nil
}

func (*knowledgePermissionRepository) ReplaceRolePermissions(context.Context, string, []string, string, *int64, int64) (rbac.Role, error) {
	return rbac.Role{}, nil
}

func TestKnowledgePermissionRequiresCredentialGrantAndLiveRBAC(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		principal  rbac.Principal
		effective  map[string]struct{}
		permission string
		want       bool
		status     int
	}{
		{
			name:       "browser membership remains domain authorized",
			principal:  rbac.UserPrincipal("user-1"),
			permission: rbac.PermissionMCPRead,
			want:       true,
		},
		{
			name:       "restricted key without grant",
			principal:  rbac.Principal{UserID: "user-1", Restricted: true},
			effective:  map[string]struct{}{rbac.PermissionMCPRead: {}},
			permission: rbac.PermissionMCPRead,
			status:     http.StatusForbidden,
		},
		{
			name: "grant removed from live owner role",
			principal: rbac.Principal{
				UserID: "user-1", Restricted: true,
				GrantedPermissions: map[string]struct{}{rbac.PermissionMCPRead: {}},
			},
			effective:  map[string]struct{}{},
			permission: rbac.PermissionMCPRead,
			status:     http.StatusForbidden,
		},
		{
			name: "grant intersects live owner role",
			principal: rbac.Principal{
				UserID: "user-1", Restricted: true,
				GrantedPermissions: map[string]struct{}{rbac.PermissionMCPRead: {}},
			},
			effective:  map[string]struct{}{rbac.PermissionMCPRead: {}},
			permission: rbac.PermissionMCPRead,
			want:       true,
		},
		{
			name: "read-only key cannot write documents",
			principal: rbac.Principal{
				UserID: "user-1", Restricted: true,
				GrantedPermissions: map[string]struct{}{rbac.PermissionMCPRead: {}},
			},
			effective:  map[string]struct{}{rbac.PermissionMCPRead: {}, rbac.PermissionMCPWrite: {}},
			permission: rbac.PermissionMCPWrite,
			status:     http.StatusForbidden,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service, err := rbac.New(&knowledgePermissionRepository{effective: test.effective})
			if err != nil {
				t.Fatal(err)
			}
			h := &handlers{native: &nativeServices{rbac: service}}
			request := httptest.NewRequest(http.MethodGet, "/api/moyro/v1/me/documents", nil)
			request = request.WithContext(setPrincipalOnContext(request.Context(), test.principal))
			response := httptest.NewRecorder()
			got := h.authorizeNativeKnowledgePermission(response, request, test.permission)
			if got != test.want {
				t.Fatalf("allowed = %v, want %v", got, test.want)
			}
			if !test.want && response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
		})
	}
}

func TestWriteNativeDocumentErrorUsesStableStatusAndCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "invalid", err: documents.ErrInvalid, wantStatus: http.StatusBadRequest, wantCode: "api.moyro.document.invalid"},
		{name: "missing", err: documents.ErrNotFound, wantStatus: http.StatusNotFound, wantCode: "api.moyro.document.not_found"},
		{name: "forbidden", err: documents.ErrForbidden, wantStatus: http.StatusForbidden, wantCode: "api.moyro.document.forbidden"},
		{name: "revision", err: documents.ErrRevisionConflict, wantStatus: http.StatusConflict, wantCode: "api.moyro.document.revision_conflict"},
		{name: "source changed", err: documents.ErrSourceChanged, wantStatus: http.StatusConflict, wantCode: "api.moyro.document.source_changed"},
		{name: "source too large", err: documents.ErrSourceTooLarge, wantStatus: http.StatusRequestEntityTooLarge, wantCode: "api.moyro.document.source_too_large"},
		{name: "idempotency", err: documents.ErrIdempotencyConflict, wantStatus: http.StatusConflict, wantCode: "api.moyro.document.idempotency_conflict"},
		{name: "internal", err: errors.New("database unavailable"), wantStatus: http.StatusInternalServerError, wantCode: "api.moyro.document.app_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			response := httptest.NewRecorder()
			writeNativeDocumentError(response, test.err)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			var body apiError
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.ID != test.wantCode || body.StatusCode != test.wantStatus {
				t.Fatalf("error body = %#v", body)
			}
		})
	}
}
