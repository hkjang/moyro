package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/hkjang/moyro/server/internal/apikeys"
	"github.com/hkjang/moyro/server/internal/application/postcommand"
	"github.com/hkjang/moyro/server/internal/auth"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/mcpserver"
	"github.com/hkjang/moyro/server/internal/oidcauth"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/secrets"
	"github.com/hkjang/moyro/server/internal/settings"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/hkjang/moyro/server/internal/teams"
)

func TestCanonicalMCPResource(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]string{
		"https://chat.corp.example":          "https://chat.corp.example/mcp",
		"https://chat.corp.example/":         "https://chat.corp.example/mcp",
		" https://chat.corp.example/mcp/ ":   "https://chat.corp.example/mcp",
		"https://chat.corp.example/chat/mcp": "https://chat.corp.example/chat/mcp",
		"http://127.0.0.1:8065/mcp":          "http://127.0.0.1:8065/mcp",
	} {
		got, err := canonicalMCPResource(raw)
		if err != nil || got != want {
			t.Errorf("canonicalMCPResource(%q) = (%q, %v), want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{
		"chat.corp.example/mcp", "https://chat.corp.example/api", "https://chat.corp.example/mcp?x=1",
		"https://chat.corp.example/mcp#f", "https://user:pw@chat.corp.example/mcp", "ftp://chat.corp.example/mcp", "",
	} {
		if _, err := canonicalMCPResource(raw); err == nil {
			t.Errorf("canonicalMCPResource(%q) accepted", raw)
		}
	}
	if got := mcpOAuthMetadataURL("https://chat.corp.example/mcp"); got != "https://chat.corp.example/.well-known/oauth-protected-resource/mcp" {
		t.Fatalf("metadata URL = %q", got)
	}
	if got := mcpOAuthMetadataURL("https://chat.corp.example/chat/mcp"); got != "https://chat.corp.example/.well-known/oauth-protected-resource/chat/mcp" {
		t.Fatalf("prefixed metadata URL = %q", got)
	}
}

func TestValidateMCPOAuthSettings(t *testing.T) {
	t.Parallel()
	off := defaultMCPOAuthSettings()
	if err := validateMCPOAuthSettings(&off, false, ""); err != nil {
		t.Fatalf("default (off) settings rejected: %v", err)
	}
	emptyScopes := mcpOAuthSettingsView{Scopes: []string{}}
	if err := validateMCPOAuthSettings(&emptyScopes, false, ""); err != nil || len(emptyScopes.Scopes) != 1 {
		t.Fatalf("disabled with empty scopes = (%v, %v), want default scope restored", emptyScopes.Scopes, err)
	}
	for name, tc := range map[string]struct {
		value   mcpOAuthSettingsView
		oidc    bool
		baseURL string
		want    string
	}{
		"needs oidc":        {value: mcpOAuthSettingsView{Enabled: true, Scopes: []string{"mcp_read"}}, baseURL: "https://chat.corp.example", want: "Keycloak"},
		"needs resource":    {value: mcpOAuthSettingsView{Enabled: true, Scopes: []string{"mcp_read"}}, oidc: true, want: "resource identifier"},
		"needs a scope":     {value: mcpOAuthSettingsView{Enabled: true}, oidc: true, baseURL: "https://chat.corp.example", want: "scope is required"},
		"unknown scope":     {value: mcpOAuthSettingsView{Scopes: []string{"manage_system"}}, want: "unsupported"},
		"bad resource":      {value: mcpOAuthSettingsView{Resource: "chat.corp.example"}, want: "absolute"},
		"quoted audience":   {value: mcpOAuthSettingsView{Audience: []string{`claude"mcp`}}, want: "audiences"},
		"too many audience": {value: mcpOAuthSettingsView{Audience: make([]string, 33)}, want: "at most 32"},
	} {
		value := tc.value
		err := validateMCPOAuthSettings(&value, tc.oidc, tc.baseURL)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want containing %q", name, err, tc.want)
		}
	}
	ok := mcpOAuthSettingsView{Enabled: true, Resource: "https://chat.corp.example/", Audience: []string{" claude-mcp ", "claude-mcp", ""}, Scopes: []string{"mcp_write", "mcp_read"}}
	if err := validateMCPOAuthSettings(&ok, true, ""); err != nil {
		t.Fatalf("valid settings rejected: %v", err)
	}
	if ok.Resource != "https://chat.corp.example/mcp" || strings.Join(ok.Audience, ",") != "claude-mcp" || strings.Join(ok.Scopes, ",") != "mcp_read,mcp_write" {
		t.Fatalf("normalised settings = %+v", ok)
	}
}

