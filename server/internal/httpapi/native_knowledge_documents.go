package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/documents"
	"github.com/hkjang/moyro/server/internal/knowledge"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/ws"
)

// authorizeNativeKnowledgePermission keeps browser sessions on their normal
// membership boundary while requiring restricted API keys to have both the
// explicit credential grant and the owner's current RBAC permission. Resource
// allow-lists are intersected again by the domain services on every query.
func (h *handlers) authorizeNativeKnowledgePermission(w http.ResponseWriter, r *http.Request, permission string) bool {
	principal := requestPrincipal(r)
	if !principal.Restricted {
		return true
	}
	if _, granted := principal.GrantedPermissions[permission]; !granted {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "missing permission: "+permission)
		return false
	}
	if h.native == nil || h.native.rbac == nil {
		writeError(w, http.StatusServiceUnavailable, "api.moyro.disabled", "moyro management services are unavailable")
		return false
	}
	allowed, err := h.native.rbac.Allowed(r.Context(), principal, permission, rbac.Scope{})
	if err != nil {
		if h.logger != nil {
			h.logger.Error("knowledge permission check failed", "actor_id", principal.UserID, "permission", permission, "err", err)
		}
		writeError(w, http.StatusInternalServerError, "api.moyro.knowledge.authorization", "knowledge authorization failed")
		return false
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "missing permission: "+permission)
		return false
	}
	return true
}

