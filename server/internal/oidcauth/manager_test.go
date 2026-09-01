package oidcauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

var testJWKS = map[string]any{"keys": []map[string]any{{
	"kty": "RSA", "use": "sig", "kid": "test", "n": "AQAB", "e": "AQAB",
}}}

func TestConfigureAndAuthCodeURLUsesNonceAndPKCE(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/moyro/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                                server.URL + "/realms/moyro",
				"authorization_endpoint":                server.URL + "/realms/moyro/protocol/openid-connect/auth",
				"token_endpoint":                        server.URL + "/realms/moyro/protocol/openid-connect/token",
				"userinfo_endpoint":                     server.URL + "/realms/moyro/protocol/openid-connect/userinfo",
				"jwks_uri":                              server.URL + "/realms/moyro/protocol/openid-connect/certs",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/realms/moyro/protocol/openid-connect/certs":
			_ = json.NewEncoder(w).Encode(testJWKS)
		default:
			http.NotFound(w, r)
		}
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
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/certs" {
			_ = json.NewEncoder(w).Encode(testJWKS)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": server.URL, "authorization_endpoint": server.URL + "/auth",
			"token_endpoint": server.URL + "/token", "userinfo_endpoint": server.URL + "/userinfo",
			"jwks_uri": server.URL + "/certs", "id_token_signing_alg_values_supported": []string{"RS256"},
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

func TestConfigureAcceptsValidCrossOriginDiscoveryEndpoints(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/certs" {
			_ = json.NewEncoder(w).Encode(testJWKS)
			return
		}
		http.NotFound(w, r)
	}))
	defer backend.Close()

	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 issuer.URL,
			"authorization_endpoint": "https://login.example.test/auth",
			"token_endpoint":         backend.URL + "/token",
			"jwks_uri":               backend.URL + "/certs",
		})
	}))
	defer issuer.Close()

	err := NewManager(issuer.Client()).Configure(context.Background(), Config{
		Enabled: true, IssuerURL: issuer.URL, ClientID: "moyro",
		ClientSecret: "secret", RedirectURL: "https://moyro.example/callback",
	})
	if err != nil {
		t.Fatalf("Configure() rejected valid cross-origin endpoints: %v", err)
	}
}

func TestProbeAcceptsDiscoveryDocumentURLWithoutClientSecret(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/moyro/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": server.URL + "/realms/moyro/", "authorization_endpoint": server.URL + "/auth",
				"token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/certs",
			})
		case "/certs":
			_ = json.NewEncoder(w).Encode(testJWKS)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	manager := NewManager(server.Client())
	issuer, err := manager.Probe(context.Background(), Config{
		Enabled: true, IssuerURL: server.URL + "/realms/moyro/.well-known/openid-configuration",
		ClientID: "moyro", RedirectURL: "https://moyro.example/callback",
	})
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if issuer != server.URL+"/realms/moyro/" {
		t.Fatalf("Probe() issuer = %q", issuer)
	}
	if manager.Enabled() {
		t.Fatal("Probe activated a provider")
	}
}

func TestNormalizeIssuerURLRejectsAmbiguousOrCredentialedURLs(t *testing.T) {
	for _, raw := range []string{
		"https://keycloak.example/realms/moyro?tenant=other",
		"https://user:secret@keycloak.example/realms/moyro",
		"https://keycloak.example/realms/moyro#fragment",
		"file:///etc/passwd",
	} {
		if _, err := normalizeIssuerURL(raw); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("normalizeIssuerURL(%q) error = %v", raw, err)
		}
	}
}

func TestNormalizeIssuerURLPreservesEscapedIssuerPath(t *testing.T) {
	const encoded = "https://keycloak.example/realms/parent%2Fchild"
	got, err := normalizeIssuerURL(encoded + discoveryDocumentSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if got != encoded {
		t.Fatalf("normalizeIssuerURL() = %q, want %q", got, encoded)
	}
	if sameIssuerLocation(encoded, "https://keycloak.example/realms/parent/child") {
		t.Fatal("encoded and literal path separators must identify different issuers")
	}
}

func TestReadBoundedResponseRejectsOversizedDocuments(t *testing.T) {
	_, err := readBoundedResponse(strings.NewReader(strings.Repeat("x", maxOIDCResponseBytes+1)))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("readBoundedResponse() error = %v", err)
	}
}

