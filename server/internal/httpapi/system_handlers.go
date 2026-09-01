package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/hkjang/moyro/server/internal/buildinfo"
	"github.com/hkjang/moyro/server/internal/config"
)

func (h *handlers) getClientConfig(w http.ResponseWriter, r *http.Request) {
	build := buildinfo.Current()
	baseURL := h.effectivePublicBaseURL(r)
	siteName := h.effectiveSiteName()
	linkPreviews := true
	if h.cfg != nil {
		linkPreviews = h.cfg.LinkPreviewsEnabled
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"Version":                          build.Version,
		"BuildNumber":                      build.Version,
		"BuildDate":                        build.BuildDate,
		"BuildHash":                        build.Commit,
		"SiteName":                         siteName,
		"SiteURL":                          baseURL,
		"WebsocketURL":                     websocketURLForBase(baseURL),
		"DiagnosticId":                     "",
		"EnableCommands":                   "true",
		"EnableCustomEmoji":                "true",
		"EnableEmailInvitations":           "true",
		"EnableFileAttachments":            "true",
		"EnableIncomingWebhooks":           "true",
		"EnableLinkPreviews":               boolString(linkPreviews),
		"EnableOAuthServiceProvider":       "false",
		"EnableOutgoingWebhooks":           "true",
		"EnablePostIconOverride":           "true",
		"EnablePostUsernameOverride":       "true",
		"EnablePreviewFeatures":            "true",
		"EnablePublicLink":                 "false",
		"EnableSignInWithEmail":            "true",
		"EnableSignInWithUsername":         "true",
		"EnableSignUpWithEmail":            "true",
		"ExperimentalPrimaryTeam":          "",
		"ExperimentalTownSquareIsReadOnly": "false",
		"SendEmailNotifications":           boolString(h.emailDigestEnabled()),
		"TeammateNameDisplay":              "username",
		"ThreadAutoFollow":                 "true",
	})
}

func (h *handlers) emailDigestEnabled() bool {
	return h != nil && h.cfg != nil && strings.TrimSpace(h.cfg.SMTPHost) != ""
}

func (h *handlers) effectiveSiteName() string {
	if h != nil && h.native != nil {
		if name := strings.TrimSpace(h.native.currentSiteSettings().SiteName); name != "" {
			return name
		}
	}
	return "moyro"
}

// effectivePublicBaseURL prefers the administrator-managed database value.
// Before it is configured, it derives the origin from the direct request so
// an air-gapped deployment is not hard-coded to localhost. Forwarded headers
// remain ignored; administrators behind a proxy should save the canonical URL.
func (h *handlers) effectivePublicBaseURL(r *http.Request) string {
	if h != nil && h.native != nil {
		if base := strings.TrimSpace(h.native.currentSiteSettings().PublicBaseURL); base != "" {
			return strings.TrimRight(base, "/")
		}
	}
	// Preserve explicit transitional values used by compatibility tests and
	// embedders, but do not let the fixed process default leak to remote users.
	if h != nil && h.cfg != nil {
		if base := strings.TrimSpace(h.cfg.PublicBaseURL); base != "" && base != config.DefaultPublicBaseURL {
			return strings.TrimRight(base, "/")
		}
	}
	if r != nil {
		if origin, err := h.externalOrigin(r); err == nil {
			return origin
		}
	}
	return config.DefaultPublicBaseURL
}

func (h *handlers) getClientLicense(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format == "" {
		writeError(w, http.StatusBadRequest, "api.license.client.old_format.app_error", "format is required")
		return
	}
	if format != "old" {
		writeError(w, http.StatusBadRequest, "api.context.invalid_param.app_error", "invalid format")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"IsLicensed":   "false",
		"SkuShortName": "",
	})
}

func (h *handlers) getEnvironmentConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (h *handlers) getSupportedTimezones(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, supportedTimezones)
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func websocketURLForBase(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	u.Path = "/api/v4/websocket"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

var supportedTimezones = []string{
	"UTC",
	"Africa/Cairo",
	"Africa/Johannesburg",
	"America/Anchorage",
	"America/Argentina/Buenos_Aires",
	"America/Chicago",
	"America/Denver",
	"America/Los_Angeles",
	"America/Mexico_City",
	"America/New_York",
	"America/Phoenix",
	"America/Sao_Paulo",
	"America/Toronto",
	"Asia/Dubai",
	"Asia/Hong_Kong",
	"Asia/Jakarta",
	"Asia/Kolkata",
	"Asia/Seoul",
	"Asia/Shanghai",
	"Asia/Singapore",
	"Asia/Tokyo",
	"Australia/Melbourne",
	"Australia/Perth",
	"Australia/Sydney",
	"Europe/Amsterdam",
	"Europe/Berlin",
	"Europe/London",
	"Europe/Madrid",
	"Europe/Paris",
	"Europe/Rome",
	"Europe/Stockholm",
	"Europe/Warsaw",
	"Pacific/Auckland",
}