// fakeIdP is a Keycloak stand-in: a real RSA key pair, a discovery document,
// and the JWKS that verifies what it signs.
type fakeIdP struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	issuer string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &fakeIdP{key: key}
	idp.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/corp/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                                idp.issuer,
				"authorization_endpoint":                idp.issuer + "/protocol/openid-connect/auth",
				"token_endpoint":                        idp.issuer + "/protocol/openid-connect/token",
				"jwks_uri":                              idp.issuer + "/protocol/openid-connect/certs",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/realms/corp/protocol/openid-connect/certs":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
				"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "corp-1",
				"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(idp.server.Close)
	idp.issuer = idp.server.URL + "/realms/corp"
	return idp
}

// token signs claims the way Keycloak would; the defaults are a healthy
// access token and each test overrides what it wants broken.
func (idp *fakeIdP) token(t *testing.T, override map[string]any) string {
	t.Helper()
	now := time.Now()
	claims := jwt.MapClaims{
		"iss": idp.issuer, "sub": "kc-subject-1", "typ": "Bearer", "azp": "claude-mcp",
		"aud": []string{"account"}, "exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(),
		"scope": "openid profile email", "preferred_username": "sso-person",
	}
	for k, v := range override {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	method := jwt.SigningMethodRS256
	token := jwt.NewWithClaims(method, claims)
	token.Header["kid"] = "corp-1"
	signed, err := token.SignedString(idp.key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

type mcpOAuthFixture struct {
	t         *testing.T
	router    http.Handler
	native    *nativeServices
	auth      *auth.Service
	db        *store.DB
	idp       *fakeIdP
	resource  string
	userID    string
	logBuffer *bytes.Buffer
}

// newMCPOAuthFixture mounts /mcp with the production middleware chain in
// front of the real MCP handler, plus one REST route, on a fresh schema.
func newMCPOAuthFixture(t *testing.T) *mcpOAuthFixture {
	t.Helper()
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	secretManager, err := secrets.New(bytes.Repeat([]byte{0x42}, secrets.MasterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.New(db, []byte("mcp-oauth-signing-key-32-bytes!!"), time.Hour, secretManager)
	settingsService, err := settings.NewPostgres(db.Pool, secretManager)
	if err != nil {
		t.Fatal(err)
	}
	rbacService, err := rbac.NewPostgres(db.Pool)
	if err != nil {
		t.Fatal(err)
	}
	keyService, err := apikeys.NewPostgres(db.Pool, secretManager, apikeys.RBACGrantValidator{Authorizer: rbacService}, apikeys.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	idp := newFakeIdP(t)
	oidcManager := oidcauth.NewManager(idp.server.Client())
	if err := oidcManager.Configure(ctx, oidcauth.Config{
		Enabled: true, IssuerURL: idp.issuer, ClientID: "moyro-web", ClientSecret: "secret",
		RedirectURL: "https://chat.corp.example" + oidcCallbackPath,
	}); err != nil {
		t.Fatalf("configure oidc: %v", err)
	}
	logBuffer := &bytes.Buffer{}
	h := &handlers{auth: authService, logger: slog.New(slog.NewTextHandler(logBuffer, nil))}
	h.teams, h.channels, h.posts = teams.New(db), channels.New(db), posts.New(db)
	native := &nativeServices{settings: settingsService, secrets: secretManager, rbac: rbacService, apiKeys: keyService, oidc: oidcManager}
	native.applySite(siteSettingsView{SiteName: "moyro", PublicBaseURL: "https://chat.corp.example"}, nil)
	h.native = native
	mcpService, err := mcpserver.New(mcpserver.Dependencies{
		Teams: h.teams, Channels: h.channels, Posts: h.posts, PostCommands: postcommand.New(postcommand.Dependencies{}),
		UserID: UserIDFromContext,
		Authorize: func(ctx context.Context, permission, _, _ string) (bool, error) {
			principal, ok := PrincipalFromContext(ctx)
			if !ok {
				return false, nil
			}
			return rbacService.Allowed(ctx, principal, permission, rbac.Scope{})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	native.mcp = mcpService
	mcpService.ConfigurePolicy(defaultMCPSettings().AllowedTools, defaultMCPSettings().AllowedResources)

	user, err := authService.Register(ctx, "sso-person", "sso-person@example.test", "long-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO user_identities (user_id, provider, subject, email, create_at) VALUES ($1,$2,$3,$4,$5)`,
		user.ID, keycloakProviderKey(idp.issuer), "kc-subject-1", user.Email, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}

	router := chi.NewRouter()
	router.With(h.mcpAuthChain()...).Handle("/mcp", mcpService.Handler())
	router.Get(mcpOAuthMetadataPath, h.protectedResourceMetadata)
	router.Get(mcpOAuthMetadataPath+"/*", h.protectedResourceMetadata)
	router.With(h.requireAuth).Get("/api/v4/users/me", h.me)
	return &mcpOAuthFixture{
		t: t, router: router, native: native, auth: authService, db: db, idp: idp,
		resource: "https://chat.corp.example/mcp", userID: user.ID, logBuffer: logBuffer,
	}
}

func (f *mcpOAuthFixture) saveMCP(mutate func(*mcpSettingsView)) {
	f.t.Helper()
	value := defaultMCPSettings()
	value.Enabled = true
	mutate(&value)
	if _, err := f.native.settings.PutJSON(context.Background(), "mcp", nativeSettingsKey, value, f.userID, nil); err != nil {
		f.t.Fatal(err)
	}
}

func (f *mcpOAuthFixture) call(method, path, bearer string, body []byte) *httptest.ResponseRecorder {
	f.t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
	}
	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, request)
	return recorder
}

var toolsListBody = []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)

func (f *mcpOAuthFixture) toolsList(bearer string) *httptest.ResponseRecorder {
	return f.call(http.MethodPost, "/mcp", bearer, toolsListBody)
}

func (f *mcpOAuthFixture) userCount() int {
	var count int
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		f.t.Fatal(err)
	}
	return count
}

func TestMCPOAuthDisabledChangesNothingPostgres(t *testing.T) {
	f := newMCPOAuthFixture(t)
	f.saveMCP(func(*mcpSettingsView) {})

	if rec := f.call(http.MethodGet, mcpOAuthMetadataPath, "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("metadata while off = %d, want 404", rec.Code)
	}
	if rec := f.call(http.MethodGet, mcpOAuthMetadataPath+"/mcp", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("metadata/mcp while off = %d, want 404", rec.Code)
	}
	// A perfectly good Keycloak token is refused exactly like any unknown
	// bearer on a key-only install, and the 401 says nothing about SSO.
	rec := f.toolsList(f.idp.token(t, map[string]any{"aud": []string{f.resource}}))
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "" || strings.Contains(rec.Body.String(), "SSO") {
		t.Fatalf("token while off: status=%d challenge=%q body=%s", rec.Code, rec.Header().Get("WWW-Authenticate"), rec.Body.String())
	}
	if rec := f.toolsList(""); rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("no token while off: status=%d challenge=%q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
}

func TestMCPOAuthMetadataAndChallengePostgres(t *testing.T) {
	f := newMCPOAuthFixture(t)
	f.saveMCP(func(value *mcpSettingsView) { value.OAuth.Enabled = true })

	for _, path := range []string{mcpOAuthMetadataPath, mcpOAuthMetadataPath + "/mcp"} {
		rec := f.call(http.MethodGet, path, "", nil)
		if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("%s: status=%d cors=%q body=%s", path, rec.Code, rec.Header().Get("Access-Control-Allow-Origin"), rec.Body.String())
		}
		var doc map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		if doc["resource"] != f.resource || doc["resource_name"] != "moyro MCP" {
			t.Fatalf("%s: document = %v", path, doc)
		}
		if servers, _ := doc["authorization_servers"].([]any); len(servers) != 1 || servers[0] != f.idp.issuer {
			t.Fatalf("%s: authorization_servers = %v", path, doc["authorization_servers"])
		}
		if methods, _ := doc["bearer_methods_supported"].([]any); len(methods) != 1 || methods[0] != "header" {
			t.Fatalf("%s: bearer_methods_supported = %v", path, doc["bearer_methods_supported"])
		}
		if scopes, _ := doc["scopes_supported"].([]any); len(scopes) != 1 || scopes[0] != "mcp_read" {
			t.Fatalf("%s: scopes_supported = %v", path, doc["scopes_supported"])
		}
		if _, envelope := doc["status_code"]; envelope {
			t.Fatalf("%s: metadata is wrapped in the API error envelope", path)
		}
	}

	wantChallenge := `Bearer realm="moyro", resource_metadata="https://chat.corp.example/.well-known/oauth-protected-resource/mcp"`
	rec := f.toolsList("")
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != wantChallenge {
		t.Fatalf("no token: status=%d challenge=%q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
	rec = f.toolsList("not-a-key-and-not-a-jwt")
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != wantChallenge+`, error="invalid_token"` {
		t.Fatalf("garbage bearer: status=%d challenge=%q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
	// REST 401s never carry the challenge: a browser must not be sent to Keycloak.
	rec = f.call(http.MethodGet, "/api/v4/users/me", "", nil)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("REST 401: status=%d challenge=%q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
}

func TestMCPOAuthAcceptsTokensForThisResourcePostgres(t *testing.T) {
	f := newMCPOAuthFixture(t)
	f.saveMCP(func(value *mcpSettingsView) { value.OAuth.Enabled = true })

	rec := f.toolsList(f.idp.token(t, map[string]any{"aud": []string{"account", f.resource}}))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"tools"`) {
		t.Fatalf("aud-bound token: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Keycloak 26 shape: aud=[account], client id only in azp. Refused until
	// the administrator lists the client — and the refusal says exactly that.
	rec = f.toolsList(f.idp.token(t, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("azp-only token before allow-listing: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.HasSuffix(rec.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Fatalf("rejected token challenge = %q", rec.Header().Get("WWW-Authenticate"))
	}
	var refusal apiError
	if err := json.Unmarshal(rec.Body.Bytes(), &refusal); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`aud=[account]`, `azp="claude-mcp"`, `add "claude-mcp"`, `Audience mapper with "https://chat.corp.example/mcp"`} {
		if !strings.Contains(refusal.Message, want) {
			t.Fatalf("audience refusal %q lacks %q", refusal.Message, want)
		}
	}

	f.saveMCP(func(value *mcpSettingsView) {
		value.OAuth.Enabled = true
		value.OAuth.Audience = []string{"claude-mcp"}
	})
	rec = f.toolsList(f.idp.token(t, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"tools"`) {
		t.Fatalf("azp allow-listed token: status=%d body=%s", rec.Code, rec.Body.String())
	}
	// A token for another realm client, with neither our resource in aud
	// nor an allow-listed azp, stays out.
	rec = f.toolsList(f.idp.token(t, map[string]any{"azp": "other-app", "aud": []string{"other-app"}}))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("other app's token: status=%d body=%s", rec.Code, rec.Body.String())
	}
	// The SSO subject passes through the same policy gate a key does.
	f.saveMCP(func(value *mcpSettingsView) {
		value.OAuth.Enabled = true
		value.OAuth.Audience = []string{"claude-mcp"}
		value.RequiredScopes = []string{"mcp_write"}
	})
	if rec := f.toolsList(f.idp.token(t, nil)); rec.Code != http.StatusForbidden {
		t.Fatalf("scope gate: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMCPOAuthRejectsBrokenTokensAndLogsWhyPostgres(t *testing.T) {
	f := newMCPOAuthFixture(t)
	f.saveMCP(func(value *mcpSettingsView) { value.OAuth.Enabled = true })
	good := map[string]any{"aud": []string{f.resource}}
	with := func(extra map[string]any) map[string]any {
		merged := map[string]any{}
		for k, v := range good {
			merged[k] = v
		}
		for k, v := range extra {
			merged[k] = v
		}
		return merged
	}
	otherIdP := newFakeIdP(t)

	cases := map[string]struct {
		token   string
		message string
		logged  string
	}{
		"expired":       {token: f.idp.token(t, with(map[string]any{"exp": time.Now().Add(-time.Minute).Unix()})), message: "not valid", logged: "expired"},
		"not yet valid": {token: f.idp.token(t, with(map[string]any{"nbf": time.Now().Add(time.Hour).Unix()})), message: "not valid", logged: "nbf"},
		"other issuer":  {token: otherIdP.token(t, good), message: "not valid", logged: "issuer"},
		"id token":      {token: f.idp.token(t, with(map[string]any{"typ": "ID"})), message: "ID token", logged: "ID token"},
		"bound (cnf)":   {token: f.idp.token(t, with(map[string]any{"cnf": map[string]any{"jkt": "x"}})), message: "proof-of-possession", logged: "proof-of-possession"},
		"no subject":    {token: f.idp.token(t, with(map[string]any{"sub": nil})), message: "no subject", logged: "no subject"},
	}
	hsToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": f.idp.issuer, "sub": "kc-subject-1", "typ": "Bearer", "aud": []string{f.resource}, "exp": time.Now().Add(time.Minute).Unix(),
	}).SignedString([]byte("shared-secret"))
	if err != nil {
		t.Fatal(err)
	}
	cases["hs256"] = struct {
		token   string
		message string
		logged  string
	}{token: hsToken, message: "not valid", logged: "HS256"}

	for name, tc := range cases {
		f.logBuffer.Reset()
		rec := f.toolsList(tc.token)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), tc.message) {
			t.Errorf("%s: status=%d body=%s", name, rec.Code, rec.Body.String())
		}
		if !strings.Contains(f.logBuffer.String(), "MCP SSO token rejected") || !strings.Contains(f.logBuffer.String(), tc.logged) {
			t.Errorf("%s: server log lacks the cause %q: %s", name, tc.logged, f.logBuffer.String())
		}
	}
}

func TestMCPOAuthNeverCreatesOrRevivesAccountsPostgres(t *testing.T) {
	f := newMCPOAuthFixture(t)
	f.saveMCP(func(value *mcpSettingsView) { value.OAuth.Enabled = true })
	before := f.userCount()

	rec := f.toolsList(f.idp.token(t, map[string]any{"aud": []string{f.resource}, "sub": "kc-stranger", "preferred_username": "stranger"}))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "sign in to the web once first") {
		t.Fatalf("unknown subject: status=%d body=%s", rec.Code, rec.Body.String())
	}
	// A local account whose username happens to match the token's
	// preferred_username is not the same person until the web sign-in links it.
	if _, err := f.auth.Register(context.Background(), "stranger", "stranger@example.test", "long-test-password"); err != nil {
		t.Fatal(err)
	}
	rec = f.toolsList(f.idp.token(t, map[string]any{"aud": []string{f.resource}, "sub": "kc-stranger", "preferred_username": "stranger"}))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("username-only match: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := f.userCount(); got != before+1 {
		t.Fatalf("user count = %d, want %d (no account created by a token)", got, before+1)
	}

	if _, err := f.auth.Deactivate(context.Background(), f.userID); err != nil {
		t.Fatal(err)
	}
	rec = f.toolsList(f.idp.token(t, map[string]any{"aud": []string{f.resource}}))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "inactive") {
		t.Fatalf("deactivated account: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMCPOAuthTokensStayOnMCPAndKeysStillWorkPostgres(t *testing.T) {
	f := newMCPOAuthFixture(t)
	f.saveMCP(func(value *mcpSettingsView) { value.OAuth.Enabled = true })
	token := f.idp.token(t, map[string]any{"aud": []string{f.resource}})
	if rec := f.toolsList(token); rec.Code != http.StatusOK {
		t.Fatalf("sanity: token on /mcp status=%d", rec.Code)
	}
	rec := f.call(http.MethodGet, "/api/v4/users/me", token, nil)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("SSO token on REST: status=%d challenge=%q body=%s", rec.Code, rec.Header().Get("WWW-Authenticate"), rec.Body.String())
	}

	created, err := f.native.apiKeys.Create(context.Background(), apikeys.CreateRequest{
		OwnerUserID: f.userID, CreatedBy: f.userID, Name: "laptop", Kind: apikeys.KindMCP, Permissions: []string{"mcp_read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec := f.toolsList(created.Secret); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"tools"`) {
		t.Fatalf("personal key with SSO on: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := f.toolsList(apikeys.SecretPrefix + "definitely-not-a-key"); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid API key") {
		t.Fatalf("bad key with SSO on: status=%d body=%s", rec.Code, rec.Body.String())
	}
	f.saveMCP(func(*mcpSettingsView) {})
	if rec := f.toolsList(created.Secret); rec.Code != http.StatusOK {
		t.Fatalf("personal key with SSO off: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMCPOAuthPatchRefusesUnworkableSettingsPostgres(t *testing.T) {
	f := newMCPOAuthFixture(t)
	h := &handlers{auth: f.auth, native: f.native, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	router := chi.NewRouter()
	router.Patch("/settings/{section}", h.patchNativeSettings)
	router.Get("/settings/{section}", h.getNativeSettings)
	patch := func(value mcpSettingsView) *httptest.ResponseRecorder {
		body, _ := json.Marshal(value)
		request := httptest.NewRequest(http.MethodPatch, "/settings/mcp", bytes.NewReader(body))
		request = request.WithContext(SetUserIDOnContext(request.Context(), f.userID))
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}

	value := defaultMCPSettings()
	value.Enabled, value.OAuth.Enabled = true, true
	value.OAuth.Scopes = []string{"manage_system"}
	if rec := patch(value); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unsupported MCP SSO scope") {
		t.Fatalf("role-like scope: status=%d body=%s", rec.Code, rec.Body.String())
	}
	f.native.applySite(siteSettingsView{SiteName: "moyro"}, nil)
	value.OAuth.Scopes = []string{"mcp_read"}
	if rec := patch(value); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "resource identifier") {
		t.Fatalf("no resource: status=%d body=%s", rec.Code, rec.Body.String())
	}
	value.OAuth.Resource = "https://chat.corp.example/"
	value.OAuth.Audience = []string{"claude-mcp"}
	rec := patch(value)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var saved mcpSettingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.OAuthStatus.Active || saved.OAuthStatus.Resource != "https://chat.corp.example/mcp" ||
		saved.OAuthStatus.MetadataURL != "https://chat.corp.example/.well-known/oauth-protected-resource/mcp" ||
		saved.OAuthStatus.IssuerURL != f.idp.issuer || saved.OAuth.Resource != "https://chat.corp.example/mcp" {
		t.Fatalf("saved response = %+v", saved)
	}
	getReq := httptest.NewRequest(http.MethodGet, "/settings/mcp", nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK || !strings.Contains(getRec.Body.String(), `"oauth_status":{"active":true`) {
		t.Fatalf("get: status=%d body=%s", getRec.Code, getRec.Body.String())
	}
	if rec := f.call(http.MethodGet, mcpOAuthMetadataPath+"/mcp", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("metadata after explicit resource: status=%d", rec.Code)
	}
	// Turning Keycloak off afterwards leaves the stored flag on but the door
	// closed: metadata 404, tokens refused, reason in the log.
	f.native.oidc.Disable()
	if rec := f.call(http.MethodGet, mcpOAuthMetadataPath+"/mcp", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("metadata with oidc off: status=%d", rec.Code)
	}
	if !strings.Contains(f.logBuffer.String(), "Keycloak SSO is not enabled") {
		t.Fatalf("log lacks the inactive reason: %s", f.logBuffer.String())
	}
}
