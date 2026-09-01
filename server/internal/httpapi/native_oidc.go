package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hkjang/moyro/server/internal/audit"
	"github.com/hkjang/moyro/server/internal/auth"
	"github.com/hkjang/moyro/server/internal/metrics"
	"github.com/hkjang/moyro/server/internal/oauth"
	"github.com/hkjang/moyro/server/internal/oidcauth"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/settings"
)

const (
	oidcSettingsSection = "oidc"
	oidcSettingsKey     = "keycloak"
	oidcSecretKey       = "keycloak-client-secret"

	oidcTransactionCookiePrefix  = "moyro_oidc_transaction_"
	oauthTransactionCookiePrefix = "moyro_oauth_transaction_"
	oidcCallbackPath             = "/api/moyro/v1/auth/oidc/callback"
	ssoHandoffCookiePrefix       = "moyro_sso_handoff_"
	ssoSessionExchangePath       = "/api/moyro/v1/auth/sso/session"
	maxReturnToLength            = 2048
)

type secretConfiguredView struct {
	Configured bool `json:"configured"`
}

type oidcProviderView struct {
	ID                       string                `json:"id,omitempty"`
	Kind                     string                `json:"kind"`
	Name                     string                `json:"name"`
	Enabled                  bool                  `json:"enabled"`
	IssuerURL                string                `json:"issuer_url"`
	ClientID                 string                `json:"client_id"`
	ClientSecret             string                `json:"client_secret,omitempty"`
	ClientSecretState        *secretConfiguredView `json:"client_secret_state,omitempty"`
	Scopes                   []string              `json:"scopes"`
	UsernameClaim            string                `json:"username_claim"`
	EmailClaim               string                `json:"email_claim"`
	CACertificatePEM         string                `json:"ca_certificate_pem,omitempty"`
	AllowSignup              bool                  `json:"allow_signup"`
	RequireVerifiedEmail     bool                  `json:"require_verified_email"`
	AllowInsecureBackchannel bool                  `json:"allow_insecure_backchannel"`
	RedirectURL              string                `json:"redirect_url,omitempty"`
	DiscoveryStatus          string                `json:"discovery_status,omitempty"`
	LastTestedAt             int64                 `json:"last_tested_at,omitempty"`
}

func defaultOIDCProvider() oidcProviderView {
	return oidcProviderView{
		ID: "keycloak", Kind: "keycloak", Name: "Keycloak",
		Scopes:        []string{"openid", "profile", "email"},
		UsernameClaim: "preferred_username", EmailClaim: "email",
		AllowSignup: true, RequireVerifiedEmail: true, DiscoveryStatus: "unknown",
	}
}

func (n *nativeServices) reloadOIDC(ctx context.Context, fallbackBaseURL string) error {
	var view oidcProviderView
	if err := n.loadJSON(ctx, oidcSettingsSection, oidcSettingsKey, &view); err != nil {
		return err
	}
	if !view.Enabled {
		n.oidc.Disable()
		return nil
	}
	secret, _, err := n.settings.RevealSecret(ctx, oidcSettingsSection, oidcSecretKey)
	if err != nil {
		return err
	}
	// Prefer the administrator-managed site origin. A blank site origin can
	// exist in data written before it became mandatory for enabled OIDC; retain
	// that record's already validated callback rather than rebinding to the
	// development-only localhost default during restart.
	view.RedirectURL = oidcRedirectURLForReload(view.RedirectURL, fallbackBaseURL)
	return n.oidc.Configure(ctx, view.oidcConfig(string(secret)))
}

func oidcRedirectURLForReload(storedRedirectURL, publicBaseURL string) string {
	if publicBaseURL = strings.TrimSpace(publicBaseURL); publicBaseURL != "" {
		return strings.TrimRight(publicBaseURL, "/") + oidcCallbackPath
	}
	return strings.TrimSpace(storedRedirectURL)
}

