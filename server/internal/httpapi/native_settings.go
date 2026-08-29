package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/buildinfo"
	"github.com/hkjang/moyro/server/internal/oidcauth"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/settings"
	"github.com/hkjang/moyro/server/internal/webhooks"
)

const nativeSettingsKey = "config"

func nativeSettingsPermission(section string) string {
	if section == "key-policy" {
		return rbac.PermissionManageKeyPermissions
	}
	return rbac.PermissionManageSettings
}

// nativeRequireSettingsSection keeps the generic settings URL compact while
// enforcing the permission of the concrete section. In particular, holding
// manage_settings must not implicitly grant authority to widen API-key
// scopes, and holding manage_key_permissions must be sufficient to maintain
// the key policy without an unrelated site-settings grant.
func (h *handlers) nativeRequireSettingsSection(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.native == nil || h.native.rbac == nil {
			writeError(w, http.StatusServiceUnavailable, "api.moyro.disabled", "moyro management services are unavailable")
			return
		}
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "authentication required")
			return
		}
		permission := nativeSettingsPermission(chi.URLParam(r, "section"))
		allowed, err := h.native.rbac.Allowed(r.Context(), principal, permission, rbac.Scope{})
		if err != nil || !allowed {
			writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "missing permission: "+permission)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type siteSettingsView struct {
	SiteName             string   `json:"site_name"`
	PublicBaseURL        string   `json:"public_base_url"`
	AllowedOutgoingHosts []string `json:"allowed_outgoing_hosts"`
	LocalSignupEnabled   bool     `json:"local_signup_enabled"`
}

func defaultSiteSettings() siteSettingsView {
	return siteSettingsView{SiteName: "moyro", AllowedOutgoingHosts: []string{}}
}

func (n *nativeServices) currentSiteSettings() siteSettingsView {
	if n != nil {
		if current := n.site.Load(); current != nil {
			copy := *current
			copy.AllowedOutgoingHosts = append([]string(nil), current.AllowedOutgoingHosts...)
			return copy
		}
	}
	return defaultSiteSettings()
}

func (n *nativeServices) reloadSite(ctx context.Context, dispatcher *webhooks.Dispatcher) error {
	value := defaultSiteSettings()
	err := n.loadJSON(ctx, "site", nativeSettingsKey, &value)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return err
	}
	if err := validateSiteSettings(&value); err != nil {
		return err
	}
	n.applySite(value, dispatcher)
	return nil
}

func (n *nativeServices) applySite(value siteSettingsView, dispatcher *webhooks.Dispatcher) {
	copy := value
	copy.AllowedOutgoingHosts = append([]string(nil), value.AllowedOutgoingHosts...)
	n.site.Store(&copy)
	if dispatcher != nil {
		dispatcher.ConfigureAllowedHosts(copy.AllowedOutgoingHosts)
	}
}

type keyPolicyView struct {
	Enabled               bool     `json:"enabled"`
	AllowedScopes         []string `json:"allowed_scopes"`
	DefaultScopes         []string `json:"default_scopes"`
	DefaultTTLDays        int      `json:"default_ttl_days"`
	MaxTTLDays            int      `json:"max_ttl_days"`
	RotationDays          int      `json:"rotation_days"`
	RotationGraceHours    int      `json:"rotation_grace_hours"`
	AllowPersonalKeys     bool     `json:"allow_personal_keys"`
	AllowScopeSelfService bool     `json:"allow_scope_self_service"`
}

func defaultKeyPolicy() keyPolicyView {
	return keyPolicyView{
		Enabled:       true,
		AllowedScopes: []string{"manage_own_api_keys", "use_ai", "mcp_read", "mcp_write", "request_approval", "review_approval"},
		DefaultScopes: []string{"mcp_read"}, DefaultTTLDays: 90, MaxTTLDays: 365,
		RotationDays: 90, RotationGraceHours: 24, AllowPersonalKeys: true,
	}
}

type mcpSettingsView struct {
	Enabled          bool     `json:"enabled"`
	Transport        string   `json:"transport"`
	EndpointPath     string   `json:"endpoint_path"`
	AllowedTools     []string `json:"allowed_tools"`
	AllowedResources []string `json:"allowed_resources"`
	RequiredScopes   []string `json:"required_scopes"`
}

func defaultMCPSettings() mcpSettingsView {
	return mcpSettingsView{
		Transport: "streamable-http", EndpointPath: "/mcp",
		AllowedTools:     []string{"list_teams", "list_channels", "search_messages", "get_thread", "create_post", "reply_to_thread", "list_pending_approvals", "approve_request", "reject_request"},
		AllowedResources: []string{"moyro://teams", "moyro://channels", "moyro://threads"},
		RequiredScopes:   []string{"mcp_read"},
	}
}

