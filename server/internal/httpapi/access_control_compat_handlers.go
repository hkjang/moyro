package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func compatAccessControlPolicy(id string) map[string]any {
	if id == "" {
		id = uuid.NewString()
	}
	return map[string]any{
		"id":           id,
		"name":         "compat-access-control",
		"display_name": "Compatibility access policy",
		"description":  "Compatibility placeholder for the enterprise access control policy surface.",
		"active":       false,
		"revision":     int64(0),
		"expression":   "",
		"create_at":    int64(0),
		"update_at":    int64(0),
		"delete_at":    int64(0),
	}
}

func (h *handlers) searchAccessControlPolicies(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"policies":    []any{},
		"total_count": 0,
	})
}

func (h *handlers) upsertAccessControlPolicy(w http.ResponseWriter, r *http.Request) {
	body := decodeCompatMap(r)
	if _, ok := body["id"]; !ok {
		body["id"] = uuid.NewString()
	}
	if _, ok := body["active"]; !ok {
		body["active"] = false
	}
	body["update_at"] = compatNowMillis()
	writeJSON(w, http.StatusOK, body)
}

func (h *handlers) getAccessControlPolicy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, compatAccessControlPolicy(chi.URLParam(r, "policyID")))
}

func (h *handlers) deleteAccessControlPolicy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func (h *handlers) activateAccessControlPolicies(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "OK",
		"active": true,
	})
}

func (h *handlers) getAccessControlPolicyActivation(w http.ResponseWriter, r *http.Request) {
	policy := compatAccessControlPolicy(chi.URLParam(r, "policyID"))
	policy["active"] = true
	writeJSON(w, http.StatusOK, policy)
}

func (h *handlers) assignAccessControlPolicy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"policy_id": chi.URLParam(r, "policyID"),
		"status":    "OK",
		"assigned":  true,
	})
}

func (h *handlers) unassignAccessControlPolicy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"policy_id":  chi.URLParam(r, "policyID"),
		"status":     "OK",
		"unassigned": true,
	})
}

func (h *handlers) listAccessControlPolicyChannels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"channels":    []any{},
		"total_count": 0,
	})
}

func (h *handlers) searchAccessControlPolicyChannels(w http.ResponseWriter, r *http.Request) {
	h.listAccessControlPolicyChannels(w, r)
}

func (h *handlers) getAccessControlCELFields(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []map[string]any{
		{"name": "user.roles", "type": "string", "repeated": true},
		{"name": "team.id", "type": "string", "repeated": false},
		{"name": "channel.id", "type": "string", "repeated": false},
	})
}

func (h *handlers) checkAccessControlCEL(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"is_valid": true,
		"errors":   []any{},
	})
}

func (h *handlers) testAccessControlCEL(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"matched": false,
		"errors":  []any{},
	})
}

func (h *handlers) validateAccessControlRequester(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"allowed": true,
		"errors":  []any{},
	})
}

func (h *handlers) visualAccessControlAST(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"type":     "empty",
		"children": []any{},
	})
}
