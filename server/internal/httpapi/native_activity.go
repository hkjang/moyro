package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/activityevents"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/ws"
)

type activityEventBackend interface {
	ListForPrincipal(context.Context, rbac.Principal, activityevents.ListOptions) (activityevents.Page, error)
	UpdateStateForPrincipal(context.Context, rbac.Principal, string, activityevents.StatePatch) (*activityevents.Event, error)
	MarkReadForPrincipal(context.Context, rbac.Principal, []string) (int64, error)
}

// activityEventView is the only activity-event shape written to HTTP. Owner
// IDs, dedupe keys, source payloads and producer-private metadata cannot enter
// the response by accidentally adding a field to activityevents.Event.
type activityEventView struct {
	ID           string                   `json:"id"`
	Type         activityevents.EventType `json:"type"`
	ActorID      string                   `json:"actor_id,omitempty"`
	TeamID       string                   `json:"team_id,omitempty"`
	ChannelID    string                   `json:"channel_id,omitempty"`
	PostID       string                   `json:"post_id,omitempty"`
	ResourceType string                   `json:"resource_type,omitempty"`
	ResourceID   string                   `json:"resource_id,omitempty"`
	Title        string                   `json:"title"`
	Summary      string                   `json:"summary,omitempty"`
	CreateAt     int64                    `json:"create_at"`
	UpdateAt     int64                    `json:"update_at"`
	ReadAt       int64                    `json:"read_at"`
	CompletedAt  int64                    `json:"completed_at"`
	SnoozedUntil int64                    `json:"snoozed_until"`
}

type activityEventPageView struct {
	Events     []activityEventView `json:"events"`
	NextCursor string              `json:"next_cursor"`
}

func activityEventResponse(event activityevents.Event) activityEventView {
	return activityEventView{
		ID: event.ID, Type: event.Type, ActorID: event.ActorID,
		TeamID: event.TeamID, ChannelID: event.ChannelID, PostID: event.PostID,
		ResourceType: event.ResourceType, ResourceID: event.ResourceID,
		Title: event.Title, Summary: event.Summary,
		CreateAt: event.CreateAt, UpdateAt: event.UpdateAt,
		ReadAt: event.ReadAt, CompletedAt: event.CompletedAt,
		SnoozedUntil: event.SnoozedUntil,
	}
}

func activityEventPageResponse(page activityevents.Page) activityEventPageView {
	events := make([]activityEventView, 0, len(page.Events))
	for _, event := range page.Events {
		events = append(events, activityEventResponse(event))
	}
	return activityEventPageView{Events: events, NextCursor: page.NextCursor}
}

func (h *handlers) listNativeActivityEvents(w http.ResponseWriter, r *http.Request) {
	if h.activity == nil {
		writeError(w, http.StatusServiceUnavailable, "api.moyro.activity.unavailable", "activity events are unavailable")
		return
	}
	options, err := activityListOptions(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.activity.query", err.Error())
		return
	}
	page, err := h.activity.ListForPrincipal(r.Context(), requestPrincipal(r), options)
	if err != nil {
		if errors.Is(err, activityevents.ErrInvalid) || errors.Is(err, activityevents.ErrInvalidCursor) {
			writeError(w, http.StatusBadRequest, "api.moyro.activity.query", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "api.moyro.activity.list", "activity events are unavailable")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, activityEventPageResponse(page))
}

func (h *handlers) patchNativeActivityEvent(w http.ResponseWriter, r *http.Request) {
	if h.activity == nil {
		writeError(w, http.StatusServiceUnavailable, "api.moyro.activity.unavailable", "activity events are unavailable")
		return
	}
	var input struct {
		Read         *bool  `json:"read,omitempty"`
		Completed    *bool  `json:"completed,omitempty"`
		SnoozedUntil *int64 `json:"snoozed_until,omitempty"`
	}
	if err := decodeActivityBody(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.activity.body", err.Error())
		return
	}
	event, err := h.activity.UpdateStateForPrincipal(r.Context(), requestPrincipal(r), chi.URLParam(r, "eventID"), activityevents.StatePatch{
		Read: input.Read, Completed: input.Completed, SnoozedUntil: input.SnoozedUntil,
	})
	if err != nil {
		switch {
		case errors.Is(err, activityevents.ErrNotFound):
			writeError(w, http.StatusNotFound, "api.moyro.activity.not_found", "activity event not found")
		case errors.Is(err, activityevents.ErrInvalid):
			writeError(w, http.StatusBadRequest, "api.moyro.activity.state", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "api.moyro.activity.state", "activity event state could not be updated")
		}
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if h.hub != nil {
		h.hub.Broadcast(ws.Event{
			Event:     "activity_state_changed",
			Data:      map[string]any{"event": activityEventResponse(*event)},
			Broadcast: ws.Broadcast{
				UserID: userID(r), TeamID: event.TeamID, ChannelID: event.ChannelID,
			},
		})
	}
	writeJSON(w, http.StatusOK, activityEventResponse(*event))
}

func (h *handlers) markNativeActivityEventsRead(w http.ResponseWriter, r *http.Request) {
	if h.activity == nil {
		writeError(w, http.StatusServiceUnavailable, "api.moyro.activity.unavailable", "activity events are unavailable")
		return
	}
	var input struct {
		IDs []string `json:"ids"`
	}
	if err := decodeActivityBody(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.activity.body", err.Error())
		return
	}
	updated, err := h.activity.MarkReadForPrincipal(r.Context(), requestPrincipal(r), input.IDs)
	if err != nil {
		if errors.Is(err, activityevents.ErrInvalid) {
			writeError(w, http.StatusBadRequest, "api.moyro.activity.mark_read", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "api.moyro.activity.mark_read", "activity events could not be marked read")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if h.hub != nil {
		h.hub.Broadcast(ws.Event{
			Event:     "activity_state_changed",
			Data:      map[string]any{"updated": updated},
			Broadcast: ws.Broadcast{UserID: userID(r)},
		})
	}
	writeJSON(w, http.StatusOK, map[string]int64{"updated": updated})
}

func activityListOptions(r *http.Request) (activityevents.ListOptions, error) {
	query := r.URL.Query()
	options := activityevents.ListOptions{
		Cursor: strings.TrimSpace(query.Get("cursor")),
		Limit:  activityevents.DefaultPageSize,
	}
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > activityevents.MaxPageSize {
			return activityevents.ListOptions{}, errors.New("limit must be between 1 and 100")
		}
		options.Limit = limit
	}
	if raw := strings.TrimSpace(query.Get("unread")); raw != "" {
		unread, err := strconv.ParseBool(raw)
		if err != nil {
			return activityevents.ListOptions{}, errors.New("unread must be true or false")
		}
		options.UnreadOnly = unread
	}
	for _, raw := range query["type"] {
		for _, item := range strings.Split(raw, ",") {
			typ, err := activityevents.ParseEventType(item)
			if err != nil {
				return activityevents.ListOptions{}, err
			}
			options.Types = append(options.Types, typ)
		}
	}
	return options, nil
}

func decodeActivityBody(w http.ResponseWriter, r *http.Request, target any) error {
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
