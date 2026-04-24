package httpapi

import (
	"net/http"
	"net/url"
	"strings"
)

func (h *handlers) getClientConfig(w http.ResponseWriter, r *http.Request) {
	baseURL := "http://localhost:8065"
	linkPreviews := true
	if h.cfg != nil {
		if h.cfg.PublicBaseURL != "" {
			baseURL = h.cfg.PublicBaseURL
		}
		linkPreviews = h.cfg.LinkPreviewsEnabled
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"Version":                          "0.1.0",
		"BuildNumber":                      "dev",
		"BuildDate":                        "",
		"BuildHash":                        "",
		"SiteName":                         "Moddle",
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
		"SendEmailNotifications":           "false",
		"TeammateNameDisplay":              "username",
		"ThreadAutoFollow":                 "true",
	})
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