func (n *nativeServices) prepareStoredOIDC(ctx context.Context, baseURL string) (*oidcauth.Prepared, bool, error) {
	view := defaultOIDCProvider()
	if err := n.loadJSON(ctx, oidcSettingsSection, oidcSettingsKey, &view); err != nil {
		if errors.Is(err, settings.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !view.Enabled {
		return nil, false, nil
	}
	secret, _, err := n.settings.RevealSecret(ctx, oidcSettingsSection, oidcSecretKey)
	if err != nil {
		return nil, false, err
	}
	view.RedirectURL = strings.TrimRight(baseURL, "/") + oidcCallbackPath
	prepared, err := n.oidc.Prepare(ctx, view.oidcConfig(string(secret)))
	if err != nil {
		return nil, false, err
	}
	return prepared, true, nil
}

func (v oidcProviderView) oidcConfig(secret string) oidcauth.Config {
	return oidcauth.Config{
		Enabled: v.Enabled, DisplayName: v.Name, IssuerURL: v.IssuerURL,
		ClientID: v.ClientID, ClientSecret: secret, RedirectURL: v.RedirectURL,
		Scopes: v.Scopes, UsernameClaim: v.UsernameClaim, EmailClaim: v.EmailClaim,
		AllowSignup:              v.AllowSignup,
		RequireVerifiedEmail:     v.RequireVerifiedEmail,
		AllowInsecureBackchannel: v.AllowInsecureBackchannel,
		CACertificatePEM:         v.CACertificatePEM,
	}
}

func (h *handlers) listNativeOIDCProviders(w http.ResponseWriter, r *http.Request) {
	view, err := h.readOIDCProvider(r.Context())
	if errors.Is(err, settings.ErrNotFound) {
		writeJSON(w, http.StatusOK, []oidcProviderView{})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.oidc.read", err.Error())
		return
	}
	live := h.native != nil && h.native.oidc != nil && h.native.oidc.Enabled()
	view.DiscoveryStatus = runtimeOIDCDiscoveryStatus(view, live)
	view.RedirectURL = h.effectivePublicBaseURL(r) + oidcCallbackPath
	writeJSON(w, http.StatusOK, []oidcProviderView{view})
}

func runtimeOIDCDiscoveryStatus(view oidcProviderView, live bool) string {
	if view.Enabled && !live {
		// Stored settings can outlive a failed restart-time discovery. Report the
		// current runtime truth instead of showing the last successful probe as if
		// SSO were still available.
		return "error"
	}
	return view.DiscoveryStatus
}

func (h *handlers) readOIDCProvider(ctx context.Context) (oidcProviderView, error) {
	view := defaultOIDCProvider()
	if err := h.native.loadJSON(ctx, oidcSettingsSection, oidcSettingsKey, &view); err != nil {
		return oidcProviderView{}, err
	}
	view.ClientSecret = ""
	if _, _, err := h.native.settings.RevealSecret(ctx, oidcSettingsSection, oidcSecretKey); err == nil {
		view.ClientSecretState = &secretConfiguredView{Configured: true}
	} else if !errors.Is(err, settings.ErrNotFound) {
		return oidcProviderView{}, err
	}
	return view, nil
}

func (h *handlers) saveNativeOIDCProvider(w http.ResponseWriter, r *http.Request) {
	view := defaultOIDCProvider()
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&view); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.oidc.body", err.Error())
		return
	}
	defaults := defaultOIDCProvider()
	if view.ID == "" {
		view.ID = defaults.ID
	}
	if view.Kind == "" {
		view.Kind = defaults.Kind
	}
	if view.Name == "" {
		view.Name = defaults.Name
	}
	if len(view.Scopes) == 0 {
		view.Scopes = defaults.Scopes
	}
	if view.UsernameClaim == "" {
		view.UsernameClaim = defaults.UsernameClaim
	}
	if view.EmailClaim == "" {
		view.EmailClaim = defaults.EmailClaim
	}
	unlock := h.native.beginSettingsUpdate()
	defer unlock()

	origin := h.effectivePublicBaseURL(r)
	if view.Enabled {
		origin = strings.TrimSpace(h.native.currentSiteSettings().PublicBaseURL)
		if origin == "" {
			writeError(w, http.StatusBadRequest, "api.moyro.oidc.public_url", "set the public base URL in site settings before enabling Keycloak")
			return
		}
	}
	view.RedirectURL = origin + oidcCallbackPath
	view.DiscoveryStatus = "unknown"
	actor := userID(r)

	secret := view.ClientSecret
	if strings.TrimSpace(secret) == "" {
		var err error
		secret, err = h.native.revealOptionalSecret(r.Context(), oidcSettingsSection, oidcSecretKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "api.moyro.oidc.secret_read", err.Error())
			return
		}
	}
	var prepared *oidcauth.Prepared
	if view.Enabled {
		if secret == "" {
			writeError(w, http.StatusBadRequest, "api.moyro.oidc.secret", "client_secret is required before enabling Keycloak")
			return
		}
		var err error
		prepared, err = h.native.oidc.Prepare(r.Context(), view.oidcConfig(secret))
		if err != nil {
			writeError(w, http.StatusBadRequest, "api.moyro.oidc.discovery", err.Error())
			return
		}
		view.IssuerURL = prepared.IssuerURL()
		view.DiscoveryStatus = "ready"
		view.LastTestedAt = time.Now().UnixMilli()
	}
	view.ClientSecret = ""
	if _, err := h.native.settings.PutJSONAndOptionalSecret(
		r.Context(), oidcSettingsSection, oidcSettingsKey, view,
		oidcSecretKey, []byte(secret), actor,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.oidc.save", err.Error())
		return
	}
	if view.Enabled {
		if err := h.native.oidc.Activate(prepared); err != nil {
			writeError(w, http.StatusInternalServerError, "api.moyro.oidc.activate", err.Error())
			return
		}
	} else {
		h.native.oidc.Disable()
	}
	view.ClientSecretState = &secretConfiguredView{Configured: secret != ""}
	if h.audit != nil {
		h.audit.LogAsync(actor, "settings.oidc.update", "keycloak", map[string]any{"enabled": view.Enabled, "issuer": view.IssuerURL})
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *handlers) testNativeOIDCProvider(w http.ResponseWriter, r *http.Request) {
	view := defaultOIDCProvider()
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&view); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.oidc.body", err.Error())
		return
	}
	view.Enabled = true
	origin := h.effectivePublicBaseURL(r)
	view.RedirectURL = origin + oidcCallbackPath
	temporary := oidcauth.NewManager(nil)
	issuer, err := temporary.Probe(r.Context(), view.oidcConfig(""))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "issuer": issuer})
}