func TestConfigureRejectsMismatchedIssuerAndInvalidEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		metadata func(string) map[string]any
		want     error
	}{
		{
			name: "issuer mismatch",
			metadata: func(serverURL string) map[string]any {
				return map[string]any{
					"issuer": "https://other.example/realms/moyro", "authorization_endpoint": serverURL + "/auth",
					"token_endpoint": serverURL + "/token", "jwks_uri": serverURL + "/certs",
				}
			},
			want: ErrIssuerMismatch,
		},
		{
			name: "endpoint credentials",
			metadata: func(serverURL string) map[string]any {
				return map[string]any{
					"issuer": serverURL, "authorization_endpoint": serverURL + "/auth",
					"token_endpoint": "https://client:secret@tokens.example/token", "jwks_uri": serverURL + "/certs",
				}
			},
			want: ErrUnexpectedEndpoint,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/certs" {
					_ = json.NewEncoder(w).Encode(testJWKS)
					return
				}
				_ = json.NewEncoder(w).Encode(test.metadata(server.URL))
			}))
			defer server.Close()
			err := NewManager(server.Client()).Configure(context.Background(), Config{
				Enabled: true, IssuerURL: server.URL, ClientID: "moyro",
				ClientSecret: "secret", RedirectURL: "https://moyro.example/callback",
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("Configure() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestProbeRejectsJWKSWithoutSigningKeys(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/certs" {
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": server.URL, "authorization_endpoint": server.URL + "/auth",
			"token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/certs",
		})
	}))
	defer server.Close()

	_, err := NewManager(server.Client()).Probe(context.Background(), Config{
		Enabled: true, IssuerURL: server.URL, ClientID: "moyro", RedirectURL: "https://moyro.example/callback",
	})
	if err == nil || !strings.Contains(err.Error(), "does not contain a compatible public signing key") {
		t.Fatalf("Probe() error = %v", err)
	}
}

func TestProbeRejectsJWKSWithoutCompatiblePublicKey(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/certs" {
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
				"kty": "oct", "use": "sig", "k": "c2hhcmVkLXNlY3JldA",
			}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": server.URL, "authorization_endpoint": server.URL + "/auth",
			"token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/certs",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	defer server.Close()

	_, err := NewManager(server.Client()).Probe(context.Background(), Config{
		Enabled: true, IssuerURL: server.URL, ClientID: "moyro", RedirectURL: "https://moyro.example/callback",
	})
	if err == nil || !strings.Contains(err.Error(), "compatible public signing key") {
		t.Fatalf("Probe() error = %v", err)
	}
}

func TestProbeRejectsHTTPSRedirectDowngrade(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer target.Close()
	issuer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	defer issuer.Close()

	_, err := NewManager(issuer.Client()).Probe(context.Background(), Config{
		Enabled: true, IssuerURL: issuer.URL, ClientID: "moyro", RedirectURL: "https://moyro.example/callback",
	})
	if err == nil || !strings.Contains(err.Error(), "downgrade HTTPS") {
		t.Fatalf("Probe() error = %v", err)
	}
}

