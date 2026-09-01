package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/audit"
	"github.com/hkjang/moyro/server/internal/automations"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/ws"
)

type automationRuleRequest struct {
	Name       string               `json:"name"`
	ChannelID  string               `json:"channel_id"`
	Enabled    bool                 `json:"enabled"`
	MatchType  string               `json:"match_type"`
	MatchValue string               `json:"match_value"`
	Revision   int64                `json:"revision,omitempty"`
	Actions    []automations.Action `json:"actions"`
}

type automationHTTP struct {
	service  *automations.Service
	audit    *audit.Service
	events   *ws.Hub
	logger   *slog.Logger
	handlers *handlers
}

func newAutomationHTTP(service *automations.Service, auditService *audit.Service, events *ws.Hub, logger *slog.Logger, h *handlers) *automationHTTP {
	if logger == nil {
		logger = slog.Default()
	}
	return &automationHTTP{service: service, audit: auditService, events: events, logger: logger, handlers: h}
}

// mountNativeAutomationRoutes is intentionally isolated from router.go. The
// central router calls this once from its already-authenticated native group.
func mountNativeAutomationRoutes(r chi.Router, h *handlers, service *automations.Service) {
	api := newAutomationHTTP(service, h.audit, h.hub, h.logger, h)
	r.Get("/me/automation-rules", api.list)
	r.Post("/me/automation-rules", api.create)
	r.Patch("/me/automation-rules/{ruleID}", api.update)
	r.Delete("/me/automation-rules/{ruleID}", api.remove)
	r.Get("/me/automation-rules/{ruleID}/runs", api.listRuns)
}

func decodeAutomationBody(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain exactly one JSON object")
		}
		return err
	}
	return nil
}

func automationInput(request automationRuleRequest) automations.SaveInput {
	return automations.SaveInput{
		Name: request.Name, ChannelID: request.ChannelID, Enabled: request.Enabled,
		MatchType: request.MatchType, MatchValue: request.MatchValue,
		Revision: request.Revision, Actions: request.Actions,
	}
}

func writeAutomationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, automations.ErrInvalid):
		writeError(w, http.StatusBadRequest, "api.moyro.automation.invalid", "자동화 규칙 정보가 올바르지 않습니다.")
	case errors.Is(err, automations.ErrForbidden):
		writeError(w, http.StatusForbidden, "api.moyro.automation.forbidden", "이 채널에서 자동화 규칙을 관리할 수 없습니다.")
	case errors.Is(err, automations.ErrNotFound):
		writeError(w, http.StatusNotFound, "api.moyro.automation.not_found", "자동화 규칙을 찾을 수 없습니다.")
	case errors.Is(err, automations.ErrRevisionConflict):
		writeError(w, http.StatusConflict, "api.moyro.automation.revision_conflict", "다른 화면에서 규칙이 변경되었습니다. 새로고침 후 다시 시도하세요.")
	default:
		writeError(w, http.StatusInternalServerError, "api.moyro.automation.app_error", "자동화 규칙을 처리하지 못했습니다.")
	}
}

func (api *automationHTTP) list(w http.ResponseWriter, r *http.Request) {
	if !requireRestrictedWorkPermission(w, r, api.handlers, rbac.PermissionMCPRead) {
		return
	}
	rules, err := api.service.ListForPrincipal(r.Context(), requestPrincipal(r))
	if err != nil {
		api.logger.Error("list automation rules", "actor_id", userID(r), "err", err)
		writeAutomationError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, rules)
}

func (api *automationHTTP) create(w http.ResponseWriter, r *http.Request) {
	if !requireRestrictedWorkPermission(w, r, api.handlers, rbac.PermissionMCPWrite) {
		return
	}
	var request automationRuleRequest
	if err := decodeAutomationBody(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.automation.body", "요청 본문이 올바르지 않습니다.")
		return
	}
	rule, err := api.service.CreateForPrincipal(r.Context(), requestPrincipal(r), automationInput(request))
	if err != nil {
		writeAutomationError(w, err)
		return
	}
	if api.audit != nil {
		api.audit.LogAsync(userID(r), "automation.rule.create", rule.ID, map[string]any{"channel_id": rule.ChannelID, "actions": len(rule.Actions)})
	}
	api.broadcast(userID(r), rule.ID, "created")
	writeJSON(w, http.StatusCreated, rule)
}

func (api *automationHTTP) update(w http.ResponseWriter, r *http.Request) {
	if !requireRestrictedWorkPermission(w, r, api.handlers, rbac.PermissionMCPWrite) {
		return
	}
	var request automationRuleRequest
	if err := decodeAutomationBody(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.automation.body", "요청 본문이 올바르지 않습니다.")
		return
	}
	rule, err := api.service.UpdateForPrincipal(r.Context(), requestPrincipal(r), chi.URLParam(r, "ruleID"), automationInput(request))
	if err != nil {
		writeAutomationError(w, err)
		return
	}
	if api.audit != nil {
		api.audit.LogAsync(userID(r), "automation.rule.update", rule.ID, map[string]any{"enabled": rule.Enabled, "revision": rule.Revision})
	}
	api.broadcast(userID(r), rule.ID, "updated")
	writeJSON(w, http.StatusOK, rule)
}

func (api *automationHTTP) remove(w http.ResponseWriter, r *http.Request) {
	if !requireRestrictedWorkPermission(w, r, api.handlers, rbac.PermissionMCPWrite) {
		return
	}
	ruleID := chi.URLParam(r, "ruleID")
	if err := api.service.DeleteForPrincipal(r.Context(), requestPrincipal(r), ruleID); err != nil {
		writeAutomationError(w, err)
		return
	}
	if api.audit != nil {
		api.audit.LogAsync(userID(r), "automation.rule.delete", ruleID, nil)
	}
	api.broadcast(userID(r), ruleID, "deleted")
	w.WriteHeader(http.StatusNoContent)
}

func (api *automationHTTP) listRuns(w http.ResponseWriter, r *http.Request) {
	if !requireRestrictedWorkPermission(w, r, api.handlers, rbac.PermissionMCPRead) {
		return
	}
	limit := 30
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "api.moyro.automation.invalid_limit", "limit은 1부터 100 사이여야 합니다.")
			return
		}
		limit = parsed
	}
	runs, err := api.service.ListRunsForPrincipal(r.Context(), requestPrincipal(r), chi.URLParam(r, "ruleID"), limit)
	if err != nil {
		writeAutomationError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, runs)
}

func (api *automationHTTP) broadcast(actorID, ruleID, change string) {
	if api.events == nil {
		return
	}
	api.events.Broadcast(ws.Event{
		Event:     "automation_rule_changed",
		Data:      map[string]any{"rule_id": ruleID, "change": change},
		Broadcast: ws.Broadcast{UserID: actorID},
	})
}