func decodeNativeKnowledgeJSON(w http.ResponseWriter, r *http.Request, limit int64, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
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

type nativeKnowledgeSearchRequest struct {
	Query     string `json:"query"`
	TeamID    string `json:"team_id"`
	ChannelID string `json:"channel_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

func (h *handlers) searchNativeKnowledge(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeNativeKnowledgePermission(w, r, rbac.PermissionMCPRead) {
		return
	}
	var request nativeKnowledgeSearchRequest
	if err := decodeNativeKnowledgeJSON(w, r, 64<<10, &request); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.knowledge.bad_request", "검색 요청이 올바르지 않습니다.")
		return
	}
	result, err := knowledge.New(h.auth.DB()).Search(r.Context(), requestPrincipal(r), knowledge.SearchInput{
		Query: request.Query, TeamID: request.TeamID, ChannelID: request.ChannelID, Limit: request.Limit,
	})
	if err != nil {
		switch {
		case errors.Is(err, knowledge.ErrInvalid):
			writeError(w, http.StatusBadRequest, "api.moyro.knowledge.invalid", "검색어 또는 검색 범위가 올바르지 않습니다.")
		case errors.Is(err, knowledge.ErrNotFound):
			writeError(w, http.StatusNotFound, "api.moyro.knowledge.not_found", "검색 범위를 찾을 수 없습니다.")
		default:
			if h.logger != nil {
				h.logger.Error("knowledge search failed", "actor_id", userID(r), "team_id", request.TeamID, "err", err)
			}
			writeError(w, http.StatusInternalServerError, "api.moyro.knowledge.app_error", "지식 검색을 완료하지 못했습니다.")
		}
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if h.audit != nil {
		h.audit.LogAsync(userID(r), "knowledge.search", request.TeamID, map[string]any{
			"channel_scoped": strings.TrimSpace(request.ChannelID) != "", "result_count": len(result.Sources),
		})
	}
	writeJSON(w, http.StatusOK, result)
}

type nativeDocumentCreateRequest struct {
	Title          string `json:"title"`
	Body           string `json:"body"`
	SourcePostID   string `json:"source_post_id"`
	SourceCursorAt int64  `json:"source_cursor_at"`
	IdempotencyKey string `json:"idempotency_key"`
}

type nativeDocumentCreateResponse struct {
	Document documents.Document `json:"document"`
	Replayed bool               `json:"replayed"`
}

type nativeDocumentPatchRequest struct {
	Title            *string `json:"title,omitempty"`
	Body             *string `json:"body,omitempty"`
	SourceCursorAt   *int64  `json:"source_cursor_at,omitempty"`
	ExpectedRevision int64   `json:"expected_revision"`
}

func writeNativeDocumentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, documents.ErrInvalid):
		writeError(w, http.StatusBadRequest, "api.moyro.document.invalid", "문서 요청이 올바르지 않습니다.")
	case errors.Is(err, documents.ErrNotFound):
		writeError(w, http.StatusNotFound, "api.moyro.document.not_found", "문서 또는 원본 대화를 찾을 수 없습니다.")
	case errors.Is(err, documents.ErrForbidden):
		writeError(w, http.StatusForbidden, "api.moyro.document.forbidden", "이 문서를 변경할 권한이 없습니다.")
	case errors.Is(err, documents.ErrRevisionConflict):
		writeError(w, http.StatusConflict, "api.moyro.document.revision_conflict", "문서가 다른 곳에서 변경되었습니다. 최신 문서를 다시 불러오세요.")
	case errors.Is(err, documents.ErrSourceChanged):
		writeError(w, http.StatusConflict, "api.moyro.document.source_changed", "문서 생성 중 원본 대화가 변경되었습니다. 최신 대화로 다시 생성하세요.")
	case errors.Is(err, documents.ErrSourceTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "api.moyro.document.source_too_large", "원본 대화가 너무 커서 문서로 변환할 수 없습니다.")
	case errors.Is(err, documents.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "api.moyro.document.idempotency_conflict", "같은 요청 키가 다른 문서에 사용되었습니다.")
	default:
		writeError(w, http.StatusInternalServerError, "api.moyro.document.app_error", "문서를 처리하지 못했습니다.")
	}
}

func (h *handlers) listNativeDocuments(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeNativeKnowledgePermission(w, r, rbac.PermissionMCPRead) {
		return
	}
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeNativeDocumentError(w, documents.ErrInvalid)
			return
		}
		limit = parsed
	}
	list, err := documents.New(h.auth.DB()).List(r.Context(), requestPrincipal(r), documents.ListOptions{Limit: limit})
	if err != nil {
		writeNativeDocumentError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, list)
}

func (h *handlers) getNativeDocument(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeNativeKnowledgePermission(w, r, rbac.PermissionMCPRead) {
		return
	}
	document, err := documents.New(h.auth.DB()).Get(r.Context(), requestPrincipal(r), chi.URLParam(r, "documentID"))
	if err != nil {
		writeNativeDocumentError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, document)
}

// getNativeDocumentSource is usable both before document creation and during
// a stale-document refresh. postID may identify either the root or a reply;
// the service canonicalizes it to the live root thread.
func (h *handlers) getNativeDocumentSource(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeNativeKnowledgePermission(w, r, rbac.PermissionMCPRead) {
		return
	}
	source, err := documents.New(h.auth.DB()).Source(r.Context(), requestPrincipal(r), chi.URLParam(r, "postID"))
	if err != nil {
		writeNativeDocumentError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, source)
}

func (h *handlers) createNativeDocument(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeNativeKnowledgePermission(w, r, rbac.PermissionMCPWrite) {
		return
	}
	var request nativeDocumentCreateRequest
	if err := decodeNativeKnowledgeJSON(w, r, 1<<20, &request); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.document.bad_request", "문서 요청 본문이 올바르지 않습니다.")
		return
	}
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if headerKey := strings.TrimSpace(r.Header.Get("Idempotency-Key")); headerKey != "" {
		if request.IdempotencyKey != "" && request.IdempotencyKey != headerKey {
			writeError(w, http.StatusBadRequest, "api.moyro.document.idempotency_mismatch", "요청 키가 서로 일치하지 않습니다.")
			return
		}
		request.IdempotencyKey = headerKey
	}
	document, replayed, err := documents.New(h.auth.DB()).Create(r.Context(), requestPrincipal(r), documents.CreateInput{
		Title: request.Title, Body: request.Body, SourcePostID: request.SourcePostID,
		SourceCursorAt: request.SourceCursorAt, IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		if h.logger != nil {
			h.logger.Error("create native document failed", "actor_id", userID(r), "err", err)
		}
		writeNativeDocumentError(w, err)
		return
	}
	if !replayed {
		h.documentChanged(userID(r), "document.create", document)
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, nativeDocumentCreateResponse{Document: *document, Replayed: replayed})
}

func (h *handlers) patchNativeDocument(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeNativeKnowledgePermission(w, r, rbac.PermissionMCPWrite) {
		return
	}
	var request nativeDocumentPatchRequest
	if err := decodeNativeKnowledgeJSON(w, r, 1<<20, &request); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.document.bad_request", "문서 요청 본문이 올바르지 않습니다.")
		return
	}
	document, err := documents.New(h.auth.DB()).Patch(r.Context(), requestPrincipal(r), chi.URLParam(r, "documentID"), documents.PatchInput{
		Title: request.Title, Body: request.Body, SourceCursorAt: request.SourceCursorAt,
		ExpectedRevision: request.ExpectedRevision,
	})
	if err != nil {
		writeNativeDocumentError(w, err)
		return
	}
	h.documentChanged(userID(r), "document.update", document)
	writeJSON(w, http.StatusOK, document)
}

func (h *handlers) deleteNativeDocument(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeNativeKnowledgePermission(w, r, rbac.PermissionMCPWrite) {
		return
	}
	revision, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("revision")), 10, 64)
	if err != nil || revision <= 0 {
		writeNativeDocumentError(w, documents.ErrInvalid)
		return
	}
	document, err := documents.New(h.auth.DB()).Delete(r.Context(), requestPrincipal(r), chi.URLParam(r, "documentID"), revision)
	if err != nil {
		writeNativeDocumentError(w, err)
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), "document.delete", document.ID, map[string]any{
			"channel_id": document.ChannelID, "revision": document.Revision,
		})
	}
	if h.hub != nil {
		h.hub.Broadcast(ws.Event{
			Event:     "document_deleted",
			Data:      map[string]any{"id": document.ID, "channel_id": document.ChannelID, "revision": document.Revision},
			Broadcast: ws.Broadcast{ChannelID: document.ChannelID, TeamID: document.TeamID},
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) documentChanged(actor, action string, document *documents.Document) {
	if document == nil {
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(actor, action, document.ID, map[string]any{
			"channel_id": document.ChannelID, "revision": document.Revision,
		})
	}
	if h.hub != nil {
		h.hub.Broadcast(ws.Event{
			Event: "document_changed",
			Data: map[string]any{
				"document": map[string]any{
					"id": document.ID, "channel_id": document.ChannelID,
					"revision": document.Revision, "stale": document.Stale,
				},
			},
			Broadcast: ws.Broadcast{ChannelID: document.ChannelID, TeamID: document.TeamID},
		})
	}
}