func (h *handlers) nativeOIDCLogin(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	result := "internal_error"
	defer func() { metrics.ObserveSSOStage("keycloak", "login", result, time.Since(started)) }()
	w.Header().Set("Cache-Control", "no-store")
	if h.native == nil || !h.native.oidc.Enabled() {
		result = "disabled"
		writeError(w, http.StatusNotFound, "api.moyro.oidc.disabled", "Keycloak login is not enabled")
		return
	}
	returnTo := sanitizeReturnTo(r.URL.Query().Get("return_to"))
	state, flow, err := h.native.oidcFlows.Create(r.Context(), returnTo)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.oidc.flow", err.Error())
		return
	}
	authURL, err := h.native.oidc.AuthCodeURL(state, flow.Nonce, flow.Verifier)
	if err != nil {
		// The row was already created. Consume it now so a transient manager
		// failure cannot leave a usable, unbound authorization transaction.
		_, _ = h.native.oidcFlows.Consume(r.Context(), state)
		writeError(w, http.StatusServiceUnavailable, "api.moyro.oidc.disabled", err.Error())
		return
	}
	result = "success"
	setOIDCTransactionCookie(w, state, flow.ExpiresAt, oidcCookieSecure(r, h.native.oidc))
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (h *handlers) nativeOIDCCallback(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	result := "internal_error"
	defer func() { metrics.ObserveSSOStage("keycloak", "callback", result, time.Since(started)) }()
	w.Header().Set("Cache-Control", "no-store")
	if h.native == nil || h.native.oidcFlows == nil || h.native.oidc == nil {
		result = "disabled"
		clearOIDCTransactionCookie(w, r.URL.Query().Get("state"), r.TLS != nil)
		h.nativeOIDCRedirectError(w, r, "provider_unavailable")
		return
	}
	flow, callbackErr := consumeOIDCCallbackTransaction(
		w, r, h.native.oidcFlows, oidcCookieSecure(r, h.native.oidc),
	)
	if callbackErr != "" {
		result = "invalid"
		h.nativeOIDCRedirectError(w, r, callbackErr)
		return
	}
	identity, err := h.native.oidc.Exchange(r.Context(), r.URL.Query().Get("code"), flow.Verifier, flow.Nonce)
	if err != nil {
		result = "exchange_error"
		h.logger.Warn("Keycloak exchange failed", "err", err)
		h.nativeOIDCRedirectError(w, r, "exchange_failed")
		return
	}
	publicCfg, ok := h.native.oidc.PublicConfig()
	if !ok {
		result = "disabled"
		h.nativeOIDCRedirectError(w, r, "provider_disabled")
		return
	}
	providerKey := keycloakProviderKey(publicCfg.IssuerURL)
	if !publicCfg.AllowSignup {
		canResolve, err := h.oidcCanResolveExisting(r.Context(), providerKey, identity)
		if err != nil || !canResolve {
			result = "resolve_error"
			h.nativeOIDCRedirectError(w, r, "signup_disabled")
			return
		}
	}
	info := &oauth.UserInfo{
		Subject: identity.Subject, Username: identity.Username, Email: identity.Email,
		EmailVerified: identity.EmailVerified, Name: identity.DisplayName, Picture: identity.Picture,
	}
	u, created, err := h.oauthIdent.ResolveOrCreateUser(r.Context(), providerKey, info)
	if err != nil {
		if errors.Is(err, oauth.ErrUnverifiedLink) {
			result = "resolve_error"
			h.nativeOIDCRedirectError(w, r, "unverified_email")
			return
		}
		result = "resolve_error"
		h.logger.Error("Keycloak identity resolution failed", "err", err)
		h.nativeOIDCRedirectError(w, r, "resolve_failed")
		return
	}
	if created {
		if err := h.bootstrapMembership(r.Context(), u.ID); err != nil {
			h.logger.Warn("Keycloak default membership failed", "user", u.ID, "err", err)
		}
		if h.audit != nil {
			h.audit.LogAsync(u.ID, audit.ActionUserRegister, u.Username, map[string]any{"oauth_provider": "keycloak", "email": u.Email})
		}
	}
	handoff, err := h.auth.CreateLoginHandoff(r.Context(), u.ID)
	if err != nil {
		result = "session_error"
		h.logger.Error("Keycloak login handoff creation failed", "user", u.ID, "err", err)
		h.nativeOIDCRedirectError(w, r, "session_failed")
		return
	}
	setSSOHandoffCookie(w, handoff.Code, handoff.BrowserBinding, handoff.ExpiresAt, oidcCookieSecure(r, h.native.oidc))
	if h.audit != nil {
		h.audit.LogAsync(u.ID, audit.ActionUserLogin, u.Username, map[string]any{"oauth_provider": "keycloak", "ip": h.clientIP(r)})
	}
	result = "success"
	destination := sanitizeReturnTo(flow.ReturnTo)
	if destination == "" {
		destination = "/"
	}
	http.Redirect(w, r, destination+"#sso_code="+url.QueryEscape(handoff.Code), http.StatusFound)
}

