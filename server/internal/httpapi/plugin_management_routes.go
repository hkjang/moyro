package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/rbac"
)

// mountPluginManagementRoutes adds the authenticated plugin-management
// surface. The caller mounts this inside the existing /api/v4 authentication
// group; this narrower middleware permits delegated plugin administrators
// without exposing unrelated system configuration or audit endpoints.
func (h *handlers) mountPluginManagementRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.nativeRequire(rbac.PermissionManagePlugins))
		r.Get("/plugins", h.listPlugins)
		r.Get("/plugins/capabilities", h.getPluginManagementCapabilities)
		r.Post("/plugins", h.uploadPlugin)
		r.Delete("/plugins/{pluginID}", h.deletePlugin)
		r.Post("/plugins/{pluginID}/enable", h.enablePlugin)
		r.Post("/plugins/{pluginID}/disable", h.disablePlugin)
		r.Get("/plugins/{pluginID}/configuration", h.getPluginConfiguration)
		r.Put("/plugins/{pluginID}/configuration", h.updatePluginConfiguration)
	})
}

// getPluginManagementCapabilities intentionally exposes only the capability
// needed by the plugin page. Delegated plugin administrators must not need the
// system_admin-only /config snapshot (which includes unrelated deployment
// details) just to decide whether runtime controls should be enabled.
func (h *handlers) getPluginManagementCapabilities(w http.ResponseWriter, _ *http.Request) {
	available := h.host != nil
	writeJSON(w, http.StatusOK, map[string]bool{
		"management_enabled": available,
		"uploads_enabled":    available,
	})
}
