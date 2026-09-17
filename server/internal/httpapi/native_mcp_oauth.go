package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/hkjang/moyro/server/internal/apikeys"
	"github.com/hkjang/moyro/server/internal/oidcauth"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/settings"
	"github.com/jackc/pgx/v5"
)

// MCP over SSO — no personal key, a Keycloak access token instead.
//
// The MCP authorization specification (2025-06-18 and later) is OAuth 2.1:
// /mcp is a *resource server* that publishes where its authorization server
// is (RFC 9728), answers 401 with a pointer to that document, and checks the
// tokens that come back. Nothing about signing people in or minting tokens
// happens here — Keycloak, already configured for the web sign-in, does that.
//
// The personal MCP key stays exactly as it is. An SSO token is a second door
// into the same room: it authenticates an account that already exists (the
// web sign-in linked it), carries the scopes the administrator chose, and
// passes the same gate a scoped key passes. It is only honoured on /mcp.

const (
	mcpOAuthMetadataPath = "/.well-known/oauth-protected-resource"
	mcpOAuthRealm        = "moyro"
	// mcpOAuthCredentialPrefix marks the credential id of an SSO principal.
	// It is non-secret audit metadata and what the deferred approval executor
	// recognises as "re-check the SSO policy, not a key row".
	mcpOAuthCredentialPrefix = "oauth:"
)

// mcpOAuthScopeVocabulary is what an SSO token may be granted: the MCP
// permissions a personal key can hold. Administrator-only grants (manage_*)
// and key self-service never ride on a token.
var mcpOAuthScopeVocabulary = []string{rbac.PermissionMCPRead, rbac.PermissionMCPWrite, rbac.PermissionRequestApproval, rbac.PermissionReviewApproval}

type mcpOAuthSettingsView struct {
	Enabled bool `json:"enabled"`
	// Resource is the identifier this server claims (RFC 8707): the public
	// HTTPS address clients actually connect to plus /mcp. Empty derives it
	// from the site's public base URL.
	Resource string `json:"resource"`
	// Audience lists client ids accepted in aud or azp without an Audience
	// mapper; Keycloak 26 puts the client id in azp and only "account" in aud.
	Audience []string `json:"audience"`
	// Scopes are what an SSO subject may do. Tokens do not carry this
	// vocabulary unless Keycloak is taught it, so the administrator states
	// the ceiling here; a token that does carry it is intersected with it.
	Scopes []string `json:"scopes"`
}

func defaultMCPOAuthSettings() mcpOAuthSettingsView {
	return mcpOAuthSettingsView{Audience: []string{}, Scopes: []string{"mcp_read"}}
}

// mcpOAuthStatusView is the read-only, computed half the admin screen shows
// next to the editable settings: what the metadata document will say and,
// when SSO tokens are not being accepted although enabled, why.
type mcpOAuthStatusView struct {
	Active         bool   `json:"active"`
	Reason         string `json:"reason,omitempty"`
	Resource       string `json:"resource,omitempty"`
	MetadataURL    string `json:"metadata_url,omitempty"`
	IssuerURL      string `json:"issuer_url,omitempty"`
	ClientID       string `json:"client_id,omitempty"`
	OIDCConfigured bool   `json:"oidc_configured"`
}

// mcpOAuthRuntime is the fully resolved policy a request is checked against.
type mcpOAuthRuntime struct {
	Resource      string
	MetadataURL   string
	IssuerURL     string
	ClientID      string
	UsernameClaim string
	Audience      []string
	Scopes        []string
}

