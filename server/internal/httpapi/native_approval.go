package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/apikeys"
	"github.com/hkjang/moyro/server/internal/approval"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/settings"
)

const (
	approvalSettingsSection = "approval"
	approvalSettingsKey     = "workflow"
)

type approvalPolicyView struct {
	ID                     string   `json:"id,omitempty"`
	Name                   string   `json:"name"`
	Enabled                bool     `json:"enabled"`
	ProtectedActions       []string `json:"protected_actions"`
	ReviewerRoles          []string `json:"reviewer_roles"`
	RequireRejectionReason bool     `json:"require_rejection_reason"`
	AllowSelfApproval      bool     `json:"allow_self_approval"`
	ExpiresAfterHours      int      `json:"expires_after_hours"`
}

func defaultApprovalPolicy() approvalPolicyView {
	return approvalPolicyView{
		ID: "default", Name: "Team lead review", ReviewerRoles: []string{"team_lead", "system_admin"},
		RequireRejectionReason: true, ExpiresAfterHours: 72,
	}
}

var supportedApprovalActions = map[string]struct{}{
	"mcp.create_post":     {},
	"mcp.reply_to_thread": {},
}

func configuredReviewerRoles(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var config struct {
		ReviewerRoles []string `json:"reviewer_roles"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	return canonicalNativeStrings(config.ReviewerRoles), nil
}

func (h *handlers) listNativeApprovalPolicies(w http.ResponseWriter, r *http.Request) {
	view := defaultApprovalPolicy()
	err := h.native.loadJSON(r.Context(), approvalSettingsSection, approvalSettingsKey, &view)
	if errors.Is(err, settings.ErrNotFound) {
		writeJSON(w, http.StatusOK, []approvalPolicyView{})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.approval.read", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, []approvalPolicyView{view})
}

func (h *handlers) saveNativeApprovalPolicy(w http.ResponseWriter, r *http.Request) {
	view := defaultApprovalPolicy()
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&view); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.approval.body", err.Error())
		return
	}
	view.ID = "default"
	view.Name = strings.TrimSpace(view.Name)
	view.ProtectedActions = canonicalNativeStrings(view.ProtectedActions)
	view.ReviewerRoles = canonicalNativeStrings(view.ReviewerRoles)
	if view.Name == "" || view.ExpiresAfterHours < 1 || view.ExpiresAfterHours > 24*365 || (view.Enabled && len(view.ProtectedActions) == 0) {
		writeError(w, http.StatusBadRequest, "api.moyro.approval.invalid", "name, protected actions, or expiry is invalid")
		return
	}
	for _, action := range view.ProtectedActions {
		if _, ok := supportedApprovalActions[action]; !ok {
			writeError(w, http.StatusBadRequest, "api.moyro.approval.unsupported_action", "unsupported protected action: "+action)
			return
		}
	}
	if view.Enabled {
		if len(view.ReviewerRoles) == 0 {
			writeError(w, http.StatusBadRequest, "api.moyro.approval.reviewers", "at least one reviewer role is required")
			return
		}
		roles, err := h.native.rbac.ListRoles(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "api.moyro.approval.roles", err.Error())
			return
		}
		known := make(map[string]struct{}, len(roles))
		for _, role := range roles {
			known[role.Name] = struct{}{}
		}
		for _, role := range view.ReviewerRoles {
			if _, ok := known[role]; !ok {
				writeError(w, http.StatusBadRequest, "api.moyro.approval.reviewers", "unknown reviewer role: "+role)
				return
			}
		}
	}
	config, _ := json.Marshal(map[string]any{
		"name": view.Name, "reviewer_roles": view.ReviewerRoles,
		"require_rejection_reason": view.RequireRejectionReason,
	})
	policies := make([]approval.Policy, 0, len(view.ProtectedActions))
	for _, action := range view.ProtectedActions {
		policies = append(policies, approval.Policy{
			ScopeType: "system", ActionType: action, Enabled: view.Enabled,
			ReviewerPermission: rbac.PermissionReviewApproval, ApprovalsRequired: 1,
			ForbidSelfApproval:  !view.AllowSelfApproval,
			ExpiresAfterSeconds: int64(view.ExpiresAfterHours) * 3600, Config: config,
		})
	}
	aggregate, err := json.Marshal(view)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.approval.encode", err.Error())
		return
	}
	if err := h.native.approval.ReplaceSystemPoliciesAndSetting(r.Context(), policies, aggregate, userID(r)); err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.approval.save", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), "approval.policy.update", view.ID, map[string]any{"enabled": view.Enabled, "actions": view.ProtectedActions})
	}
	writeJSON(w, http.StatusOK, view)
}

type submitApprovalRequest struct {
	ActionType     string          `json:"action_type"`
	TeamID         string          `json:"team_id,omitempty"`
	ResourceType   string          `json:"resource_type,omitempty"`
	ResourceID     string          `json:"resource_id,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
}

func (h *handlers) submitNativeApproval(w http.ResponseWriter, r *http.Request) {
	var input submitApprovalRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.approval.body", err.Error())
		return
	}
	if _, ok := supportedApprovalActions[input.ActionType]; !ok {
		writeError(w, http.StatusBadRequest, "api.moyro.approval.unsupported_action", "approval requests are created automatically for supported MCP writes")
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok || !principal.Restricted || principal.CredentialID == "" {
		writeError(w, http.StatusUnauthorized, "api.moyro.approval.mcp_key_required", "a scoped MCP API key must submit protected MCP approval requests")
		return
	}
	currentPrincipal, key, err := h.native.apiKeys.ResolveCurrent(r.Context(), principal.UserID, principal.CredentialID)
	if err != nil || key.Kind != apikeys.KindMCP {
		writeError(w, http.StatusUnauthorized, "api.moyro.approval.mcp_key_invalid", "the originating MCP API key is no longer valid")
		return
	}
	if input.ResourceType != "channel" || strings.TrimSpace(input.ResourceID) == "" {
		writeError(w, http.StatusBadRequest, "api.moyro.approval.resource", "supported MCP approvals require a channel resource")
		return
	}
	channel, err := h.channels.Get(r.Context(), input.ResourceID)
	if err != nil || channel == nil || (input.TeamID != "" && input.TeamID != channel.TeamID) {
		writeError(w, http.StatusBadRequest, "api.moyro.approval.resource", "destination channel is invalid")
		return
	}
	member, err := h.channels.IsMember(r.Context(), channel.ID, principal.UserID)
	if err != nil || !member {
		writeError(w, http.StatusForbidden, "api.moyro.approval.resource", "destination channel is not visible to the requester")
		return
	}
	for _, permission := range []string{rbac.PermissionMCPWrite, rbac.PermissionRequestApproval} {
		allowed, err := h.native.rbac.Allowed(r.Context(), currentPrincipal, permission, rbac.Scope{TeamID: channel.TeamID, ChannelID: channel.ID})
		if err != nil || !allowed {
			writeError(w, http.StatusForbidden, "api.moyro.approval.permission", "the MCP key lacks current permission for this channel")
			return
		}
	}
	var payload map[string]any
	if len(input.Payload) == 0 || json.Unmarshal(input.Payload, &payload) != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.approval.payload", "MCP approval payload must be a JSON object")
		return
	}
	payloadChannelID, _ := payload["channel_id"].(string)
	message, _ := payload["message"].(string)
	rootID, _ := payload["root_id"].(string)
	if payloadChannelID != channel.ID || strings.TrimSpace(message) == "" || (input.ActionType == "mcp.reply_to_thread" && rootID == "") {
		writeError(w, http.StatusBadRequest, "api.moyro.approval.payload", "MCP approval payload does not match the protected channel action")
		return
	}
	if rootID != "" {
		root, err := h.posts.Get(r.Context(), rootID)
		if err != nil || root == nil || root.DeleteAt != 0 || root.RootID != "" || root.ChannelID != channel.ID {
			writeError(w, http.StatusBadRequest, "api.moyro.approval.payload", "root post is not in the protected channel")
			return
		}
	}
	payload["_moyro_credential_id"] = principal.CredentialID
	input.Payload, err = json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.approval.payload", err.Error())
		return
	}
	input.TeamID = channel.TeamID
	result, err := h.native.approval.Submit(r.Context(), approval.Submission{
		ActionType: input.ActionType, RequesterID: userID(r), TeamID: input.TeamID,
		ResourceType: input.ResourceType, ResourceID: input.ResourceID,
		Payload: input.Payload, IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.approval.submit", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handlers) listMyApprovalRequests(w http.ResponseWriter, r *http.Request) {
	requests, err := h.native.approval.List(r.Context(), userID(r), false, r.URL.Query().Get("status"), 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.approval.list", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, requests)
}

func (h *handlers) listReviewApprovalRequests(w http.ResponseWriter, r *http.Request) {
	requests, err := h.native.approval.List(r.Context(), userID(r), true, r.URL.Query().Get("status"), 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.approval.list", err.Error())
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "authentication required")
		return
	}
	visible := make([]approval.Request, 0, len(requests))
	for i := range requests {
		scope, valid := h.approvalRequestScope(r.Context(), &requests[i])
		if !valid {
			continue
		}
		allowed, authErr := h.native.rbac.Allowed(r.Context(), principal, rbac.PermissionReviewApproval, scope)
		if authErr != nil {
			writeError(w, http.StatusInternalServerError, "api.moyro.approval.permission", authErr.Error())
			return
		}
		if allowed {
			visible = append(visible, requests[i])
		}
	}
	writeJSON(w, http.StatusOK, visible)
}

func (h *handlers) decideNativeApproval(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.approval.body", err.Error())
		return
	}
	requestID := chi.URLParam(r, "requestID")
	pending, err := h.native.approval.Get(r.Context(), requestID)
	if err != nil || pending == nil {
		writeError(w, http.StatusNotFound, "api.moyro.approval.not_found", "approval request not found")
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	scope, valid := h.approvalRequestScope(r.Context(), pending)
	if !ok || !valid {
		writeError(w, http.StatusForbidden, "api.moyro.approval.permission", "approval request is outside the credential scope")
		return
	}
	allowed, authErr := h.native.rbac.Allowed(r.Context(), principal, rbac.PermissionReviewApproval, scope)
	if authErr != nil || !allowed {
		writeError(w, http.StatusForbidden, "api.moyro.approval.permission", "approval request is outside the credential scope")
		return
	}
	request, err := h.native.approval.Decide(r.Context(), requestID, userID(r), input.Decision, input.Reason)
	if err == nil && request != nil && request.Status == "approved" && h.native.mcp != nil {
		_, request, err = h.native.mcp.ExecuteApproved(r.Context(), request)
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, approval.ErrForbidden) || errors.Is(err, approval.ErrSelfReview) {
			status = http.StatusForbidden
		}
		writeError(w, status, "api.moyro.approval.decide", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), "approval.request."+input.Decision, request.ID, map[string]any{"status": request.Status})
	}
	writeJSON(w, http.StatusOK, request)
}

func (h *handlers) approvalRequestScope(ctx context.Context, request *approval.Request) (rbac.Scope, bool) {
	if request == nil {
		return rbac.Scope{}, false
	}
	scope := rbac.Scope{TeamID: request.TeamID}
	switch request.ResourceType {
	case "channel":
		channel, err := h.channels.Get(ctx, request.ResourceID)
		if err != nil || channel == nil || channel.TeamID != request.TeamID {
			return rbac.Scope{}, false
		}
		scope.TeamID, scope.ChannelID = channel.TeamID, channel.ID
	case "team":
		if request.ResourceID != "" && request.ResourceID != request.TeamID {
			return rbac.Scope{}, false
		}
	default:
		return rbac.Scope{}, false
	}
	return scope, true
}
