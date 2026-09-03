package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func compatNowMillis() int64 { return time.Now().UnixMilli() }

func decodeCompatMap(r *http.Request) map[string]any {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body == nil {
		body = map[string]any{}
	}
	return body
}

func (h *handlers) compatOK(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func (h *handlers) compatNoContent(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) compatEmptyList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (h *handlers) compatEmptyObject(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{})
}

// ---- Interactive dialog compatibility ----

func (h *handlers) openDialog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func (h *handlers) lookupDialog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"type":  "ok",
		"items": []any{},
	})
}

func (h *handlers) submitDialog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "OK",
		"errors": map[string]string{},
	})
}

// ---- Audit-log certificate compatibility ----

func (h *handlers) uploadAuditLogCertificate(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseMultipartForm(8 << 20)
	writeJSON(w, http.StatusCreated, map[string]string{"status": "OK"})
}

func (h *handlers) deleteAuditLogCertificate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

// ---- Content flagging compatibility ----

func (h *handlers) getContentFlaggingFields(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []map[string]any{
		{
			"id":          "reason",
			"name":        "Reason",
			"type":        "select",
			"required":    false,
			"attrs":       map[string]any{"options": []string{}},
			"create_at":   int64(0),
			"update_at":   int64(0),
			"delete_at":   int64(0),
			"description": "Compatibility placeholder for disabled content flagging.",
		},
	})
}

func (h *handlers) getContentFlaggingPost(w http.ResponseWriter, r *http.Request) {
	postID := chi.URLParam(r, "postID")
	writeJSON(w, http.StatusOK, map[string]any{
		"post_id":          postID,
		"status":           "none",
		"assigned_user_id": "",
		"reported_by":      "",
		"create_at":        int64(0),
		"update_at":        int64(0),
	})
}

func (h *handlers) getContentFlaggingPostFieldValues(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (h *handlers) searchContentFlaggingTeamReviewers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (h *handlers) getContentFlaggingTeamStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"team_id":   chi.URLParam(r, "teamID"),
		"enabled":   false,
		"reviewers": []any{},
	})
}

func (h *handlers) assignContentFlaggedPost(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"post_id":          chi.URLParam(r, "postID"),
		"assigned_user_id": chi.URLParam(r, "userID"),
		"status":           "assigned",
		"update_at":        compatNowMillis(),
	})
}

func (h *handlers) flagContentPost(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusCreated, map[string]any{
		"post_id":   chi.URLParam(r, "postID"),
		"status":    "flagged",
		"create_at": compatNowMillis(),
	})
}

func (h *handlers) keepContentFlaggedPost(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"post_id":   chi.URLParam(r, "postID"),
		"status":    "kept",
		"update_at": compatNowMillis(),
	})
}

func (h *handlers) removeContentFlaggedPost(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"post_id":   chi.URLParam(r, "postID"),
		"status":    "removed",
		"update_at": compatNowMillis(),
	})
}

// ---- Compliance reports compatibility ----

func (h *handlers) listComplianceReports(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (h *handlers) createComplianceReport(w http.ResponseWriter, r *http.Request) {
	body := decodeCompatMap(r)
	id := uuid.NewString()
	body["id"] = id
	body["status"] = "finished"
	body["create_at"] = compatNowMillis()
	writeJSON(w, http.StatusCreated, body)
}

func (h *handlers) getComplianceReport(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "reportID")
	writeJSON(w, http.StatusOK, map[string]any{
		"id":        id,
		"status":    "finished",
		"type":      "adhoc",
		"create_at": int64(0),
	})
}

func (h *handlers) downloadComplianceReport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	// reportID is a caller-supplied path segment, so it can carry a quote that
	// would otherwise close the header's quoted-string early.
	w.Header().Set("Content-Disposition", contentDispositionAttachment("compliance-"+chi.URLParam(r, "reportID")+".csv"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("post_id,user_id,channel_id,create_at,message\n"))
}

// ---- Data retention compatibility ----

