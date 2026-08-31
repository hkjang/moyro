// Package oidcauth implements the dynamically configurable OpenID Connect
// client used for Keycloak SSO. It intentionally keeps browser-flow state out
// of the manager; callers persist one-time state/nonce/PKCE values separately.
package oidcauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

var (
	ErrDisabled           = errors.New("oidc is disabled")
	ErrInvalidConfig      = errors.New("invalid oidc configuration")
	ErrMissingIDToken     = errors.New("oidc token response did not contain an id_token")
	ErrNonceMismatch      = errors.New("oidc nonce mismatch")
	ErrEmailUnverified    = errors.New("oidc email is not verified")
	ErrUnexpectedEndpoint = errors.New("oidc discovery returned an invalid endpoint")
	ErrIssuerMismatch     = errors.New("oidc discovery returned an unexpected issuer")
	ErrAuthorizedParty    = errors.New("oidc authorized party mismatch")
	ErrAccessTokenHash    = errors.New("oidc access token hash mismatch")
)

const (
	discoveryDocumentSuffix = "/.well-known/openid-configuration"
	maxOIDCResponseBytes    = 1 << 20
)

type Config struct {
	Enabled              bool     `json:"enabled"`
	DisplayName          string   `json:"display_name"`
	IssuerURL            string   `json:"issuer_url"`
	ClientID             string   `json:"client_id"`
	ClientSecret         string   `json:"-"`
	RedirectURL          string   `json:"redirect_url"`
	Scopes               []string `json:"scopes"`
	UsernameClaim        string   `json:"username_claim"`
	EmailClaim           string   `json:"email_claim"`
	GroupsClaim          string   `json:"groups_claim"`
	AllowSignup          bool     `json:"allow_signup"`
	RequireVerifiedEmail bool     `json:"require_verified_email"`
	CACertificatePEM     string   `json:"ca_certificate_pem,omitempty"`
}

type Identity struct {
	Subject       string
	Username      string
	Email         string
	EmailVerified bool
	DisplayName   string
	Picture       string
	Groups        []string
	Claims        map[string]any
}

type discoveryMetadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	UserInfoEndpoint      string   `json:"userinfo_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	SigningAlgorithms     []string `json:"id_token_signing_alg_values_supported"`
}

type discoveredProvider struct {
	config   Config
	provider *gooidc.Provider
	client   *http.Client
}

type snapshot struct {
	config   Config
	provider *gooidc.Provider
	oauth2   oauth2.Config
	verifier *gooidc.IDTokenVerifier
	client   *http.Client
}

// Manager atomically replaces a fully discovered provider after an admin
// saves and tests new settings. In-flight requests retain their old immutable
// snapshot; subsequent requests see the new configuration without restart.
type Manager struct {
	mu         sync.RWMutex
	current    *snapshot
	httpClient *http.Client
}

// Prepared is a fully discovered, validated provider snapshot that has not
// yet changed live authentication state. Its internals are deliberately
// private so only the manager that prepared it can activate it.
type Prepared struct {
	owner *Manager
	next  *snapshot
}

func NewManager(client *http.Client) *Manager {
	if client == nil {
		client = &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				Proxy: nil, // do not implicitly consume HTTP_PROXY in an air-gapped deployment
			},
		}
	}
	return &Manager{httpClient: client}
}

func (m *Manager) Configure(ctx context.Context, cfg Config) error {
	if !cfg.Enabled {
		m.Disable()
		return nil
	}
	prepared, err := m.Prepare(ctx, cfg)
	if err != nil {
		return err
	}
	return m.Activate(prepared)
}

// Prepare performs normalization, CA setup, discovery, and endpoint
// validation without mutating the active provider.
func (m *Manager) Prepare(ctx context.Context, cfg Config) (*Prepared, error) {
	if !cfg.Enabled {
		return nil, fmt.Errorf("%w: cannot prepare a disabled provider", ErrInvalidConfig)
	}
	discovered, err := m.discover(ctx, cfg, true)
	if err != nil {
		return nil, err
	}

	oauthCfg := oauth2.Config{
		ClientID:     discovered.config.ClientID,
		ClientSecret: discovered.config.ClientSecret,
		RedirectURL:  discovered.config.RedirectURL,
		Endpoint:     discovered.provider.Endpoint(),
		Scopes:       append([]string(nil), discovered.config.Scopes...),
	}
	discoveryCtx := gooidc.ClientContext(ctx, discovered.client)
	next := &snapshot{
		config:   discovered.config,
		provider: discovered.provider,
		oauth2:   oauthCfg,
		verifier: discovered.provider.VerifierContext(discoveryCtx, &gooidc.Config{ClientID: discovered.config.ClientID}),
		client:   discovered.client,
	}
	return &Prepared{owner: m, next: next}, nil
}

// Probe validates provider discovery and the advertised signing-key document
// without requiring or activating a client secret. A secret is only needed
// when an enabled provider is prepared for the authorization-code exchange.
func (m *Manager) Probe(ctx context.Context, cfg Config) (string, error) {
	discovered, err := m.discover(ctx, cfg, false)
	if err != nil {
		return "", err
	}
	return discovered.config.IssuerURL, nil
}

// IssuerURL returns the exact issuer advertised by the validated discovery
// document. Persisting this value keeps restart-time discovery and ID-token
// issuer checks on the same canonical identity.
func (p *Prepared) IssuerURL() string {
	if p == nil || p.next == nil {
		return ""
	}
	return p.next.config.IssuerURL
}

func (m *Manager) discover(ctx context.Context, cfg Config, requireSecret bool) (*discoveredProvider, error) {
	normalized, err := normalizeConfig(cfg, requireSecret)
	if err != nil {
		return nil, err
	}
	providerClient, err := clientWithCA(m.httpClient, normalized.CACertificatePEM)
	if err != nil {
		return nil, err
	}
	metadata, err := fetchDiscovery(ctx, providerClient, normalized.IssuerURL)
	if err != nil {
		return nil, err
	}
	if err := validateDiscovery(normalized.IssuerURL, metadata); err != nil {
		return nil, err
	}
	advertisedAlgorithms := metadata.SigningAlgorithms
	metadata.SigningAlgorithms = supportedSigningAlgorithms(advertisedAlgorithms)
	if len(advertisedAlgorithms) > 0 && len(metadata.SigningAlgorithms) == 0 {
		return nil, errors.New("oidc discovery does not advertise a supported ID-token signing algorithm")
	}
	keyAlgorithms := metadata.SigningAlgorithms
	if len(keyAlgorithms) == 0 {
		keyAlgorithms = []string{gooidc.RS256}
	}
	if err := probeJWKS(ctx, providerClient, metadata.JWKSURI, keyAlgorithms); err != nil {
		return nil, err
	}
	discoveryCtx := gooidc.ClientContext(ctx, providerClient)
	provider := (&gooidc.ProviderConfig{
		IssuerURL:   metadata.Issuer,
		AuthURL:     metadata.AuthorizationEndpoint,
		TokenURL:    metadata.TokenEndpoint,
		UserInfoURL: metadata.UserInfoEndpoint,
		JWKSURL:     metadata.JWKSURI,
		Algorithms:  metadata.SigningAlgorithms,
	}).NewProvider(discoveryCtx)
	normalized.IssuerURL = metadata.Issuer
	return &discoveredProvider{config: normalized, provider: provider, client: providerClient}, nil
}

func fetchDiscovery(ctx context.Context, client *http.Client, issuer string) (discoveryMetadata, error) {
	discoveryURL := strings.TrimRight(issuer, "/") + discoveryDocumentSuffix
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return discoveryMetadata{}, fmt.Errorf("oidc discovery request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return discoveryMetadata{}, fmt.Errorf("oidc discovery request %q: %w", discoveryURL, err)
	}
	defer resp.Body.Close()
	body, err := readBoundedResponse(resp.Body)
	if err != nil {
		return discoveryMetadata{}, fmt.Errorf("oidc discovery response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return discoveryMetadata{}, fmt.Errorf("oidc discovery request %q returned %s%s", discoveryURL, resp.Status, responseDetail(body))
	}
	var metadata discoveryMetadata
	if err := json.Unmarshal(body, &metadata); err != nil {
		return discoveryMetadata{}, fmt.Errorf("oidc discovery response is not valid JSON: %w", err)
	}
	return metadata, nil
}

func probeJWKS(ctx context.Context, client *http.Client, rawURL string, signingAlgorithms []string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("oidc JWKS request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("oidc JWKS request %q: %w", rawURL, err)
	}
	defer resp.Body.Close()
	body, err := readBoundedResponse(resp.Body)
	if err != nil {
		return fmt.Errorf("oidc JWKS response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oidc JWKS request %q returned %s%s", rawURL, resp.Status, responseDetail(body))
	}
	var document jose.JSONWebKeySet
	if err := json.Unmarshal(body, &document); err != nil {
		return fmt.Errorf("oidc JWKS response is not valid JSON: %w", err)
	}
	for index := range document.Keys {
		if jwkSupportsAnySigningAlgorithm(&document.Keys[index], signingAlgorithms) {
			return nil
		}
	}
	return errors.New("oidc JWKS response does not contain a compatible public signing key")
}

func jwkSupportsAnySigningAlgorithm(key *jose.JSONWebKey, algorithms []string) bool {
	if !key.Valid() || !key.IsPublic() || (key.Use != "" && key.Use != "sig") {
		return false
	}
	for _, algorithm := range algorithms {
		if key.Algorithm != "" && key.Algorithm != algorithm {
			continue
		}
		switch publicKey := key.Key.(type) {
		case *rsa.PublicKey:
			if strings.HasPrefix(algorithm, "RS") || strings.HasPrefix(algorithm, "PS") {
				return true
			}
		case *ecdsa.PublicKey:
			if (algorithm == gooidc.ES256 && publicKey.Curve == elliptic.P256()) ||
				(algorithm == gooidc.ES384 && publicKey.Curve == elliptic.P384()) ||
				(algorithm == gooidc.ES512 && publicKey.Curve == elliptic.P521()) {
				return true
			}
		case ed25519.PublicKey:
			if algorithm == gooidc.EdDSA {
				return true
			}
		}
	}
	return false
}

func readBoundedResponse(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxOIDCResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxOIDCResponseBytes {
		return nil, fmt.Errorf("body exceeds %d bytes", maxOIDCResponseBytes)
	}
	return data, nil
}

func responseDetail(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return ""
	}
	text = strings.Map(func(char rune) rune {
		if char < 0x20 || char == 0x7f {
			return ' '
		}
		return char
	}, text)
	runes := []rune(text)
	if len(runes) > 256 {
		text = string(runes[:256]) + "…"
	}
	return ": " + text
}

func supportedSigningAlgorithms(values []string) []string {
	supported := map[string]struct{}{
		gooidc.RS256: {}, gooidc.RS384: {}, gooidc.RS512: {},
		gooidc.ES256: {}, gooidc.ES384: {}, gooidc.ES512: {},
		gooidc.PS256: {}, gooidc.PS384: {}, gooidc.PS512: {},
		gooidc.EdDSA: {},
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := supported[value]; ok {
			result = append(result, value)
		}
	}
	return result
}

// Activate swaps a previously prepared immutable snapshot without additional
// network or database work, making the live transition failure-free after the
// caller commits its durable configuration.
func (m *Manager) Activate(prepared *Prepared) error {
	if prepared == nil || prepared.owner != m || prepared.next == nil {
		return fmt.Errorf("%w: prepared provider does not belong to this manager", ErrInvalidConfig)
	}
	m.mu.Lock()
	m.current = prepared.next
	m.mu.Unlock()
	return nil
}

func (m *Manager) Disable() {
	m.mu.Lock()
	m.current = nil
	m.mu.Unlock()
}

func (m *Manager) Enabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current != nil
}

func (m *Manager) PublicConfig() (Config, bool) {
	s, err := m.load()
	if err != nil {
		return Config{}, false
	}
	cfg := s.config
	cfg.ClientSecret = ""
	return cfg, true
}

func (m *Manager) AuthCodeURL(state, nonce, verifier string) (string, error) {
	if state == "" || nonce == "" || verifier == "" {
		return "", fmt.Errorf("%w: state, nonce, and PKCE verifier are required", ErrInvalidConfig)
	}
	s, err := m.load()
	if err != nil {
		return "", err
	}
	return s.oauth2.AuthCodeURL(
		state,
		gooidc.Nonce(nonce),
		oauth2.S256ChallengeOption(verifier),
	), nil
}

func (m *Manager) Exchange(ctx context.Context, code, verifier, expectedNonce string) (*Identity, error) {
	if code == "" || verifier == "" || expectedNonce == "" {
		return nil, fmt.Errorf("%w: incomplete callback", ErrInvalidConfig)
	}
	s, err := m.load()
	if err != nil {
		return nil, err
	}
	exchangeCtx := gooidc.ClientContext(ctx, s.client)
	token, err := s.oauth2.Exchange(exchangeCtx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("oidc code exchange: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, ErrMissingIDToken
	}
	idToken, err := s.verifier.Verify(exchangeCtx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("oidc id token verification: %w", err)
	}
	if idToken.Nonce != expectedNonce {
		return nil, ErrNonceMismatch
	}
	if idToken.AccessTokenHash != "" {
		if err := idToken.VerifyAccessToken(token.AccessToken); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrAccessTokenHash, err)
		}
	}

	claims := map[string]any{}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("oidc claims: %w", err)
	}
	authorizedParty := stringClaim(claims, "azp")
	if (len(idToken.Audience) > 1 && authorizedParty == "") ||
		(authorizedParty != "" && authorizedParty != s.config.ClientID) {
		return nil, ErrAuthorizedParty
	}
	identity := identityFromClaims(s.config, claims)
	identity.Subject = idToken.Subject
	if identity.Subject == "" {
		return nil, fmt.Errorf("%w: subject claim is empty", ErrInvalidConfig)
	}
	if s.config.RequireVerifiedEmail && !identity.EmailVerified {
		return nil, ErrEmailUnverified
	}
	return identity, nil
}

func (m *Manager) load() (*snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.current == nil {
		return nil, ErrDisabled
	}
	return m.current, nil
}

func normalizeConfig(cfg Config, requireSecret bool) (Config, error) {
	issuerURL, err := normalizeIssuerURL(cfg.IssuerURL)
	if err != nil {
		return Config{}, err
	}
	cfg.IssuerURL = issuerURL
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	cfg.RedirectURL = strings.TrimSpace(cfg.RedirectURL)
	if cfg.ClientID == "" || cfg.RedirectURL == "" {
		return Config{}, fmt.Errorf("%w: issuer_url, client_id, and redirect_url are required", ErrInvalidConfig)
	}
	if requireSecret && cfg.ClientSecret == "" {
		return Config{}, fmt.Errorf("%w: client_secret is required for an enabled provider", ErrInvalidConfig)
	}
	if err := validateHTTPURL("redirect_url", cfg.RedirectURL, false); err != nil {
		return Config{}, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{gooidc.ScopeOpenID, "profile", "email"}
	}
	seen := map[string]struct{}{gooidc.ScopeOpenID: {}}
	scopes := []string{gooidc.ScopeOpenID}
	for _, scope := range cfg.Scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, exists := seen[scope]; exists {
			continue
		}
		seen[scope] = struct{}{}
		scopes = append(scopes, scope)
	}
	cfg.Scopes = scopes
	if cfg.DisplayName == "" {
		cfg.DisplayName = "Keycloak"
	}
	if cfg.UsernameClaim == "" {
		cfg.UsernameClaim = "preferred_username"
	}
	if cfg.EmailClaim == "" {
		cfg.EmailClaim = "email"
	}
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = "groups"
	}
	return cfg, nil
}

func normalizeIssuerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if err := validateHTTPURL("issuer_url", raw, true); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	parsed, _ := url.Parse(raw)
	escapedPath := strings.TrimRight(parsed.EscapedPath(), "/")
	if strings.HasSuffix(escapedPath, discoveryDocumentSuffix) {
		escapedPath = strings.TrimSuffix(escapedPath, discoveryDocumentSuffix)
	}
	path, err := url.PathUnescape(escapedPath)
	if err != nil {
		return "", fmt.Errorf("%w: issuer_url has an invalid escaped path", ErrInvalidConfig)
	}
	parsed.Path = path
	parsed.RawPath = escapedPath
	if escapedPath == "" {
		parsed.RawPath = ""
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	return parsed.String(), nil
}

func validateDiscovery(issuer string, metadata discoveryMetadata) error {
	if err := validateHTTPURL("issuer", metadata.Issuer, true); err != nil {
		return fmt.Errorf("%w: %v", ErrIssuerMismatch, err)
	}
	if !sameIssuerLocation(issuer, metadata.Issuer) {
		return fmt.Errorf("%w: configured %q, discovered %q", ErrIssuerMismatch, issuer, metadata.Issuer)
	}
	issuerURL, _ := url.Parse(metadata.Issuer)
	endpoints := []struct {
		name string
		raw  string
	}{
		{name: "authorization_endpoint", raw: metadata.AuthorizationEndpoint},
		{name: "token_endpoint", raw: metadata.TokenEndpoint},
		{name: "jwks_uri", raw: metadata.JWKSURI},
	}
	for _, endpointConfig := range endpoints {
		if err := validateHTTPURL(endpointConfig.name, endpointConfig.raw, false); err != nil {
			return fmt.Errorf("%w: %v", ErrUnexpectedEndpoint, err)
		}
		endpoint, _ := url.Parse(endpointConfig.raw)
		if issuerURL.Scheme == "https" && endpoint.Scheme != "https" {
			return fmt.Errorf("%w: %s must not downgrade an HTTPS issuer", ErrUnexpectedEndpoint, endpointConfig.name)
		}
	}
	if metadata.UserInfoEndpoint != "" {
		if err := validateHTTPURL("userinfo_endpoint", metadata.UserInfoEndpoint, false); err != nil {
			return fmt.Errorf("%w: %v", ErrUnexpectedEndpoint, err)
		}
		endpoint, _ := url.Parse(metadata.UserInfoEndpoint)
		if issuerURL.Scheme == "https" && endpoint.Scheme != "https" {
			return fmt.Errorf("%w: userinfo_endpoint must not downgrade an HTTPS issuer", ErrUnexpectedEndpoint)
		}
	}
	return nil
}

func validateHTTPURL(name, raw string, issuer bool) error {
	if raw == "" || raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, "\r\n\x00") {
		return fmt.Errorf("%s must be a non-empty absolute HTTP(S) URL", name)
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" {
		return fmt.Errorf("%s must be an absolute HTTP(S) URL without credentials or a fragment", name)
	}
	if issuer && (parsed.RawQuery != "" || parsed.ForceQuery) {
		return fmt.Errorf("%s must not contain a query or fragment", name)
	}
	return nil
}

func sameIssuerLocation(configured, discovered string) bool {
	left, leftErr := url.Parse(configured)
	right, rightErr := url.Parse(discovered)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Host, right.Host) &&
		strings.TrimRight(left.EscapedPath(), "/") == strings.TrimRight(right.EscapedPath(), "/")
}

func identityFromClaims(cfg Config, claims map[string]any) *Identity {
	identity := &Identity{Claims: claims}
	identity.Username = stringClaim(claims, cfg.UsernameClaim)
	identity.Email = stringClaim(claims, cfg.EmailClaim)
	identity.DisplayName = stringClaim(claims, "name")
	identity.Picture = stringClaim(claims, "picture")
	identity.EmailVerified, _ = claims["email_verified"].(bool)
	identity.Groups = stringSliceClaim(claims, cfg.GroupsClaim)
	return identity
}

func stringClaim(claims map[string]any, name string) string {
	value, _ := claims[name].(string)
	return strings.TrimSpace(value)
}

func stringSliceClaim(claims map[string]any, name string) []string {
	var out []string
	switch value := claims[name].(type) {
	case []any:
		for _, entry := range value {
			if text, ok := entry.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
	case []string:
		out = append(out, value...)
	case string:
		for _, entry := range strings.Fields(value) {
			out = append(out, entry)
		}
	}
	sort.Strings(out)
	return out
}

func clientWithCA(base *http.Client, pemText string) (*http.Client, error) {
	clone := *base
	previousRedirectPolicy := base.CheckRedirect
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("oidc HTTP redirect limit exceeded")
		}
		if err := validateHTTPURL("redirect target", req.URL.String(), false); err != nil {
			return err
		}
		if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return errors.New("oidc HTTP redirect attempted to downgrade HTTPS")
		}
		if len(via) > 0 && !sameHTTPOrigin(via[len(via)-1].URL, req.URL) && requestMayCarryCredentials(via[0]) {
			return errors.New("oidc HTTP redirect attempted to forward credentials across origins")
		}
		if previousRedirectPolicy != nil {
			return previousRedirectPolicy(req, via)
		}
		return nil
	}
	if strings.TrimSpace(pemText) == "" {
		return &clone, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM([]byte(pemText)) {
		return nil, fmt.Errorf("%w: ca_certificate_pem has no valid certificate", ErrInvalidConfig)
	}
	transport := &http.Transport{Proxy: nil}
	if existing, ok := base.Transport.(*http.Transport); ok {
		transport = existing.Clone()
	}
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = new(tls.Config)
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.RootCAs = pool
	clone.Transport = transport
	return &clone, nil
}

func sameHTTPOrigin(left, right *url.URL) bool {
	return left != nil && right != nil &&
		strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Host, right.Host)
}

func requestMayCarryCredentials(request *http.Request) bool {
	if request == nil {
		return false
	}
	return request.Method != http.MethodGet && request.Method != http.MethodHead ||
		request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != ""
}