func validateMCPOAuthSettings(value *mcpOAuthSettingsView, oidcConfigured bool, publicBaseURL string) error {
	value.Resource = strings.TrimSpace(value.Resource)
	if value.Resource != "" {
		resource, err := canonicalMCPResource(value.Resource)
		if err != nil {
			return err
		}
		value.Resource = resource
	}
	if len(value.Audience) > 32 {
		return errors.New("at most 32 allowed audiences can be configured")
	}
	value.Audience = canonicalNativeStrings(value.Audience)
	for _, audience := range value.Audience {
		if len(audience) > 256 || strings.ContainsAny(audience, " \t\r\n\x00\"") {
			return errors.New("allowed audiences must be client ids or URIs without whitespace or quotes")
		}
	}
	value.Scopes = canonicalNativeStrings(value.Scopes)
	for _, scope := range value.Scopes {
		if !slices.Contains(mcpOAuthScopeVocabulary, scope) {
			return errors.New("unsupported MCP SSO scope: " + scope)
		}
	}
	if !value.Enabled {
		if len(value.Scopes) == 0 {
			value.Scopes = defaultMCPOAuthSettings().Scopes
		}
		return nil
	}
	if len(value.Scopes) == 0 {
		return errors.New("at least one MCP SSO scope is required")
	}
	if !oidcConfigured {
		return errors.New("MCP SSO requires Keycloak SSO to be enabled and reachable first")
	}
	if value.Resource == "" && strings.TrimSpace(publicBaseURL) == "" {
		return errors.New("MCP SSO needs a resource identifier: set it here or set the site's public base URL")
	}
	return nil
}

// canonicalMCPResource accepts an absolute http(s) URL ending in the MCP path
// and normalises it; a bare origin gets /mcp appended. Query, fragment, and
// credentials are refused — clients compare this string byte for byte.
func canonicalMCPResource(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", errors.New("MCP resource must be an absolute http(s) URL without query, fragment, or credentials")
	}
	path := strings.TrimRight(parsed.Path, "/")
	if path == "" {
		path = "/mcp"
	}
	if !strings.HasSuffix(path, "/mcp") {
		return "", errors.New("MCP resource must end with the MCP endpoint path /mcp")
	}
	parsed.Path, parsed.RawPath = path, ""
	return parsed.String(), nil
}

// mcpOAuthMetadataURL is where a refused client is sent to learn the above
// (RFC 9728 §3: the well-known segment is inserted before the resource path).
func mcpOAuthMetadataURL(resource string) string {
	parsed, err := url.Parse(resource)
	if err != nil {
		return ""
	}
	path := parsed.Path
	parsed.Path, parsed.RawPath = mcpOAuthMetadataPath+path, ""
	return parsed.String()
}

// mcpOAuthStatus resolves the stored settings against the live OIDC provider
// and site origin. A nil runtime with a reason is "enabled but not accepting
// tokens"; the reason is what the admin screen and the log show.
func (n *nativeServices) mcpOAuthStatus(value mcpSettingsView) (*mcpOAuthRuntime, mcpOAuthStatusView) {
	status := mcpOAuthStatusView{}
	public, oidcLive := n.oidc.PublicConfig()
	status.OIDCConfigured = oidcLive
	if oidcLive {
		status.IssuerURL, status.ClientID = public.IssuerURL, public.ClientID
	}
	resource := value.OAuth.Resource
	if resource == "" {
		if base := strings.TrimSpace(n.currentSiteSettings().PublicBaseURL); base != "" {
			resource = strings.TrimRight(base, "/") + "/mcp"
		}
	}
	if resource != "" {
		status.Resource, status.MetadataURL = resource, mcpOAuthMetadataURL(resource)
	}
	switch {
	case !value.OAuth.Enabled:
		status.Reason = "disabled"
	case !value.Enabled:
		status.Reason = "MCP endpoint is disabled"
	case !oidcLive:
		status.Reason = "Keycloak SSO is not enabled or its discovery failed"
	case resource == "":
		status.Reason = "no resource identifier: set mcp.oauth.resource or the site's public base URL"
	default:
		status.Active = true
		return &mcpOAuthRuntime{
			Resource: resource, MetadataURL: status.MetadataURL,
			IssuerURL: public.IssuerURL, ClientID: public.ClientID, UsernameClaim: public.UsernameClaim,
			Audience: append([]string(nil), value.OAuth.Audience...),
			Scopes:   append([]string(nil), value.OAuth.Scopes...),
		}, status
	}
	return nil, status
}