type ssoSessionExchangeRequest struct {
	Code string `json:"code"`
}

// nativeSSOSessionExchange completes a browser SSO redirect in one request.
// The opaque code and independent HttpOnly browser binding must both match the
// same live database row before a local session can be minted.
func (h *handlers) nativeSSOSessionExchange(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	result := "internal_error"
	defer func() { metrics.ObserveSSOStage("browser", "exchange", result, time.Since(started)) }()
	w.Header().Set("Cache-Control", "no-store")
	if !h.validateBrowserOrigin(r) {
		result = "invalid"
		writeError(w, http.StatusForbidden, "api.context.csrf.app_error", "browser request origin is invalid")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	decoder.DisallowUnknownFields()
	var req ssoSessionExchangeRequest
	if err := decoder.Decode(&req); err != nil {
		result = "invalid"
		writeError(w, http.StatusBadRequest, "api.moyro.sso.invalid_body", err.Error())
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		result = "invalid"
		writeError(w, http.StatusBadRequest, "api.moyro.sso.invalid_body", "request body must contain one JSON object")
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	if req.Code == "" {
		result = "invalid"
		writeError(w, http.StatusBadRequest, "api.moyro.sso.missing_code", "code is required")
		return
	}
	cookie, err := r.Cookie(ssoHandoffCookieName(req.Code))
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		result = "invalid"
		writeError(w, http.StatusUnauthorized, "api.moyro.sso.invalid_code", "SSO login code is invalid or expired")
		return
	}

	u, token, err := h.auth.ExchangeLoginHandoff(r.Context(), req.Code, cookie.Value)
	if errors.Is(err, auth.ErrInvalidLoginHandoff) {
		result = "invalid"
		clearSSOHandoffCookie(w, req.Code, h.oauthSecureCookies(r))
		writeError(w, http.StatusUnauthorized, "api.moyro.sso.invalid_code", "SSO login code is invalid or expired")
		return
	}
	if err != nil {
		result = "session_error"
		if h.logger != nil {
			h.logger.Error("SSO login code exchange failed", "err", err)
		}
		writeError(w, http.StatusInternalServerError, "api.moyro.sso.exchange", "could not create the SSO session")
		return
	}
	// Keep the path-restricted binding cookie until its five-minute expiry so
	// the browser can repeat this exact request if the success response is lost.
	// The exchange service returns the same session during its shorter retry
	// window. Only the HttpOnly session cookie receives the reusable credential.
	h.setBrowserSessionCookie(w, r, token)
	result = "success"
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

func (h *handlers) nativeOIDCRedirectError(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/login#oauth_error="+url.QueryEscape(code), http.StatusFound)
}

func (h *handlers) oidcCanResolveExisting(ctx context.Context, provider string, identity *oidcauth.Identity) (bool, error) {
	normalizedEmail := oauth.NormalizeEmail(identity.Email)
	var exists bool
	err := h.auth.DB().Pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM user_identities WHERE provider=$1 AND subject=$2)
		    OR ($3<>'' AND $4 AND EXISTS(SELECT 1 FROM users WHERE LOWER(BTRIM(email))=$3 AND delete_at=0))
	`, provider, identity.Subject, normalizedEmail, identity.EmailVerified).Scan(&exists)
	return exists, err
}

func keycloakProviderKey(issuer string) string {
	digest := sha256.Sum256([]byte(strings.TrimRight(strings.ToLower(issuer), "/")))
	return "keycloak:" + hex.EncodeToString(digest[:8])
}

func sanitizeReturnTo(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxReturnToLength || !strings.HasPrefix(value, "/") ||
		strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\#\r\n\x00") {
		return ""
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil || parsed.Fragment != "" ||
		parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") {
		return ""
	}
	// Browsers and intermediaries may percent-decode a Location more than
	// once. Reject any encoded form which could become a protocol-relative
	// URL or a Windows-style network path after repeated decoding.
	decodedPath := parsed.Path
	for i := 0; i < 4; i++ {
		if strings.HasPrefix(decodedPath, "//") || strings.Contains(decodedPath, "\\") || containsURLControl(decodedPath) {
			return ""
		}
		next, err := url.PathUnescape(decodedPath)
		if err != nil {
			return ""
		}
		if next == decodedPath {
			break
		}
		decodedPath = next
	}
	if strings.HasPrefix(decodedPath, "//") || strings.Contains(decodedPath, "\\") || containsURLControl(decodedPath) {
		return ""
	}
	return value
}

func containsURLControl(value string) bool {
	for _, char := range value {
		if char <= 0x1f || char == 0x7f {
			return true
		}
	}
	return false
}

// externalOrigin deliberately handles only a direct request and ignores
// Forwarded and X-Forwarded-* headers. The handlers.externalOrigin wrapper may
// use those headers after the immediate peer matches the administrator's
// trusted-proxy CIDR allowlist. A same-host browser Origin can preserve the
// public HTTPS scheme here without accepting an arbitrary forwarded host.
func externalOrigin(r *http.Request) (string, error) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if host == "" || strings.TrimSpace(host) != host || strings.ContainsAny(host, "/\\@?#,\r\n\x00") {
		return "", errors.New("request host is invalid")
	}
	parsed, err := url.Parse(scheme + "://" + host)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("request host is invalid")
	}
	// A browser-generated Origin is not a proxy assertion. When its authority
	// exactly matches the direct Host it can safely preserve the public HTTPS
	// scheme across TLS termination without accepting an arbitrary forwarded
	// host. Non-browser clients simply use the direct connection scheme.
	if rawOrigin := strings.TrimSpace(r.Header.Get("Origin")); rawOrigin != "" {
		origin, originErr := url.Parse(rawOrigin)
		if originErr == nil && (origin.Scheme == "http" || origin.Scheme == "https") &&
			strings.EqualFold(origin.Host, parsed.Host) && origin.User == nil &&
			origin.Path == "" && origin.RawQuery == "" && origin.Fragment == "" {
			scheme = origin.Scheme
		}
	}
	return (&url.URL{Scheme: scheme, Host: parsed.Host}).String(), nil
}

type oidcFlowConsumer interface {
	Consume(context.Context, string) (oidcauth.Flow, error)
}

// consumeOIDCCallbackTransaction performs the state/cookie checks before any
// provider result is acted on. It always attempts to consume state and always
// deletes the browser cookie, including provider-error callbacks. This makes
// every callback one-shot and prevents login CSRF via an attacker-created
// authorization transaction.
func consumeOIDCCallbackTransaction(w http.ResponseWriter, r *http.Request, store oidcFlowConsumer, secure bool) (oidcauth.Flow, string) {
	state := r.URL.Query().Get("state")
	cookie, cookieErr := r.Cookie(oidcTransactionCookieName(state))
	clearOIDCTransactionCookie(w, state, secure)

	// Consume even when the cookie is absent or mismatched. An attacker who
	// learns a state must not be able to probe it repeatedly or reuse it after
	// a provider-error callback.
	flow, flowErr := store.Consume(r.Context(), state)
	cookieMatches := cookieErr == nil && state != "" &&
		subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) == 1
	if flowErr != nil || !cookieMatches {
		return oidcauth.Flow{}, "state_mismatch"
	}
	if providerErr := sanitizeProviderError(r.URL.Query().Get("error")); providerErr != "" {
		return oidcauth.Flow{}, "provider_" + providerErr
	}
	return flow, ""
}

func sanitizeProviderError(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) > 64 {
		return "error"
	}
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.') {
			return "error"
		}
	}
	return value
}

func setOIDCTransactionCookie(w http.ResponseWriter, state string, expiresAt int64, secure bool) {
	expires := time.UnixMilli(expiresAt)
	maxAge := int(time.Until(expires).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name: oidcTransactionCookieName(state), Value: state, Path: oidcCallbackPath,
		Expires: expires, MaxAge: maxAge, HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearOIDCTransactionCookie(w http.ResponseWriter, state string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: oidcTransactionCookieName(state), Value: "", Path: oidcCallbackPath,
		Expires: time.Unix(1, 0), MaxAge: -1, HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func oidcTransactionCookieName(state string) string {
	digest := sha256.Sum256([]byte(state))
	return oidcTransactionCookiePrefix + hex.EncodeToString(digest[:8])
}

func oauthTransactionCookieName(state string) string {
	digest := sha256.Sum256([]byte(state))
	return oauthTransactionCookiePrefix + hex.EncodeToString(digest[:8])
}

func setSSOHandoffCookie(w http.ResponseWriter, code, binding string, expiresAt int64, secure bool) {
	expires := time.UnixMilli(expiresAt)
	maxAge := int(time.Until(expires).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name: ssoHandoffCookieName(code), Value: binding, Path: ssoSessionExchangePath,
		Expires: expires, MaxAge: maxAge, HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearSSOHandoffCookie(w http.ResponseWriter, code string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: ssoHandoffCookieName(code), Value: "", Path: ssoSessionExchangePath,
		Expires: time.Unix(1, 0), MaxAge: -1, HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func ssoHandoffCookieName(code string) string {
	digest := sha256.Sum256([]byte(code))
	return ssoHandoffCookiePrefix + hex.EncodeToString(digest[:8])
}

func oidcCookieSecure(r *http.Request, manager *oidcauth.Manager) bool {
	if r.TLS != nil {
		return true
	}
	if manager != nil {
		if cfg, ok := manager.PublicConfig(); ok {
			callback, err := url.Parse(cfg.RedirectURL)
			return err == nil && callback.Scheme == "https"
		}
	}
	return false
}

// Keep a direct reference so the compiler catches permission renames used by
// the route registration in native_routes.go.
var _ = rbac.PermissionManageOIDC
