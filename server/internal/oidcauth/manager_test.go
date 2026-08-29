package oidcauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestConfigureAndAuthCodeURLUsesNonceAndPKCE(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/realms/moyro/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                server.URL + "/realms/moyro",
			"authorization_endpoint":                server.URL + "/realms/moyro/protocol/openid-connect/auth",
			"token_endpoint":                        server.URL + "/realms/moyro/protocol/openid-connect/token",
			"userinfo_endpoint":                     server.URL + "/realms/moyro/protocol/openid-connect/userinfo",
			"jwks_uri":                              server.URL + "/realms/moyro/protocol/openid-connect/certs",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	defer server.Close()

	manager := NewManager(server.Client())
	err := manager.Configure(context.Background(), Config{
		Enabled:      true,
		IssuerURL:    server.URL + "/realms/moyro",
		ClientID:     "moyro",
		ClientSecret: "secret",
		RedirectURL:  "https://moyro.example/api/moyro/v1/auth/oidc/keycloak/callback",
	})
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	authURL, err := manager.AuthCodeURL("state-value", "nonce-value", "pkce-verifier-value")
	if err != nil {
		t.Fatalf("AuthCodeURL() error = %v", err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	for key, want := range map[string]string{
		"state":                 "state-value",
		"nonce":                 "nonce-value",
		"code_challenge_method": "S256",
		"client_id":             "moyro",
	} {
		if got := query.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if query.Get("code_challenge") == "" || query.Get("code_challenge") == "pkce-verifier-value" {
		t.Fatalf("code_challenge was not an S256 challenge: %q", query.Get("code_challenge"))
	}
}

func TestPrepareDoesNotMutateLiveProviderUntilActivated(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                server.URL,
			"authorization_endpoint":                server.URL + "/auth",
			"token_endpoint":                        server.URL + "/token",
			"userinfo_endpoint":                     server.URL + "/userinfo",
			"jwks_uri":                              server.URL + "/certs",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	defer server.Close()

	manager := NewManager(server.Client())
	prepared, err := manager.Prepare(context.Background(), Config{
		Enabled: true, IssuerURL: server.URL, ClientID: "moyro",
		ClientSecret: "secret", RedirectURL: "https://moyro.example/callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if manager.Enabled() {
		t.Fatal("Prepare changed the live provider before durable settings could commit")
	}
	if err := manager.Activate(prepared); err != nil {
		t.Fatal(err)
	}
	if !manager.Enabled() {
		t.Fatal("Activate did not publish the prepared provider")
	}
}

func TestConfigureRejectsCrossOriginDiscoveryEndpoint(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 server.URL,
			"authorization_endpoint": server.URL + "/auth",
			"token_endpoint":         "https://attacker.example/token",
			"jwks_uri":               server.URL + "/certs",
		})
	}))
	defer server.Close()

	err := NewManager(server.Client()).Configure(context.Background(), Config{
		Enabled: true, IssuerURL: server.URL, ClientID: "moyro",
		ClientSecret: "secret", RedirectURL: "https://moyro.example/callback",
	})
	if err == nil {
		t.Fatal("Configure() accepted a cross-origin token endpoint")
	}
}

func TestIdentityFromClaimsUsesConfiguredClaims(t *testing.T) {
	identity := identityFromClaims(Config{
		UsernameClaim: "login", EmailClaim: "mail", GroupsClaim: "realm_groups",
	}, map[string]any{
		"login":          "operator",
		"mail":           "operator@example.com",
		"email_verified": true,
		"realm_groups":   []any{"ops", "admins"},
		"name":           "Moyro Operator",
	})
	if identity.Username != "operator" || identity.Email != "operator@example.com" || !identity.EmailVerified {
		t.Fatalf("unexpected identity: %+v", identity)
	}
	if len(identity.Groups) != 2 || identity.Groups[0] != "admins" || identity.Groups[1] != "ops" {
		t.Fatalf("groups = %#v", identity.Groups)
	}
}
