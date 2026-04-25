package httpapi

import "testing"

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
