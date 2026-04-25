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
