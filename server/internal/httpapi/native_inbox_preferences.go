package httpapi

import (
	"errors"
	"net/http"

	"github.com/hkjang/moyro/server/internal/activityevents"
	"github.com/hkjang/moyro/server/internal/inboxprefs"
	"github.com/hkjang/moyro/server/internal/rbac"
)

type inboxPreferencesPatch struct {
	VIPUserIDs           *[]string                   `json:"vip_user_ids,omitempty"`
	PriorityEventTypes   *[]activityevents.EventType `json:"priority_event_types,omitempty"`
	BundleBy             *string                     `json:"bundle_by,omitempty"`
	SnoozePresetsMinutes *[]int                      `json:"snooze_presets_minutes,omitempty"`
	WorkHoursEnabled     *bool                       `json:"work_hours_enabled,omitempty"`
	WorkHoursTimezone    *string                     `json:"work_hours_timezone,omitempty"`
	WorkHoursWeekdays    *[]int16                    `json:"work_hours_weekdays,omitempty"`
	WorkHoursStartMinute *int                        `json:"work_hours_start_minute,omitempty"`
	WorkHoursEndMinute   *int                        `json:"work_hours_end_minute,omitempty"`
	PriorityOverride     *bool                       `json:"priority_override,omitempty"`
}

func (p inboxPreferencesPatch) empty() bool {
	return p.VIPUserIDs == nil && p.PriorityEventTypes == nil && p.BundleBy == nil &&
		p.SnoozePresetsMinutes == nil && p.WorkHoursEnabled == nil &&
		p.WorkHoursTimezone == nil && p.WorkHoursWeekdays == nil &&
		p.WorkHoursStartMinute == nil && p.WorkHoursEndMinute == nil &&
		p.PriorityOverride == nil
}

func (p inboxPreferencesPatch) servicePatch() inboxprefs.Patch {
	return inboxprefs.Patch{
		VIPUserIDs: p.VIPUserIDs, PriorityEventTypes: p.PriorityEventTypes,
		BundleBy: p.BundleBy, SnoozePresetsMinutes: p.SnoozePresetsMinutes,
		WorkHoursEnabled: p.WorkHoursEnabled, WorkHoursTimezone: p.WorkHoursTimezone,
		WorkHoursWeekdays: p.WorkHoursWeekdays, WorkHoursStartMinute: p.WorkHoursStartMinute,
		WorkHoursEndMinute: p.WorkHoursEndMinute, PriorityOverride: p.PriorityOverride,
	}
}

// getNativeInboxPreferences backs GET /api/moyro/v1/me/inbox-preferences.
// It is intentionally constructed from the shared DB handle so the feature
// adds no state to the central handlers struct.
func (h *handlers) getNativeInboxPreferences(w http.ResponseWriter, r *http.Request) {
	if !h.requireInboxPreferenceGrant(w, r, rbac.PermissionMCPRead) {
		return
	}
	if h.auth == nil || h.auth.DB() == nil {
		writeError(w, http.StatusServiceUnavailable, "api.moyro.inbox_preferences.unavailable", "inbox preferences are unavailable")
		return
	}
	prefs, err := inboxprefs.New(h.auth.DB()).Get(r.Context(), userID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.inbox_preferences.get", "inbox preferences could not be loaded")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, prefs)
}

// patchNativeInboxPreferences backs PATCH
// /api/moyro/v1/me/inbox-preferences.  Omitted fields retain their current
// value; unknown fields and trailing JSON are rejected by decodeActivityBody.
func (h *handlers) patchNativeInboxPreferences(w http.ResponseWriter, r *http.Request) {
	if !h.requireInboxPreferenceGrant(w, r, rbac.PermissionMCPWrite) {
		return
	}
	if h.auth == nil || h.auth.DB() == nil {
		writeError(w, http.StatusServiceUnavailable, "api.moyro.inbox_preferences.unavailable", "inbox preferences are unavailable")
		return
	}
	var patch inboxPreferencesPatch
	if err := decodeActivityBody(w, r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.inbox_preferences.body", err.Error())
		return
	}
	if patch.empty() {
		writeError(w, http.StatusBadRequest, "api.moyro.inbox_preferences.empty", "at least one preference is required")
		return
	}
	service := inboxprefs.New(h.auth.DB())
	updated, err := service.Patch(r.Context(), userID(r), patch.servicePatch())
	if err != nil {
		if errors.Is(err, inboxprefs.ErrInvalid) {
			writeError(w, http.StatusBadRequest, "api.moyro.inbox_preferences.invalid", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "api.moyro.inbox_preferences.save", "inbox preferences could not be saved")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, updated)
}

// requireInboxPreferenceGrant keeps browser sessions and unrestricted PATs
// compatible while preventing a narrowly scoped managed API key from reading
// or rewriting user preferences outside its explicit grant intersection.
func (h *handlers) requireInboxPreferenceGrant(w http.ResponseWriter, r *http.Request, permission string) bool {
	principal := requestPrincipal(r)
	if !principal.Restricted {
		return true
	}
	if _, granted := principal.GrantedPermissions[permission]; !granted {
		writeError(w, http.StatusForbidden, "api.moyro.inbox_preferences.scope", "restricted credential is missing required scope: "+permission)
		return false
	}
	if h == nil || h.native == nil || h.native.rbac == nil {
		writeError(w, http.StatusServiceUnavailable, "api.moyro.disabled", "moyro management services are unavailable")
		return false
	}
	allowed, err := h.native.rbac.Allowed(r.Context(), principal, permission, rbac.Scope{})
	if err != nil {
		if h.logger != nil {
			h.logger.Error("inbox preference permission check failed", "actor_id", principal.UserID, "permission", permission, "err", err)
		}
		writeError(w, http.StatusInternalServerError, "api.moyro.inbox_preferences.authorization", "inbox preference authorization failed")
		return false
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "api.moyro.inbox_preferences.scope", "credential owner is missing current permission: "+permission)
		return false
	}
	return true
}