func TestOIDCClientRejectsCrossOriginCredentialRedirect(t *testing.T) {
	var targetReached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetReached.Store(true)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/token", http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	client, err := clientWithCA(source.Client(), "")
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, source.URL+"/token", strings.NewReader("client_secret=secret"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if response != nil {
		response.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "forward credentials across origins") {
		t.Fatalf("client.Do() error = %v", err)
	}
	if targetReached.Load() {
		t.Fatal("credential-bearing request reached the cross-origin redirect target")
	}
}

func TestValidateDiscoveryRequiresExplicitHTTPBackchannelOptIn(t *testing.T) {
	err := validateDiscovery("https://issuer.example/realms/moyro", discoveryMetadata{
		Issuer:                "https://issuer.example/realms/moyro",
		AuthorizationEndpoint: "https://login.example/auth",
		TokenEndpoint:         "http://tokens.example/token",
		JWKSURI:               "https://keys.example/certs",
	}, false)
	if !errors.Is(err, ErrUnexpectedEndpoint) || !strings.Contains(err.Error(), "enable allow_insecure_backchannel") {
		t.Fatalf("validateDiscovery() error = %v", err)
	}
}

func TestProbeAllowsExplicitHTTPBackchannel(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/certs" {
			_ = json.NewEncoder(w).Encode(testJWKS)
			return
		}
		http.NotFound(w, r)
	}))
	defer backend.Close()

	var issuer *httptest.Server
	issuer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != discoveryDocumentSuffix {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/auth",
			"token_endpoint": backend.URL + "/token", "jwks_uri": backend.URL + "/certs",
			"userinfo_endpoint":                     backend.URL + "/userinfo",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	defer issuer.Close()

	got, err := NewManager(issuer.Client()).Probe(context.Background(), Config{
		Enabled: true, IssuerURL: issuer.URL, ClientID: "moyro",
		RedirectURL: "https://moyro.example/callback", AllowInsecureBackchannel: true,
	})
	if err != nil {
		t.Fatalf("Probe() rejected explicitly allowed HTTP back-channel: %v", err)
	}
	if got != issuer.URL {
		t.Fatalf("Probe() issuer = %q, want %q", got, issuer.URL)
	}
}

