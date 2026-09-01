// Command fakeoidc is a browser-test-only OpenID Provider used by the release
// gate. It is built into a dedicated Docker target and is never copied into the
// production Moyro image.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const keyID = "moyro-e2e-key"

type authorization struct {
	clientID, redirectURI, nonce, challenge string
}

type provider struct {
	issuer, browserBase string
	key                 *rsa.PrivateKey
	mu                  sync.Mutex
	codes               map[string]authorization
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return strings.TrimRight(value, "/")
	}
	return fallback
}

func main() {
	p := &provider{
		issuer:      env("FAKE_OIDC_ISSUER", "http://127.0.0.1:18066"),
		browserBase: env("FAKE_OIDC_BROWSER_BASE", "http://127.0.0.1:18066"),
		codes:       make(map[string]authorization),
	}
	var err error
	p.key, err = rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /jwks", p.jwks)
	mux.HandleFunc("GET /authorize", p.authorize)
	mux.HandleFunc("POST /authorize", p.authorize)
	mux.HandleFunc("POST /token", p.token)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	server := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("fake OIDC listening on %s (browser %s)", p.issuer, p.browserBase)
	log.Fatal(server.ListenAndServe())
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}

func (p *provider) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                                p.issuer,
		"authorization_endpoint":                p.browserBase + "/authorize",
		"token_endpoint":                        p.issuer + "/token",
		"jwks_uri":                              p.issuer + "/jwks",
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"scopes_supported":                      []string{"openid", "profile", "email"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (p *provider) jwks(w http.ResponseWriter, _ *http.Request) {
	exponent := big.NewInt(int64(p.key.PublicKey.E)).Bytes()
	writeJSON(w, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": keyID,
		"n": base64.RawURLEncoding.EncodeToString(p.key.PublicKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(exponent),
	}}})
}

var authorizePage = template.Must(template.New("authorize").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Moyro test identity</title></head>
<body><main><h1>Test identity provider</h1><p>Sign in as the release-gate user.</p>
<form method="post">{{range $key, $values := .}}{{range $values}}<input type="hidden" name="{{$key}}" value="{{.}}">{{end}}{{end}}
<button type="submit">Continue as E2E User</button></form></main></body></html>`))

func validAuthorization(values url.Values) bool {
	redirect, err := url.Parse(values.Get("redirect_uri"))
	return err == nil && (redirect.Scheme == "http" || redirect.Scheme == "https") &&
		redirect.Host != "" && values.Get("client_id") == "moyro-e2e" &&
		values.Get("response_type") == "code" && values.Get("state") != "" &&
		values.Get("nonce") != "" && values.Get("code_challenge_method") == "S256" &&
		values.Get("code_challenge") != ""
}

func (p *provider) authorize(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || !validAuthorization(r.Form) {
		http.Error(w, "invalid authorization request", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = authorizePage.Execute(w, r.Form)
		return
	}
	code := randomValue(32)
	p.mu.Lock()
	p.codes[code] = authorization{
		clientID: r.Form.Get("client_id"), redirectURI: r.Form.Get("redirect_uri"),
		nonce: r.Form.Get("nonce"), challenge: r.Form.Get("code_challenge"),
	}
	p.mu.Unlock()
	destination, _ := url.Parse(r.Form.Get("redirect_uri"))
	query := destination.Query()
	query.Set("code", code)
	query.Set("state", r.Form.Get("state"))
	destination.RawQuery = query.Encode()
	http.Redirect(w, r, destination.String(), http.StatusFound)
}

func (p *provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	clientID, clientSecret, basic := r.BasicAuth()
	if !basic {
		clientID, clientSecret = r.Form.Get("client_id"), r.Form.Get("client_secret")
	}
	code := r.Form.Get("code")
	p.mu.Lock()
	grant, found := p.codes[code]
	if found {
		delete(p.codes, code)
	}
	p.mu.Unlock()
	challenge := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
	if !found || clientID != grant.clientID || clientSecret != "moyro-e2e-secret" ||
		r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("redirect_uri") != grant.redirectURI ||
		base64.RawURLEncoding.EncodeToString(challenge[:]) != grant.challenge {
		writeJSON(w, map[string]string{"error": "invalid_grant"})
		return
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"iss": p.issuer, "sub": "e2e-user-1", "aud": grant.clientID,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "nonce": grant.nonce,
		"preferred_username": "sso-e2e-user", "email": "sso-e2e@moyro.test",
		"email_verified": true, "name": "SSO E2E User",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = keyID
	rawIDToken, err := token.SignedString(p.key)
	if err != nil {
		http.Error(w, "signing failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"access_token": randomValue(32), "token_type": "Bearer", "expires_in": 300, "id_token": rawIDToken,
	})
}

func randomValue(size int) string {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("random value: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}
