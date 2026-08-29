package pat

import (
	"net/http/httptest"
	"testing"
)

func TestExtractAcceptsAuthorizationHeaderOnly(t *testing.T) {
	request := httptest.NewRequest("GET", "/api/v4/users/me?access_token=mdp_query_secret", nil)
	if token := extract(request); token != "" {
		t.Fatalf("query credential was accepted: %q", token)
	}

	request.Header.Set("Authorization", "Bearer mdp_header_secret")
	if token := extract(request); token != "mdp_header_secret" {
		t.Fatalf("header credential = %q", token)
	}
}
