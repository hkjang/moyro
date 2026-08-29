// Package oidcauth implements the dynamically configurable OpenID Connect
// client used for Keycloak SSO. It intentionally keeps browser-flow state out
// of the manager; callers persist one-time state/nonce/PKCE values separately.
package oidcauth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

var (
	ErrDisabled           = errors.New("oidc is disabled")
	ErrInvalidConfig      = errors.New("invalid oidc configuration")
	ErrMissingIDToken     = errors.New("oidc token response did not contain an id_token")
	ErrNonceMismatch      = errors.New("oidc nonce mismatch")
	ErrEmailUnverified    = errors.New("oidc email is not verified")
	ErrUnexpectedEndpoint = errors.New("oidc discovery returned an endpoint outside the issuer origin")
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
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserInfoEndpoint      string `json:"userinfo_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
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
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}

	providerClient, err := clientWithCA(m.httpClient, normalized.CACertificatePEM)
	if err != nil {
		return nil, err
	}
	discoveryCtx := gooidc.ClientContext(ctx, providerClient)
	provider, err := gooidc.NewProvider(discoveryCtx, normalized.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	var metadata discoveryMetadata
	if err := provider.Claims(&metadata); err != nil {
		return nil, fmt.Errorf("oidc discovery claims: %w", err)
	}
	if err := validateDiscoveredEndpoints(normalized.IssuerURL, metadata); err != nil {
		return nil, err
	}

	oauthCfg := oauth2.Config{
		ClientID:     normalized.ClientID,
		ClientSecret: normalized.ClientSecret,
		RedirectURL:  normalized.RedirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       append([]string(nil), normalized.Scopes...),
	}
	next := &snapshot{
		config:   normalized,
		provider: provider,
		oauth2:   oauthCfg,
		verifier: provider.VerifierContext(discoveryCtx, &gooidc.Config{ClientID: normalized.ClientID}),
		client:   providerClient,
	}
	return &Prepared{owner: m, next: next}, nil
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

	claims := map[string]any{}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("oidc claims: %w", err)
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

func normalizeConfig(cfg Config) (Config, error) {
	cfg.IssuerURL = strings.TrimSuffix(strings.TrimSpace(cfg.IssuerURL), "/")
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	cfg.RedirectURL = strings.TrimSpace(cfg.RedirectURL)
	if cfg.IssuerURL == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.RedirectURL == "" {
		return Config{}, fmt.Errorf("%w: issuer_url, client_id, client_secret, and redirect_url are required", ErrInvalidConfig)
	}
	issuer, err := url.Parse(cfg.IssuerURL)
	if err != nil || issuer.Host == "" || (issuer.Scheme != "https" && issuer.Scheme != "http") {
		return Config{}, fmt.Errorf("%w: issuer_url must be an absolute HTTP(S) URL", ErrInvalidConfig)
	}
	redirect, err := url.Parse(cfg.RedirectURL)
	if err != nil || redirect.Host == "" || (redirect.Scheme != "https" && redirect.Scheme != "http") {
		return Config{}, fmt.Errorf("%w: redirect_url must be an absolute HTTP(S) URL", ErrInvalidConfig)
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

func validateDiscoveredEndpoints(issuer string, metadata discoveryMetadata) error {
	base, err := url.Parse(issuer)
	if err != nil {
		return err
	}
	for name, raw := range map[string]string{
		"authorization_endpoint": metadata.AuthorizationEndpoint,
		"token_endpoint":         metadata.TokenEndpoint,
		"jwks_uri":               metadata.JWKSURI,
	} {
		endpoint, err := url.Parse(raw)
		if err != nil || endpoint.Scheme != base.Scheme || !strings.EqualFold(endpoint.Host, base.Host) {
			return fmt.Errorf("%w: %s", ErrUnexpectedEndpoint, name)
		}
	}
	if metadata.UserInfoEndpoint != "" {
		endpoint, err := url.Parse(metadata.UserInfoEndpoint)
		if err != nil || endpoint.Scheme != base.Scheme || !strings.EqualFold(endpoint.Host, base.Host) {
			return fmt.Errorf("%w: userinfo_endpoint", ErrUnexpectedEndpoint)
		}
	}
	return nil
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
	if strings.TrimSpace(pemText) == "" {
		return base, nil
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
	clone := *base
	clone.Transport = transport
	return &clone, nil
}