func TestExchangeAllowsExplicitHTTPBackchannel(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const accessToken = "access-token"
	var issuer *httptest.Server
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/certs":
			_ = json.NewEncoder(w).Encode(rsaJWKS(&privateKey.PublicKey))
		case "/token":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "invalid form", http.StatusBadRequest)
				return
			}
			if r.Form.Get("code") != "code" || r.Form.Get("code_verifier") != "verifier" {
				http.Error(w, "invalid authorization code exchange", http.StatusBadRequest)
				return
			}
			claims := jwt.MapClaims{
				"iss": issuer.URL, "sub": "subject-1", "aud": "moyro",
				"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
				"nonce": "expected-nonce", "preferred_username": "operator",
				"email": "operator@example.com", "email_verified": true,
				"at_hash": accessTokenHash(accessToken),
			}
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			token.Header["kid"] = "test-key"
			rawToken, signErr := token.SignedString(privateKey)
			if signErr != nil {
				http.Error(w, "sign token", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": accessToken, "token_type": "Bearer", "expires_in": 300, "id_token": rawToken,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()

	issuer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != discoveryDocumentSuffix {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/auth",
			"token_endpoint": backend.URL + "/token", "jwks_uri": backend.URL + "/certs",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	defer issuer.Close()

	manager := NewManager(issuer.Client())
	if err := manager.Configure(context.Background(), Config{
		Enabled: true, IssuerURL: issuer.URL, ClientID: "moyro", ClientSecret: "secret",
		RedirectURL: "https://moyro.example/callback", RequireVerifiedEmail: true,
		AllowInsecureBackchannel: true,
	}); err != nil {
		t.Fatalf("Configure() rejected explicitly allowed HTTP back-channel: %v", err)
	}
	identity, err := manager.Exchange(context.Background(), "code", "verifier", "expected-nonce")
	if err != nil {
		t.Fatalf("Exchange() over explicitly allowed HTTP back-channel error = %v", err)
	}
	if identity.Subject != "subject-1" || identity.Email != "operator@example.com" {
		t.Fatalf("Exchange() identity = %+v", identity)
	}
}

func TestValidateDiscoveryKeepsAuthorizationEndpointHTTPS(t *testing.T) {
	err := validateDiscovery("https://issuer.example/realms/moyro", discoveryMetadata{
		Issuer:                "https://issuer.example/realms/moyro",
		AuthorizationEndpoint: "http://login.example/auth",
		TokenEndpoint:         "http://tokens.example/token",
		JWKSURI:               "http://keys.example/certs",
	}, true)
	if !errors.Is(err, ErrUnexpectedEndpoint) || !strings.Contains(err.Error(), "authorization_endpoint must use HTTPS") {
		t.Fatalf("validateDiscovery() error = %v", err)
	}
}

func TestExchangeValidatesAuthorizedPartyAndAccessTokenHash(t *testing.T) {
	tests := []struct {
		name        string
		claims      map[string]any
		want        error
		wantSuccess bool
	}{
		{
			name: "valid", wantSuccess: true,
			claims: map[string]any{"aud": []string{"moyro", "account"}, "azp": "moyro"},
		},
		{
			name: "wrong authorized party", want: ErrAuthorizedParty,
			claims: map[string]any{"aud": []string{"moyro", "account"}, "azp": "other-client"},
		},
		{
			name: "missing authorized party for multiple audiences", want: ErrAuthorizedParty,
			claims: map[string]any{"aud": []string{"moyro", "account"}},
		},
		{
			name: "wrong access token hash", want: ErrAccessTokenHash,
			claims: map[string]any{"aud": "moyro", "azp": "moyro", "at_hash": "wrong"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := exchangeTestManager(t, test.claims)
			identity, err := manager.Exchange(context.Background(), "code", "verifier", "expected-nonce")
			if test.wantSuccess {
				if err != nil || identity == nil || identity.Subject != "subject-1" {
					t.Fatalf("Exchange() = (%+v, %v)", identity, err)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("Exchange() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestExchangeForRemainsBoundToAuthorizationSnapshotAcrossActivation(t *testing.T) {
	manager := exchangeTestManager(t, map[string]any{"aud": "moyro"})
	manager.mu.RLock()
	firstActivatedAt := manager.current.activated
	manager.mu.RUnlock()
	clock := firstActivatedAt.Add(20 * time.Minute)
	manager.now = func() time.Time { return clock }
	// Model a flow captured after A has already been current for longer than
	// the retention interval. Its validity begins when A is retired, not when A
	// was originally activated.
	original, ok := manager.CurrentSnapshot()
	if !ok || original.ID == "" {
		t.Fatal("original provider snapshot is unavailable")
	}
	if _, err := manager.AuthCodeURLFor(original.ID, "state", "nonce", "verifier"); err != nil {
		t.Fatalf("create flow against long-running current snapshot: %v", err)
	}

	var replacementServer *httptest.Server
	replacementServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case discoveryDocumentSuffix:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": replacementServer.URL, "authorization_endpoint": replacementServer.URL + "/auth",
				"token_endpoint": replacementServer.URL + "/token", "jwks_uri": replacementServer.URL + "/certs",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/certs":
			_ = json.NewEncoder(w).Encode(testJWKS)
		default:
			http.NotFound(w, r)
		}
	}))
	defer replacementServer.Close()
	prepared, err := manager.Prepare(context.Background(), Config{
		Enabled: true, IssuerURL: replacementServer.URL, ClientID: "replacement-client", ClientSecret: "secret",
		RedirectURL: "https://moyro.example/callback", RequireVerifiedEmail: true,
	})
	if err != nil {
		t.Fatalf("prepare replacement provider: %v", err)
	}
	if err := manager.Activate(prepared); err != nil {
		t.Fatalf("activate replacement provider: %v", err)
	}
	current, ok := manager.CurrentSnapshot()
	if !ok || current.ID == original.ID || current.Config.IssuerURL != replacementServer.URL {
		t.Fatalf("current snapshot = %#v", current)
	}

	result, err := manager.ExchangeFor(context.Background(), original.ID, "code", "verifier", "expected-nonce")
	if err != nil {
		t.Fatalf("exchange against retained snapshot: %v", err)
	}
	if result.SnapshotID != original.ID || result.Config.IssuerURL != original.Config.IssuerURL || result.Identity.Subject != "subject-1" {
		t.Fatalf("exchange result crossed provider snapshots: %#v", result)
	}
	manager.mu.RLock()
	originalRetiredAt := manager.snapshots[original.ID].retiredAt
	manager.mu.RUnlock()
	if !originalRetiredAt.Equal(clock) {
		t.Fatalf("original retired_at = %v, want replacement time %v", originalRetiredAt, clock)
	}
	clock = originalRetiredAt.Add(snapshotRetention - time.Millisecond)
	if _, err := manager.loadSnapshot(original.ID); err != nil {
		t.Fatalf("snapshot expired before retirement retention elapsed: %v", err)
	}
	clock = originalRetiredAt.Add(snapshotRetention)
	if _, err := manager.loadSnapshot(original.ID); !errors.Is(err, ErrInvalidFlow) {
		t.Fatalf("expired retained snapshot error = %v, want ErrInvalidFlow", err)
	}
	manager.mu.RLock()
	_, retained := manager.snapshots[original.ID]
	manager.mu.RUnlock()
	if retained {
		t.Fatal("expired non-current snapshot and client secret remained retained")
	}
}

func TestActivationCapacityNeverEvictsUnexpiredFlows(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	manager := NewManager(nil)
	manager.now = func() time.Time { return clock }

	ids := make([]string, 0, maxRetainedSnapshots)
	for index := 0; index < maxRetainedSnapshots; index++ {
		id := fmt.Sprintf("snapshot-%02d", index)
		ids = append(ids, id)
		prepared := preparedSnapshotForRetentionTest(manager, id, fmt.Sprintf("secret-%02d", index))
		if err := manager.CheckActivationCapacity(prepared); err != nil {
			t.Fatalf("capacity preflight %d: %v", index, err)
		}
		if err := manager.Activate(prepared); err != nil {
			t.Fatalf("activate %d: %v", index, err)
		}
		clock = clock.Add(time.Second)
	}

	manager.mu.RLock()
	oldest := manager.snapshots[ids[0]]
	currentID := manager.current.id
	count := len(manager.snapshots)
	manager.mu.RUnlock()
	if count != maxRetainedSnapshots || oldest == nil || oldest.config.ClientSecret != "secret-00" {
		t.Fatalf("retained snapshots = %d, oldest=%#v", count, oldest)
	}

	blocked := preparedSnapshotForRetentionTest(manager, "snapshot-blocked", "secret-blocked")
	if err := manager.CheckActivationCapacity(blocked); !errors.Is(err, ErrSnapshotCapacity) {
		t.Fatalf("capacity preflight error = %v, want ErrSnapshotCapacity", err)
	}
	if err := manager.Activate(blocked); !errors.Is(err, ErrSnapshotCapacity) {
		t.Fatalf("capacity activation error = %v, want ErrSnapshotCapacity", err)
	}
	manager.mu.RLock()
	_, oldestRetained := manager.snapshots[ids[0]]
	_, blockedPublished := manager.snapshots["snapshot-blocked"]
	unchangedCurrentID := manager.current.id
	unchangedCount := len(manager.snapshots)
	manager.mu.RUnlock()
	if !oldestRetained || blockedPublished || unchangedCurrentID != currentID || unchangedCount != maxRetainedSnapshots {
		t.Fatalf("capacity rejection mutated live snapshots: oldest=%v blocked=%v current=%q count=%d", oldestRetained, blockedPublished, unchangedCurrentID, unchangedCount)
	}

	clock = oldest.retiredAt.Add(snapshotRetention)
	if err := manager.CheckActivationCapacity(blocked); err != nil {
		t.Fatalf("capacity did not recover after oldest retirement expired: %v", err)
	}
	if err := manager.Activate(blocked); err != nil {
		t.Fatalf("activate after retention expiry: %v", err)
	}
	manager.mu.RLock()
	_, oldestRetained = manager.snapshots[ids[0]]
	_, blockedPublished = manager.snapshots["snapshot-blocked"]
	count = len(manager.snapshots)
	manager.mu.RUnlock()
	if oldestRetained || !blockedPublished || count > maxRetainedSnapshots {
		t.Fatalf("expired secret cleanup/publish = oldest:%v blocked:%v count:%d", oldestRetained, blockedPublished, count)
	}
}

func TestCurrentSnapshotRedactsSecretAndDisableDropsRetainedSnapshots(t *testing.T) {
	manager := NewManager(nil)
	first := preparedSnapshotForRetentionTest(manager, "first", "first-secret")
	second := preparedSnapshotForRetentionTest(manager, "second", "second-secret")
	if err := manager.Activate(first); err != nil {
		t.Fatal(err)
	}
	if err := manager.Activate(second); err != nil {
		t.Fatal(err)
	}
	binding, ok := manager.CurrentSnapshot()
	if !ok || binding.ID != "second" || binding.Config.ClientSecret != "" {
		t.Fatalf("public current snapshot leaked secret or identity: %#v", binding)
	}
	manager.Disable()
	if manager.Enabled() {
		t.Fatal("manager remained enabled after Disable")
	}
	if _, ok := manager.CurrentSnapshot(); ok {
		t.Fatal("disabled manager returned a current snapshot")
	}
	manager.mu.RLock()
	current := manager.current
	snapshots := manager.snapshots
	manager.mu.RUnlock()
	if current != nil || snapshots != nil {
		t.Fatalf("Disable retained snapshot/secret references: current=%v snapshots=%v", current != nil, snapshots != nil)
	}
	if _, err := manager.loadSnapshot("first"); !errors.Is(err, ErrInvalidFlow) {
		t.Fatalf("disabled retained snapshot lookup error = %v, want ErrInvalidFlow", err)
	}
}

func preparedSnapshotForRetentionTest(manager *Manager, id, secret string) *Prepared {
	return &Prepared{owner: manager, next: &snapshot{
		id: id,
		config: Config{
			Enabled: true, IssuerURL: "https://issuer.example/" + id,
			ClientID: "client-" + id, ClientSecret: secret,
		},
		oauth2: oauth2.Config{ClientSecret: secret},
	}}
}

func exchangeTestManager(t *testing.T, extraClaims map[string]any) *Manager {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const accessToken = "access-token"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case discoveryDocumentSuffix:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": server.URL, "authorization_endpoint": server.URL + "/auth",
				"token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/certs",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/certs":
			_ = json.NewEncoder(w).Encode(rsaJWKS(&privateKey.PublicKey))
		case "/token":
			claims := jwt.MapClaims{
				"iss": server.URL, "sub": "subject-1", "exp": time.Now().Add(time.Hour).Unix(),
				"iat": time.Now().Unix(), "nonce": "expected-nonce", "preferred_username": "operator",
				"email": "operator@example.com", "email_verified": true,
			}
			for key, value := range extraClaims {
				claims[key] = value
			}
			if _, exists := claims["at_hash"]; !exists {
				claims["at_hash"] = accessTokenHash(accessToken)
			}
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			token.Header["kid"] = "test-key"
			rawToken, signErr := token.SignedString(privateKey)
			if signErr != nil {
				t.Errorf("sign token: %v", signErr)
				http.Error(w, "sign token", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": accessToken, "token_type": "Bearer", "expires_in": 300, "id_token": rawToken,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	manager := NewManager(server.Client())
	if err := manager.Configure(context.Background(), Config{
		Enabled: true, IssuerURL: server.URL, ClientID: "moyro", ClientSecret: "secret",
		RedirectURL: "https://moyro.example/callback", RequireVerifiedEmail: true,
	}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	return manager
}

func rsaJWKS(publicKey *rsa.PublicKey) map[string]any {
	exponent := big.NewInt(int64(publicKey.E)).Bytes()
	return map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "test-key",
		"n": base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(exponent),
	}}}
}

func accessTokenHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(digest[:len(digest)/2])
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
