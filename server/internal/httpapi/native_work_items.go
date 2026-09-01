package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/activityevents"
	"github.com/hkjang/moyro/server/internal/audit"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/workitems"
	"github.com/hkjang/moyro/server/internal/ws"
)

func requireRestrictedWorkPermission(w http.ResponseWriter, r *http.Request, h *handlers, permission string) bool {
	principal := requestPrincipal(r)
	if !principal.Restricted {
		return true
	}
	if _, granted := principal.GrantedPermissions[permission]; !granted {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "missing permission: "+permission)
		return false
	}
	if h == nil || h.native == nil || h.native.rbac == nil {
		writeError(w, http.StatusServiceUnavailable, "api.moyro.disabled", "moyro management services are unavailable")
		return false
	}
	allowed, err := h.native.rbac.Allowed(r.Context(), principal, permission, rbac.Scope{})
	if err != nil {
		if h.logger != nil {
			h.logger.Error("work management authorization failed", "actor_id", principal.UserID, "permission", permission, "err", err)
		}
		writeError(w, http.StatusInternalServerError, "api.moyro.work_item.authorization", "work management authorization failed")
		return false
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "missing permission: "+permission)
		return false
	}
	return true
}

type createNativeWorkItemRequest struct {
	Kind               string   `json:"kind"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	AssigneeID         string   `json:"assignee_id"`
	SourcePostID       string   `json:"source_post_id"`
	DueAt              int64    `json:"due_at"`
	IdempotencyKey     string   `json:"idempotency_key"`
	Priority           string   `json:"priority"`
	ReviewerID         string   `json:"reviewer_id"`
	InitialStatus      string   `json:"initial_status"`
	RecurrenceUnit     string   `json:"recurrence_unit"`
	RecurrenceInterval int      `json:"recurrence_interval"`
	SupersedesID       string   `json:"supersedes_id"`
	DependencyIDs      []string `json:"dependency_ids"`
	ImpactTaskIDs      []string `json:"impact_task_ids"`
}

type createNativeWorkItemResponse struct {
	Item     workitems.Item `json:"item"`
	Replayed bool           `json:"replayed"`
}

type patchNativeWorkItemRequest struct {
	Title              *string `json:"title"`
	Description        *string `json:"description"`
	Status             *string `json:"status"`
	AssigneeID         *string `json:"assignee_id"`
	DueAt              *int64  `json:"due_at"`
	Priority           *string `json:"priority"`
	ReviewerID         *string `json:"reviewer_id"`
	RecurrenceUnit     *string `json:"recurrence_unit"`
	RecurrenceInterval *int    `json:"recurrence_interval"`
}

func decodeNativeWorkItemBody(w http.ResponseWriter, r *http.Request, target any) error {
	// 10,000 Unicode code points can occupy 40KB in UTF-8 before JSON
	// framing. Keep this above the domain limit while still bounding abuse.
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
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

func parseNativeWorkItemListOptions(query url.Values) (workitems.ListOptions, error) {
	options := workitems.ListOptions{
		Kind: strings.TrimSpace(query.Get("kind")), Status: strings.TrimSpace(query.Get("status")),
		Cursor: strings.TrimSpace(query.Get("cursor")),
	}
	if raw := strings.TrimSpace(query.Get("per_page")); raw != "" {
		pageSize, err := strconv.Atoi(raw)
		if err != nil || pageSize < 1 || pageSize > workitems.MaxPageSize {
			return workitems.ListOptions{}, errors.New("per_page must be between 1 and 100")
		}
		options.PageSize = pageSize
	}
	for key, target := range map[string]*int64{"due_from": &options.DueFrom, "due_to": &options.DueTo} {
		if raw := strings.TrimSpace(query.Get(key)); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value < 0 {
				return workitems.ListOptions{}, errors.New(key + " must be a non-negative millisecond timestamp")
			}
			*target = value
		}
	}
	options.Sort = strings.TrimSpace(query.Get("sort"))
	return options, nil
}

func writeNativeWorkItemError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workitems.ErrInvalid):
		writeError(w, http.StatusBadRequest, "api.moyro.work_item.invalid", "요청한 작업 또는 결정 정보가 올바르지 않습니다.")
	case errors.Is(err, workitems.ErrSourceNotAccessible), errors.Is(err, workitems.ErrNotFound):
		writeError(w, http.StatusNotFound, "api.moyro.work_item.not_found", "작업 또는 원본 메시지를 찾을 수 없습니다.")
	case errors.Is(err, workitems.ErrForbidden):
		writeError(w, http.StatusForbidden, "api.moyro.work_item.forbidden", "이 작업을 수행할 권한이 없습니다.")
	case errors.Is(err, workitems.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "api.moyro.work_item.idempotency_conflict", "같은 요청 키가 다른 작업에 사용되었습니다.")
	case errors.Is(err, workitems.ErrBlocked):
		writeError(w, http.StatusConflict, "api.moyro.work_item.blocked", "완료되지 않은 선행 작업이 있어 상태를 변경할 수 없습니다.")
	case errors.Is(err, workitems.ErrDependencyCycle):
		writeError(w, http.StatusConflict, "api.moyro.work_item.dependency_cycle", "작업 의존성이 순환 구조를 만들 수 없습니다.")
	case errors.Is(err, workitems.ErrTransition):
		writeError(w, http.StatusConflict, "api.moyro.work_item.transition", "허용되지 않은 업무 상태 전환입니다.")
	default:
		writeError(w, http.StatusInternalServerError, "api.moyro.work_item.app_error", "작업을 처리하지 못했습니다.")
	}
}

func (h *handlers) createNativeWorkItem(w http.ResponseWriter, r *http.Request) {
	if !requireRestrictedWorkPermission(w, r, h, rbac.PermissionMCPWrite) {
		return
	}
	if h.workItems == nil {
		writeError(w, http.StatusServiceUnavailable, "api.moyro.work_item.unavailable", "작업 관리 기능을 사용할 수 없습니다.")
		return
	}
	var request createNativeWorkItemRequest
	if err := decodeNativeWorkItemBody(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.work_item.bad_request", "요청 본문이 올바르지 않습니다.")
		return
	}
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if headerKey := strings.TrimSpace(r.Header.Get("Idempotency-Key")); headerKey != "" {
		if request.IdempotencyKey != "" && request.IdempotencyKey != headerKey {
			writeError(w, http.StatusBadRequest, "api.moyro.work_item.idempotency_mismatch", "요청 키가 서로 일치하지 않습니다.")
			return
		}
		request.IdempotencyKey = headerKey
	}
	item, replayed, err := h.workItems.CreateForPrincipal(r.Context(), requestPrincipal(r), workitems.CreateInput{
		Kind: request.Kind, Title: request.Title, Description: request.Description,
		AssigneeID: request.AssigneeID, SourcePostID: request.SourcePostID,
		DueAt: request.DueAt, IdempotencyKey: request.IdempotencyKey,
		Priority: request.Priority, ReviewerID: request.ReviewerID,
		InitialStatus: request.InitialStatus, RecurrenceUnit: request.RecurrenceUnit,
		RecurrenceInterval: request.RecurrenceInterval, SupersedesID: request.SupersedesID,
		DependencyIDs: request.DependencyIDs, ImpactTaskIDs: request.ImpactTaskIDs,
	})
	if err != nil {
		h.logger.Error("create native work item failed", "actor_id", userID(r), "kind", request.Kind, "err", err)
		writeNativeWorkItemError(w, err)
		return
	}
	if !replayed {
		h.audit.LogAsync(userID(r), audit.ActionWorkItemCreate, item.ID, map[string]any{
			"kind": item.Kind, "channel_id": item.ChannelID, "source_post_id": item.SourcePostID,
		})
		h.broadcastWorkItem(*item)
		h.emitAssignedWorkItem(r, *item)
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, createNativeWorkItemResponse{Item: *item, Replayed: replayed})
}

func (h *handlers) listNativeWorkItems(w http.ResponseWriter, r *http.Request) {
	if !requireRestrictedWorkPermission(w, r, h, rbac.PermissionMCPRead) {
		return
	}
	if h.workItems == nil {
		writeError(w, http.StatusServiceUnavailable, "api.moyro.work_item.unavailable", "작업 관리 기능을 사용할 수 없습니다.")
		return
	}
	options, err := parseNativeWorkItemListOptions(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.work_item.invalid_query", "목록 조회 조건이 올바르지 않습니다.")
		return
	}
	page, err := h.workItems.ListForPrincipal(r.Context(), requestPrincipal(r), options)
	if err != nil {
		h.logger.Error("list native work items failed", "actor_id", userID(r), "err", err)
		writeNativeWorkItemError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, page)
}

func (h *handlers) patchNativeWorkItem(w http.ResponseWriter, r *http.Request) {
	if !requireRestrictedWorkPermission(w, r, h, rbac.PermissionMCPWrite) {
		return
	}
	if h.workItems == nil {
		writeError(w, http.StatusServiceUnavailable, "api.moyro.work_item.unavailable", "작업 관리 기능을 사용할 수 없습니다.")
		return
	}
	var request patchNativeWorkItemRequest
	if err := decodeNativeWorkItemBody(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.work_item.bad_request", "요청 본문이 올바르지 않습니다.")
		return
	}
	item, err := h.workItems.PatchForPrincipal(r.Context(), requestPrincipal(r), chi.URLParam(r, "workItemID"), workitems.PatchInput{
		Title: request.Title, Description: request.Description, Status: request.Status,
		AssigneeID: request.AssigneeID, DueAt: request.DueAt, Priority: request.Priority,
		ReviewerID: request.ReviewerID, RecurrenceUnit: request.RecurrenceUnit,
		RecurrenceInterval: request.RecurrenceInterval,
	})
	if err != nil {
		h.logger.Error("patch native work item failed", "actor_id", userID(r), "work_item_id", chi.URLParam(r, "workItemID"), "err", err)
		writeNativeWorkItemError(w, err)
		return
	}
	h.audit.LogAsync(userID(r), audit.ActionWorkItemUpdate, item.ID, map[string]any{"kind": item.Kind, "status": item.Status})
	h.broadcastWorkItem(*item)
	if item.AssigneeChanged {
		h.emitAssignedWorkItem(r, *item)
	}
	if item.SpawnedItem != nil {
		h.broadcastWorkItem(*item.SpawnedItem)
		h.emitAssignedWorkItem(r, *item.SpawnedItem)
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *handlers) listNativeWorkItemEvents(w http.ResponseWriter, r *http.Request) {
	if !requireRestrictedWorkPermission(w, r, h, rbac.PermissionMCPRead) {
		return
	}
	if h.workItems == nil {
		writeError(w, http.StatusServiceUnavailable, "api.moyro.work_item.unavailable", "작업 관리 기능을 사용할 수 없습니다.")
		return
	}
	events, err := h.workItems.ListEventsForPrincipal(r.Context(), requestPrincipal(r), chi.URLParam(r, "workItemID"))
	if err != nil {
		writeNativeWorkItemError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, events)
}

func (h *handlers) mutateNativeWorkItemLink(w http.ResponseWriter, r *http.Request, relation, targetParam string, remove bool) {
	if !requireRestrictedWorkPermission(w, r, h, rbac.PermissionMCPWrite) {
		return
	}
	if h.workItems == nil {
		writeError(w, http.StatusServiceUnavailable, "api.moyro.work_item.unavailable", "작업 관리 기능을 사용할 수 없습니다.")
		return
	}
	var (
		item *workitems.Item
		err  error
	)
	if remove {
		item, err = h.workItems.RemoveLinkForPrincipal(r.Context(), requestPrincipal(r), chi.URLParam(r, "workItemID"), chi.URLParam(r, targetParam), relation)
	} else {
		item, err = h.workItems.AddLinkForPrincipal(r.Context(), requestPrincipal(r), chi.URLParam(r, "workItemID"), chi.URLParam(r, targetParam), relation)
	}
	if err != nil {
		writeNativeWorkItemError(w, err)
		return
	}
	h.audit.LogAsync(userID(r), audit.ActionWorkItemUpdate, item.ID, map[string]any{"relation": relation, "removed": remove})
	h.broadcastWorkItem(*item)
	writeJSON(w, http.StatusOK, item)
}

func (h *handlers) addNativeWorkItemDependency(w http.ResponseWriter, r *http.Request) {
	h.mutateNativeWorkItemLink(w, r, workitems.RelationDependsOn, "dependencyID", false)
}

func (h *handlers) removeNativeWorkItemDependency(w http.ResponseWriter, r *http.Request) {
	h.mutateNativeWorkItemLink(w, r, workitems.RelationDependsOn, "dependencyID", true)
}

func (h *handlers) addNativeWorkItemImpact(w http.ResponseWriter, r *http.Request) {
	h.mutateNativeWorkItemLink(w, r, workitems.RelationImpacts, "taskID", false)
}

func (h *handlers) removeNativeWorkItemImpact(w http.ResponseWriter, r *http.Request) {
	h.mutateNativeWorkItemLink(w, r, workitems.RelationImpacts, "taskID", true)
}

func (h *handlers) emitAssignedWorkItem(r *http.Request, item workitems.Item) {
	if h.activityEmit == nil || item.Kind != workitems.KindTask || item.AssigneeID == "" {
		return
	}
	_, err := h.activityEmit.Emit(r.Context(), activityevents.EmitInput{
		UserID: item.AssigneeID, Type: activityevents.TypeTaskAssigned,
		DedupeKey: item.ID + ":" + strconv.FormatInt(item.UpdateAt, 10),
		ActorID:   userID(r), TeamID: item.TeamID, ChannelID: item.ChannelID,
		PostID: item.SourcePostID, ResourceType: "work_item", ResourceID: item.ID,
		Title: "새 작업이 할당되었습니다", Summary: activityExcerpt(item.Title, 240),
	})
	if err != nil && h.logger != nil {
		h.logger.Warn("emit assigned work item activity", "work_item_id", item.ID, "assignee_id", item.AssigneeID, "err", err)
	}
}

func (h *handlers) deleteNativeWorkItem(w http.ResponseWriter, r *http.Request) {
	if !requireRestrictedWorkPermission(w, r, h, rbac.PermissionMCPWrite) {
		return
	}
	if h.workItems == nil {
		writeError(w, http.StatusServiceUnavailable, "api.moyro.work_item.unavailable", "작업 관리 기능을 사용할 수 없습니다.")
		return
	}
	id := chi.URLParam(r, "workItemID")
	item, err := h.workItems.DeleteForPrincipal(r.Context(), requestPrincipal(r), id)
	if err != nil {
		writeNativeWorkItemError(w, err)
		return
	}
	h.audit.LogAsync(userID(r), audit.ActionWorkItemDelete, id, nil)
	h.broadcastWorkItemDeleted(*item)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) broadcastWorkItem(item workitems.Item) {
	if item.Kind == workitems.KindDecision {
		h.hub.Broadcast(ws.Event{
			Event: "work_item_changed", Data: map[string]any{"work_item": item},
			Broadcast: ws.Broadcast{ChannelID: item.ChannelID},
		})
		return
	}
	h.hub.Broadcast(ws.Event{
		Event: "work_item_changed", Data: map[string]any{"work_item": item},
		Broadcast: ws.Broadcast{UserID: item.CreatedBy, ChannelID: item.ChannelID, TeamID: item.TeamID},
	})
	if item.AssigneeID != "" && item.AssigneeID != item.CreatedBy {
		h.hub.Broadcast(ws.Event{
			Event: "work_item_changed", Data: map[string]any{"work_item": item},
			Broadcast: ws.Broadcast{UserID: item.AssigneeID, ChannelID: item.ChannelID, TeamID: item.TeamID},
		})
	}
	if item.PreviousAssigneeID != "" && item.PreviousAssigneeID != item.CreatedBy && item.PreviousAssigneeID != item.AssigneeID {
		h.hub.Broadcast(ws.Event{
			Event: "work_item_deleted",
			Data: map[string]any{
				"id": item.ID, "kind": item.Kind, "channel_id": item.ChannelID,
			},
			Broadcast: ws.Broadcast{UserID: item.PreviousAssigneeID, ChannelID: item.ChannelID, TeamID: item.TeamID},
		})
	}
}

func (h *handlers) broadcastWorkItemDeleted(item workitems.Item) {
	data := map[string]any{"id": item.ID, "kind": item.Kind, "channel_id": item.ChannelID}
	if item.Kind == workitems.KindDecision {
		h.hub.Broadcast(ws.Event{
			Event: "work_item_deleted", Data: data,
			Broadcast: ws.Broadcast{ChannelID: item.ChannelID},
		})
		return
	}
	h.hub.Broadcast(ws.Event{
		Event: "work_item_deleted", Data: data,
		Broadcast: ws.Broadcast{UserID: item.CreatedBy, ChannelID: item.ChannelID, TeamID: item.TeamID},
	})
	if item.AssigneeID != "" && item.AssigneeID != item.CreatedBy {
		h.hub.Broadcast(ws.Event{
			Event: "work_item_deleted", Data: data,
			Broadcast: ws.Broadcast{UserID: item.AssigneeID, ChannelID: item.ChannelID, TeamID: item.TeamID},
		})
	}
}