// resolveMCPOAuthRuntime is the per-request read; it mirrors nativeMCPGate,
// which reads the same section so a saved change applies to the next call.
func (h *handlers) resolveMCPOAuthRuntime(ctx context.Context) (*mcpOAuthRuntime, mcpOAuthStatusView, error) {
	if h.native == nil || h.native.oidc == nil {
		return nil, mcpOAuthStatusView{Reason: "disabled"}, nil
	}
	value := defaultMCPSettings()
	if err := h.native.loadJSON(ctx, "mcp", nativeSettingsKey, &value); err != nil && !errors.Is(err, settings.ErrNotFound) {
		return nil, mcpOAuthStatusView{}, err
	}
	runtime, status := h.native.mcpOAuthStatus(value)
	return runtime, status, nil
}

// loadMCPOAuthRuntime is resolveMCPOAuthRuntime for the MCP path and the
// metadata document: "enabled but not accepting tokens" is logged there,
// where a client is actually being turned away, with the reason.
func (h *handlers) loadMCPOAuthRuntime(ctx context.Context) (*mcpOAuthRuntime, error) {
	runtime, status, err := h.resolveMCPOAuthRuntime(ctx)
	if err != nil {
		return nil, err
	}
	if runtime == nil && status.Reason != "disabled" && h.logger != nil {
		h.logger.Warn("MCP SSO is enabled but not accepting tokens", "reason", status.Reason)
	}
	return runtime, nil
}

// protectedResourceMetadata is RFC 9728: the document a refused MCP client
// reads to find the authorization server. Public by design — it says where to
// sign in, not who is signed in — and bare JSON rather than the API error
// envelope, because the reader is an OAuth client library.
func (h *handlers) protectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	runtime, err := h.loadMCPOAuthRuntime(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "api.moyro.mcp.settings", "MCP settings are unavailable")
		return
	}
	if runtime == nil {
		writeError(w, http.StatusNotFound, "api.moyro.mcp.oauth_disabled", "this server's MCP endpoint does not accept SSO tokens; use a personal MCP key")
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 runtime.Resource,
		"authorization_servers":    []string{runtime.IssuerURL},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         runtime.Scopes,
		"resource_name":            h.native.currentSiteSettings().SiteName + " MCP",
	})
}

// mcpAuthChain is the order /mcp authenticates in: bearer-header only, the
// SSO challenge on any 401, personal keys, SSO tokens, account reload, then
// the policy gate. Tests mount the same chain so what they prove is this.
func (h *handlers) mcpAuthChain() []func(http.Handler) http.Handler {
	return []func(http.Handler) http.Handler{
		nativeBearerOnly, h.nativeMCPChallenge, h.nativeAPIKeyMiddleware, h.nativeMCPOAuth, h.requireAuth, h.nativeMCPGate,
	}
}

// mcpOAuthRuntimeKey carries the runtime the challenge middleware resolved so
// the token middleware behind it does not read the settings row again.
type mcpOAuthRuntimeKey struct{}

func mcpOAuthRuntimeFromContext(ctx context.Context) (*mcpOAuthRuntime, bool) {
	runtime, ok := ctx.Value(mcpOAuthRuntimeKey{}).(*mcpOAuthRuntime)
	return runtime, ok
}

// mcpChallengeWriter adds the WWW-Authenticate challenge to any 401 written
// on the MCP path — whichever middleware refuses. Without it a refusal is a
// dead end; with it the client reads resource_metadata and starts OAuth.
type mcpChallengeWriter struct {
	http.ResponseWriter
	challenge string
}

func (w *mcpChallengeWriter) WriteHeader(status int) {
	if status == http.StatusUnauthorized && w.challenge != "" {
		w.Header().Set("WWW-Authenticate", w.challenge)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *mcpChallengeWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *mcpChallengeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// nativeMCPChallenge is MCP-path only: a REST 401 must never carry this
// header or browsers and other clients would be sent to Keycloak.
func (h *handlers) nativeMCPChallenge(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtime, err := h.loadMCPOAuthRuntime(r.Context())
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "api.moyro.mcp.settings", "MCP settings are unavailable")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), mcpOAuthRuntimeKey{}, runtime))
		if runtime == nil {
			next.ServeHTTP(w, r)
			return
		}
		challenge := fmt.Sprintf(`Bearer realm=%q, resource_metadata=%q`, mcpOAuthRealm, runtime.MetadataURL)
		if extractBearer(r) != "" {
			challenge += `, error="invalid_token"`
		}
		next.ServeHTTP(&mcpChallengeWriter{ResponseWriter: w, challenge: challenge}, r)
	})
}

