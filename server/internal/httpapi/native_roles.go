package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/rbac"
)

func (h *handlers) getNativeEffectivePermissions(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "authentication required")
		return
	}
	permissions, err := h.native.rbac.EffectivePermissions(r.Context(), principal, rbac.Scope{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.permissions.effective", "effective permissions could not be resolved")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"permissions": permissions})
}

func (h *handlers) listNativePermissions(w http.ResponseWriter, r *http.Request) {
	permissions, err := h.native.rbac.ListPermissions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.permissions.list", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, permissions)
}

func (h *handlers) listNativeRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := h.native.rbac.ListRoles(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.roles.list", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, roles)
}

func (h *handlers) patchNativeRole(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Permissions []string `json:"permissions"`
		Revision    *int64   `json:"revision,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.roles.body", err.Error())
		return
	}
	role, err := h.native.rbac.PatchRolePermissions(
		r.Context(), chi.URLParam(r, "roleID"), input.Permissions, userID(r), input.Revision,
	)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, rbac.ErrNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, rbac.ErrRevisionConflict) {
			status = http.StatusConflict
		} else if errors.Is(err, rbac.ErrProtectedRole) {
			status = http.StatusUnprocessableEntity
		}
		writeError(w, status, "api.moyro.roles.patch", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), "rbac.role.permissions.update", role.ID, map[string]any{"permissions": role.Permissions, "revision": role.Revision})
	}
	writeJSON(w, http.StatusOK, role)
}