func (n *nativeServices) reloadMCPPolicy(ctx context.Context) error {
	value := defaultMCPSettings()
	err := n.loadJSON(ctx, "mcp", nativeSettingsKey, &value)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return err
	}
	if n.mcp != nil {
		n.mcp.ConfigurePolicy(value.AllowedTools, value.AllowedResources)
	}
	return nil
}

func (h *handlers) nativeSystemInfo(w http.ResponseWriter, r *http.Request) {
	info := buildinfo.Current()
	view := map[string]any{
		"name": "moyro", "version": info.Version, "build_hash": info.Commit,
		"build_date": info.BuildDate, "oidc_enabled": false, "approval_enabled": false,
		"local_signup_enabled": false,
	}
	if h.native != nil {
		view["local_signup_enabled"] = h.native.currentSiteSettings().LocalSignupEnabled
		if public, ok := h.native.oidc.PublicConfig(); ok {
			view["oidc_enabled"] = true
			view["oidc_provider_name"] = public.DisplayName
		}
		if enabled, err := h.native.approval.AnyEnabled(r.Context()); err == nil {
			view["approval_enabled"] = enabled
		}
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *handlers) getNativeSettings(w http.ResponseWriter, r *http.Request) {
	section := chi.URLParam(r, "section")
	var target any
	switch section {
	case "site":
		value := h.native.currentSiteSettings()
		target = &value
	case "key-policy":
		value := defaultKeyPolicy()
		target = &value
	case "mcp":
		value := defaultMCPSettings()
		target = &value
	default:
		writeError(w, http.StatusNotFound, "api.moyro.settings.section", "unknown settings section")
		return
	}
	err := h.native.loadJSON(r.Context(), section, nativeSettingsKey, target)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "api.moyro.settings.read", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, target)
}

func (h *handlers) patchNativeSettings(w http.ResponseWriter, r *http.Request) {
	section := chi.URLParam(r, "section")
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	actor := userID(r)
	switch section {
	case "site":
		value := defaultSiteSettings()
		if err := decoder.Decode(&value); err != nil {
			writeError(w, http.StatusBadRequest, "api.moyro.settings.body", err.Error())
			return
		}
		if err := validateSiteSettings(&value); err != nil {
			writeError(w, http.StatusBadRequest, "api.moyro.settings.site", err.Error())
			return
		}
		unlock := h.native.beginSettingsUpdate()
		defer unlock()
		if value.PublicBaseURL == "" {
			storedOIDC := defaultOIDCProvider()
			err := h.native.loadJSON(r.Context(), oidcSettingsSection, oidcSettingsKey, &storedOIDC)
			if err != nil && !errors.Is(err, settings.ErrNotFound) {
				writeError(w, http.StatusInternalServerError, "api.moyro.settings.oidc_read", err.Error())
				return
			}
			if err == nil && storedOIDC.Enabled {
				writeError(w, http.StatusBadRequest, "api.moyro.settings.public_url", "public base URL cannot be cleared while Keycloak is enabled")
				return
			}
		}
		baseURL := value.PublicBaseURL
		if baseURL == "" {
			var err error
			baseURL, err = externalOrigin(r)
			if err != nil {
				writeError(w, http.StatusBadRequest, "api.moyro.settings.site_origin", err.Error())
				return
			}
		}
		// If Keycloak is live, prepare its new callback snapshot before
		// committing the site URL. A discovery failure leaves both durable and
		// live settings untouched instead of returning 500 after a partial save.
		var preparedOIDC *oidcauth.Prepared
		oidcEnabled := false
		publicOIDC, liveOIDC := h.native.oidc.PublicConfig()
		if !liveOIDC || strings.TrimRight(publicOIDC.RedirectURL, "/") != strings.TrimRight(baseURL, "/")+oidcCallbackPath {
			var prepareErr error
			preparedOIDC, oidcEnabled, prepareErr = h.native.prepareStoredOIDC(r.Context(), baseURL)
			if prepareErr != nil {
				writeError(w, http.StatusBadRequest, "api.moyro.settings.oidc_prepare", prepareErr.Error())
				return
			}
		}
		if _, err := h.native.settings.PutJSON(r.Context(), section, nativeSettingsKey, value, actor, nil); err != nil {
			writeError(w, http.StatusInternalServerError, "api.moyro.settings.save", err.Error())
			return
		}
		h.native.applySite(value, h.outDisp)
		if oidcEnabled {
			if err := h.native.oidc.Activate(preparedOIDC); err != nil {
				writeError(w, http.StatusInternalServerError, "api.moyro.settings.oidc_activate", err.Error())
				return
			}
		}
		writeJSON(w, http.StatusOK, value)
	case "key-policy":
		value := defaultKeyPolicy()
		if err := decoder.Decode(&value); err != nil {
			writeError(w, http.StatusBadRequest, "api.moyro.settings.body", err.Error())
			return
		}
		if err := validateKeyPolicy(&value); err != nil {
			writeError(w, http.StatusBadRequest, "api.moyro.settings.key_policy", err.Error())
			return
		}
		unlock := h.native.beginSettingsUpdate()
		defer unlock()
		if _, err := h.native.settings.PutJSON(r.Context(), section, nativeSettingsKey, value, actor, nil); err != nil {
			writeError(w, http.StatusInternalServerError, "api.moyro.settings.save", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, value)
	case "mcp":
		value := defaultMCPSettings()
		if err := decoder.Decode(&value); err != nil {
			writeError(w, http.StatusBadRequest, "api.moyro.settings.body", err.Error())
			return
		}
		if err := validateMCPSettings(&value); err != nil {
			writeError(w, http.StatusBadRequest, "api.moyro.settings.mcp", err.Error())
			return
		}
		unlock := h.native.beginSettingsUpdate()
		defer unlock()
		if _, err := h.native.settings.PutJSON(r.Context(), section, nativeSettingsKey, value, actor, nil); err != nil {
			writeError(w, http.StatusInternalServerError, "api.moyro.settings.save", err.Error())
			return
		}
		if h.native.mcp != nil {
			h.native.mcp.ConfigurePolicy(value.AllowedTools, value.AllowedResources)
		}
		writeJSON(w, http.StatusOK, value)
	default:
		writeError(w, http.StatusNotFound, "api.moyro.settings.section", "unknown settings section")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(actor, "settings."+section+".update", section, nil)
	}
}

func validateSiteSettings(value *siteSettingsView) error {
	value.SiteName = strings.TrimSpace(value.SiteName)
	if value.SiteName == "" || utf8.RuneCountInString(value.SiteName) > 80 || strings.ContainsAny(value.SiteName, "\r\n\x00") {
		return errors.New("site name must contain between 1 and 80 visible characters")
	}
	value.PublicBaseURL = strings.TrimSpace(value.PublicBaseURL)
	if value.PublicBaseURL != "" {
		parsed, err := url.Parse(value.PublicBaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
			return errors.New("public base URL must be an absolute http(s) origin without a path, query, credentials, or fragment")
		}
		parsed.Path, parsed.RawPath = "", ""
		value.PublicBaseURL = strings.TrimRight(parsed.String(), "/")
	}

	hosts := make([]string, 0, len(value.AllowedOutgoingHosts))
	seen := make(map[string]struct{}, len(value.AllowedOutgoingHosts))
	if len(value.AllowedOutgoingHosts) > 256 {
		return errors.New("at most 256 outgoing hosts can be configured")
	}
	for _, raw := range value.AllowedOutgoingHosts {
		host := strings.ToLower(strings.TrimSpace(raw))
		if host == "" {
			continue
		}
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		}
		if ip := net.ParseIP(host); ip != nil {
			host = ip.String()
		} else {
			host = strings.TrimSuffix(host, ".")
			if len(host) > 253 || strings.ContainsAny(host, ":/\\@?#[] \t\r\n\x00") {
				return errors.New("outgoing hosts must contain hostnames or IP addresses only, without schemes, ports, or paths")
			}
			for _, label := range strings.Split(host, ".") {
				if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
					return errors.New("outgoing host contains an invalid DNS label")
				}
				for _, char := range label {
					if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' && char != '_' {
						return errors.New("outgoing host contains unsupported characters")
					}
				}
			}
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	slices.Sort(hosts)
	value.AllowedOutgoingHosts = hosts
	return nil
}