// nativeMCPOAuth sits between the API-key middleware and requireAuth. The
// same Authorization: Bearer header carries either credential: a key prefix
// went to the key middleware already; a JWT shape is tried here when SSO is
// on; anything else falls through to the exact refusal a key-only install
// gives today, so a disabled install says nothing new.
func (h *handlers) nativeMCPOAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearer(r)
		if _, alreadyKeyed := PrincipalFromContext(r.Context()); alreadyKeyed || token == "" ||
			strings.HasPrefix(token, apikeys.SecretPrefix) || !oidcauth.LooksLikeJWT(token) {
			next.ServeHTTP(w, r)
			return
		}
		runtime, resolved := mcpOAuthRuntimeFromContext(r.Context())
		if !resolved {
			var err error
			if runtime, err = h.loadMCPOAuthRuntime(r.Context()); err != nil {
				writeError(w, http.StatusServiceUnavailable, "api.moyro.mcp.settings", "MCP settings are unavailable")
				return
			}
		}
		if runtime == nil {
			next.ServeHTTP(w, r)
			return
		}
		principal, refusal, err := h.mcpOAuthPrincipal(r.Context(), runtime, token)
		if err != nil {
			h.logger.Error("MCP SSO account lookup failed", "err", err)
			writeError(w, http.StatusServiceUnavailable, "api.moyro.mcp.oauth_lookup", "SSO account lookup is unavailable")
			return
		}
		if refusal != "" {
			writeError(w, http.StatusUnauthorized, "api.moyro.mcp.oauth_rejected", refusal)
			return
		}
		next.ServeHTTP(w, r.WithContext(setPrincipalOnContext(r.Context(), principal)))
	})
}

// mcpOAuthPrincipal turns a bearer access token into a restricted principal,
// or says exactly why it will not. The refusal text is for the operator who
// reads it in the client; the precise verification failure (which of
// signature, issuer, expiry, nbf) goes to the server log here, because the
// client message deliberately does not distinguish them.
func (h *handlers) mcpOAuthPrincipal(ctx context.Context, runtime *mcpOAuthRuntime, raw string) (rbac.Principal, string, error) {
	token, err := h.native.oidc.VerifyAccessToken(ctx, raw)
	if err != nil {
		h.logger.Warn("MCP SSO token rejected", "issuer", runtime.IssuerURL, "err", err)
		switch {
		case errors.Is(err, oidcauth.ErrAccessTokenIsIDToken):
			return rbac.Principal{}, "the bearer is an ID token; MCP needs the access token Keycloak issued for this server", nil
		case errors.Is(err, oidcauth.ErrAccessTokenBound):
			return rbac.Principal{}, "the access token is bound to a proof-of-possession key (cnf) this server cannot verify", nil
		case errors.Is(err, oidcauth.ErrAccessTokenNoSubject):
			return rbac.Principal{}, "the access token has no subject", nil
		case errors.Is(err, oidcauth.ErrDisabled):
			return rbac.Principal{}, "Keycloak SSO is not enabled on this server", nil
		}
		return rbac.Principal{}, "the SSO access token is not valid (signature, issuer, expiry, or not-before); sign in again from the MCP client", nil
	}
	// Whom the token was minted for. Measured against a real Keycloak 26: an
	// access token issued to a client carries that client in azp and only
	// "account" in aud — the client id is not in aud, whatever an ID token
	// does. So the binding is "aud names this resource, or aud/azp names a
	// client the administrator listed". Either means the token is for this
	// deployment rather than passed through from another application in the
	// realm, which is what RFC 8707 guards against.
	bound := append(slices.Clone(token.Audience), token.AuthorizedParty)
	accepted := slices.ContainsFunc(bound, func(value string) bool {
		return value != "" && (value == runtime.Resource || slices.Contains(runtime.Audience, value))
	})
	if !accepted {
		h.logger.Warn("MCP SSO token audience refused", "aud", token.Audience, "azp", token.AuthorizedParty, "resource", runtime.Resource)
		return rbac.Principal{}, fmt.Sprintf(
			"the SSO token was not issued for this server (aud=%v, azp=%q): add %q to the MCP SSO allowed audiences, or give the Keycloak client an Audience mapper with %q",
			token.Audience, token.AuthorizedParty, token.AuthorizedParty, runtime.Resource), nil
	}
	// The same link the web sign-in wrote, without the provisioning half. A
	// token names somebody only if that person already signed in to the web
	// with this issuer; nothing is created, linked by email, or reactivated.
	userID, err := h.mcpOAuthAccount(ctx, runtime.IssuerURL, token.Subject)
	if err != nil {
		return rbac.Principal{}, "", err
	}
	if userID == "" {
		h.logger.Warn("MCP SSO token for an unknown or inactive account", "subject", token.Subject)
		return rbac.Principal{}, "this SSO account is not registered here or is inactive; sign in to the web once first", nil
	}
	// Scopes come from the administrator, not the token — unless the token
	// carries this app's own vocabulary, in which case only the intersection
	// is granted. Roles in the token never widen anything: RBAC intersects a
	// restricted principal with the account's current role assignments.
	granted := runtime.Scopes
	if slices.ContainsFunc(token.Scopes, func(scope string) bool { return slices.Contains(mcpOAuthScopeVocabulary, scope) }) {
		granted = slices.DeleteFunc(slices.Clone(granted), func(scope string) bool { return !slices.Contains(token.Scopes, scope) })
	}
	principal := rbac.Principal{
		UserID: userID, CredentialID: mcpOAuthCredentialPrefix + token.Subject, Restricted: true,
		GrantedPermissions: make(map[string]struct{}, len(granted)),
		AllowedTeamIDs:     map[string]struct{}{}, AllowedChannelIDs: map[string]struct{}{},
	}
	for _, scope := range granted {
		principal.GrantedPermissions[scope] = struct{}{}
	}
	return principal, "", nil
}