func dataRetentionPolicy(id string) map[string]any {
	if id == "" {
		id = "global"
	}
	return map[string]any{
		"id":                       id,
		"display_name":             "Global retention",
		"message_deletion_enabled": false,
		"file_deletion_enabled":    false,
		"message_retention_cutoff": int64(0),
		"file_retention_cutoff":    int64(0),
		"create_at":                int64(0),
		"update_at":                int64(0),
		"delete_at":                int64(0),
	}
}

func (h *handlers) getDataRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, dataRetentionPolicy(""))
}

func (h *handlers) listDataRetentionPolicies(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (h *handlers) getDataRetentionPoliciesCount(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]int{"total_count": 0})
}

func (h *handlers) getDataRetentionPolicyByID(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, dataRetentionPolicy(chi.URLParam(r, "policyID")))
}

func (h *handlers) createDataRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	body := decodeCompatMap(r)
	if _, ok := body["id"]; !ok {
		body["id"] = uuid.NewString()
	}
	body["create_at"] = compatNowMillis()
	writeJSON(w, http.StatusCreated, body)
}

func (h *handlers) patchDataRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	body := decodeCompatMap(r)
	body["id"] = chi.URLParam(r, "policyID")
	body["update_at"] = compatNowMillis()
	writeJSON(w, http.StatusOK, body)
}

func (h *handlers) deleteDataRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

// ---- Groups and schemes compatibility ----

func compatGroup(id string) map[string]any {
	if id == "" {
		id = uuid.NewString()
	}
	return map[string]any{
		"id":           id,
		"name":         "compat-group",
		"display_name": "Compatibility Group",
		"source":       "custom",
		"remote_id":    "",
		"create_at":    int64(0),
		"update_at":    int64(0),
		"delete_at":    int64(0),
	}
}

func (h *handlers) listGroups(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (h *handlers) createGroup(w http.ResponseWriter, r *http.Request) {
	if h.denyGuestMutation(w, r, "api.group.create.guest_forbidden") {
		return
	}
	body := decodeCompatMap(r)
	if _, ok := body["id"]; !ok {
		body["id"] = uuid.NewString()
	}
	body["create_at"] = compatNowMillis()
	writeJSON(w, http.StatusCreated, body)
}

func (h *handlers) getGroup(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, compatGroup(chi.URLParam(r, "groupID")))
}

func (h *handlers) getGroupsByNames(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (h *handlers) getGroupStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"group_id":           chi.URLParam(r, "groupID"),
		"total_member_count": 0,
	})
}

func (h *handlers) patchGroup(w http.ResponseWriter, r *http.Request) {
	body := decodeCompatMap(r)
	body["id"] = chi.URLParam(r, "groupID")
	body["update_at"] = compatNowMillis()
	writeJSON(w, http.StatusOK, body)
}

func (h *handlers) restoreGroup(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, compatGroup(chi.URLParam(r, "groupID")))
}

func compatScheme(id string) map[string]any {
	if id == "" {
		id = uuid.NewString()
	}
	return map[string]any{
		"id":                         id,
		"name":                       "compat-scheme",
		"display_name":               "Compatibility Scheme",
		"description":                "",
		"scope":                      "team",
		"default_team_admin_role":    "team_admin",
		"default_team_user_role":     "team_user",
		"default_channel_admin_role": "channel_admin",
		"default_channel_user_role":  "channel_user",
		"create_at":                  int64(0),
		"update_at":                  int64(0),
		"delete_at":                  int64(0),
	}
}

func (h *handlers) listSchemes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (h *handlers) createScheme(w http.ResponseWriter, r *http.Request) {
	body := decodeCompatMap(r)
	if _, ok := body["id"]; !ok {
		body["id"] = uuid.NewString()
	}
	body["create_at"] = compatNowMillis()
	writeJSON(w, http.StatusCreated, body)
}

func (h *handlers) getScheme(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, compatScheme(chi.URLParam(r, "schemeID")))
}