func validateKeyPolicy(value *keyPolicyView) error {
	value.AllowedScopes = canonicalNativeStrings(value.AllowedScopes)
	value.DefaultScopes = canonicalNativeStrings(value.DefaultScopes)
	if value.DefaultTTLDays < 1 || value.DefaultTTLDays > 365 || value.MaxTTLDays < value.DefaultTTLDays || value.MaxTTLDays > 365 || value.RotationDays < 1 || value.RotationDays > 365 || value.RotationGraceHours < 0 || value.RotationGraceHours > 24 {
		return errors.New("TTL, rotation, or grace value is outside the supported range")
	}
	for _, permission := range value.DefaultScopes {
		if !slices.Contains(value.AllowedScopes, permission) {
			return errors.New("default scopes must be included in allowed scopes")
		}
	}
	return nil
}

func validateMCPSettings(value *mcpSettingsView) error {
	value.AllowedTools = canonicalNativeStrings(value.AllowedTools)
	value.AllowedResources = canonicalNativeStrings(value.AllowedResources)
	value.RequiredScopes = canonicalNativeStrings(value.RequiredScopes)
	if value.Transport != "streamable-http" {
		return errors.New("this release supports the current Streamable HTTP transport only")
	}
	if value.EndpointPath != "/mcp" {
		return errors.New("the MCP endpoint path is fixed to /mcp in this release")
	}
	for _, scope := range value.RequiredScopes {
		if scope != "mcp_read" && scope != "mcp_write" && scope != "review_approval" {
			return errors.New("unsupported MCP required scope: " + scope)
		}
	}
	return nil
}

func canonicalNativeStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}