// mcpOAuthAccount finds the active account the web sign-in linked to this
// issuer's subject. Empty means "nobody": unknown subject, or the account is
// deactivated — a suspended person must not come back to life through MCP.
func (h *handlers) mcpOAuthAccount(ctx context.Context, issuer, subject string) (string, error) {
	var userID string
	err := h.auth.DB().Pool.QueryRow(ctx, `
		SELECT u.id FROM user_identities i JOIN users u ON u.id=i.user_id
		WHERE i.provider=$1 AND i.subject=$2 AND u.delete_at=0
	`, keycloakProviderKey(issuer), subject).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return userID, err
}

// mcpOAuthApprovedAllowed is the deferred-execution counterpart of
// ResolveCurrent for SSO principals. The token that submitted the request is
// gone (short-lived, never stored), so what is re-checked at execution time is
// the door itself: MCP and SSO still on, the administrator's scopes still
// covering the action, and RBAC for the requester's current roles.
func (n *nativeServices) mcpOAuthApprovedAllowed(ctx context.Context, requesterID, permission string, scope rbac.Scope) (bool, error) {
	value := defaultMCPSettings()
	if err := n.loadJSON(ctx, "mcp", nativeSettingsKey, &value); err != nil && !errors.Is(err, settings.ErrNotFound) {
		return false, err
	}
	runtime, _ := n.mcpOAuthStatus(value)
	if runtime == nil {
		return false, nil
	}
	principal := rbac.Principal{
		UserID: requesterID, Restricted: true,
		GrantedPermissions: make(map[string]struct{}, len(runtime.Scopes)),
		AllowedTeamIDs:     map[string]struct{}{}, AllowedChannelIDs: map[string]struct{}{},
	}
	for _, granted := range runtime.Scopes {
		principal.GrantedPermissions[granted] = struct{}{}
	}
	for _, need := range append(slices.Clone(value.RequiredScopes), permission) {
		if _, granted := principal.GrantedPermissions[need]; !granted {
			return false, nil
		}
		allowed, err := n.rbac.Allowed(ctx, principal, need, scope)
		if err != nil || !allowed {
			return allowed, err
		}
	}
	return true, nil
}