func (h *handlers) patchScheme(w http.ResponseWriter, r *http.Request) {
	body := decodeCompatMap(r)
	body["id"] = chi.URLParam(r, "schemeID")
	body["update_at"] = compatNowMillis()
	writeJSON(w, http.StatusOK, body)
}

// ---- Remote cluster / shared channel compatibility ----

func compatRemoteCluster(id string) map[string]any {
	if id == "" {
		id = uuid.NewString()
	}
	return map[string]any{
		"id":              id,
		"name":            "compat-remote",
		"display_name":    "Compatibility Remote",
		"site_url":        "",
		"create_at":       int64(0),
		"last_ping_at":    int64(0),
		"delete_at":       int64(0),
		"remote_id":       "",
		"topics":          "",
		"creator_id":      "",
		"remote_username": "",
	}
}

func (h *handlers) listRemoteClusters(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (h *handlers) createRemoteCluster(w http.ResponseWriter, r *http.Request) {
	body := decodeCompatMap(r)
	if _, ok := body["id"]; !ok {
		body["id"] = uuid.NewString()
	}
	body["create_at"] = compatNowMillis()
	writeJSON(w, http.StatusCreated, body)
}

func (h *handlers) getRemoteCluster(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, compatRemoteCluster(chi.URLParam(r, "remoteID")))
}

func (h *handlers) patchRemoteCluster(w http.ResponseWriter, r *http.Request) {
	body := decodeCompatMap(r)
	body["id"] = chi.URLParam(r, "remoteID")
	body["update_at"] = compatNowMillis()
	writeJSON(w, http.StatusOK, body)
}

func (h *handlers) generateRemoteClusterInvite(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusCreated, map[string]any{
		"remote_id":  chi.URLParam(r, "remoteID"),
		"invite":     "",
		"expires_at": int64(0),
	})
}

func (h *handlers) acceptRemoteClusterInvite(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusCreated, compatRemoteCluster(""))
}

func (h *handlers) remoteClusterChannelInvite(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func (h *handlers) getSharedChannel(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	writeJSON(w, http.StatusOK, map[string]any{
		"channel_id": channelID,
		"home":       true,
		"readonly":   false,
		"share_name": "",
		"remote_id":  "",
	})
}

func (h *handlers) getSharedChannelRemoteInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"remote_id": chi.URLParam(r, "remoteID"),
		"site_url":  "",
		"name":      "",
	})
}

func (h *handlers) canDMSharedChannelUser(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"can_dm": false})
}

// ---- Property field compatibility ----

func (h *handlers) createPropertyField(w http.ResponseWriter, r *http.Request) {
	body := decodeCompatMap(r)
	body["id"] = uuid.NewString()
	body["group_id"] = chi.URLParam(r, "groupID")
	body["create_at"] = compatNowMillis()
	writeJSON(w, http.StatusCreated, body)
}

func (h *handlers) patchPropertyField(w http.ResponseWriter, r *http.Request) {
	body := decodeCompatMap(r)
	body["id"] = chi.URLParam(r, "fieldID")
	body["group_id"] = chi.URLParam(r, "groupID")
	body["update_at"] = compatNowMillis()
	writeJSON(w, http.StatusOK, body)
}

func (h *handlers) getPropertyValue(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"group_id":  chi.URLParam(r, "groupID"),
		"target_id": chi.URLParam(r, "targetID"),
		"field_id":  chi.URLParam(r, "fieldID"),
		"value":     nil,
	})
}

func (h *handlers) patchPropertyValue(w http.ResponseWriter, r *http.Request) {
	body := decodeCompatMap(r)
	body["group_id"] = chi.URLParam(r, "groupID")
	body["target_id"] = chi.URLParam(r, "targetID")
	body["field_id"] = chi.URLParam(r, "fieldID")
	body["update_at"] = compatNowMillis()
	writeJSON(w, http.StatusOK, body)
}

func (h *handlers) normalizeCompatQuery(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Encode())
	writeJSON(w, http.StatusOK, map[string]any{"query": q})
}
