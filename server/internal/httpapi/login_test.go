package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExtractBearerUsesAuthorizationHeaderOnly(t *testing.T) {
	queryOnly := httptest.NewRequest(http.MethodGet, "http://moyro.local/api/v4/users/me?access_token=query-secret", nil)
	if got := extractBearer(queryOnly); got != "" {
		t.Fatalf("query credential was accepted: %q", got)
	}

	header := httptest.NewRequest(http.MethodGet, "http://moyro.local/api/v4/users/me?access_token=query-secret", nil)
	header.Header.Set("Authorization", "Bearer header-secret")
	if got := extractBearer(header); got != "header-secret" {
		t.Fatalf("header credential = %q, want header-secret", got)
	}
}

func TestWebsocketRejectsURLCredential(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://moyro.local/api/v4/websocket?access_token=query-secret", nil)

	(&handlers{}).websocket(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestLoginReqIdentifier(t *testing.T) {
	tests := []struct {
		name string
		req  loginReq
		want string
	}{
		{
			name: "prefers login_id",
			req:  loginReq{LoginID: " webuser ", ID: "ignored"},
			want: "webuser",
		},
		{
			name: "falls back to official id field",
			req:  loginReq{ID: " web@example.com "},
			want: "web@example.com",
		},
		{
			name: "empty",
			req:  loginReq{},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.req.identifier(); got != tt.want {
				t.Fatalf("identifier() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnsureRoleToken(t *testing.T) {
	tests := []struct {
		name  string
		roles string
		role  string
		want  string
	}{
		{name: "append missing admin", roles: "system_user", role: "system_admin", want: "system_user system_admin"},
		{name: "dedupe existing admin", roles: " system_user  system_admin ", role: "system_admin", want: "system_user system_admin"},
		{name: "empty roles", roles: "", role: "system_admin", want: "system_admin"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ensureRoleToken(tt.roles, tt.role); got != tt.want {
				t.Fatalf("ensureRoleToken(%q, %q) = %q, want %q", tt.roles, tt.role, got, tt.want)
			}
		})
	}
}
