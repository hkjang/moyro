package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/activityevents"
	"github.com/hkjang/moyro/server/internal/rbac"
)

type activityEventBackendStub struct {
	page activityevents.Page
	err  error

	listPrincipal rbac.Principal
	listOptions   activityevents.ListOptions

	updated         *activityevents.Event
	updatePrincipal rbac.Principal
	updateEventID   string
	updatePatch     activityevents.StatePatch

	markCount     int64
	markPrincipal rbac.Principal
	markIDs       []string
}

func (s *activityEventBackendStub) ListForPrincipal(_ context.Context, principal rbac.Principal, options activityevents.ListOptions) (activityevents.Page, error) {
	s.listPrincipal, s.listOptions = principal, options
	return s.page, s.err
}

func (s *activityEventBackendStub) UpdateStateForPrincipal(_ context.Context, principal rbac.Principal, eventID string, patch activityevents.StatePatch) (*activityevents.Event, error) {
	s.updatePrincipal, s.updateEventID, s.updatePatch = principal, eventID, patch
	if s.err != nil {
		return nil, s.err
	}
	return s.updated, nil
}

func (s *activityEventBackendStub) MarkReadForPrincipal(_ context.Context, principal rbac.Principal, eventIDs []string) (int64, error) {
	s.markPrincipal, s.markIDs = principal, append([]string(nil), eventIDs...)
	return s.markCount, s.err
}

func TestListNativeActivityEventsUsesOwnerAndSafeResponseAllowlist(t *testing.T) {
	backend := &activityEventBackendStub{page: activityevents.Page{
		Events: []activityevents.Event{{
			ID: "event-1", UserID: "owner-private", Type: activityevents.TypeMention,
			ActorID: "actor-1", TeamID: "team-1", ChannelID: "channel-1", PostID: "post-1",
			ResourceType: "post", ResourceID: "post-1", Title: "새 멘션", Summary: "표시 가능한 요약",
			DedupeKey: "_moyro_credential_id=credential-private",
			CreateAt:  1, UpdateAt: 2, ReadAt: 3, CompletedAt: 4, SnoozedUntil: 5,
		}},
		NextCursor: "next-page",
	}}
	h := &handlers{activity: backend}
	request := requestWithActivityUser(http.MethodGet,
		"/api/moyro/v1/me/activity-events?limit=7&unread=true&type=mention&type=plugin_event&cursor=cursor-1", nil)
	recorder := httptest.NewRecorder()

	h.listNativeActivityEvents(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list response = %d: %s", recorder.Code, recorder.Body.String())
	}
	if backend.listPrincipal.UserID != "user-1" || backend.listPrincipal.Restricted || backend.listOptions.Limit != 7 || !backend.listOptions.UnreadOnly || backend.listOptions.Cursor != "cursor-1" {
		t.Fatalf("list scope/options = principal %#v, %#v", backend.listPrincipal, backend.listOptions)
	}
	if !reflect.DeepEqual(backend.listOptions.Types, []activityevents.EventType{activityevents.TypeMention, activityevents.TypePluginEvent}) {
		t.Fatalf("list types = %#v", backend.listOptions.Types)
	}

	body := recorder.Body.String()
	for _, forbidden := range []string{
		"owner-private", "credential-private", "_moyro_credential_id", "dedupe_key", "user_id", "payload",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("activity response contains %q: %s", forbidden, body)
		}
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	assertJSONKeys(t, decoded, []string{"events", "next_cursor"})
	var events []map[string]json.RawMessage
	if err := json.Unmarshal(decoded["events"], &events); err != nil || len(events) != 1 {
		t.Fatalf("decode response events = %#v, %v", events, err)
	}
	assertJSONKeys(t, events[0], []string{
		"id", "type", "actor_id", "team_id", "channel_id", "post_id",
		"resource_type", "resource_id", "title", "summary", "create_at", "update_at",
		"read_at", "completed_at", "snoozed_until",
	})
}

func TestNativeActivityStateAndBulkReadMutationsStayOwnerScoped(t *testing.T) {
	backend := &activityEventBackendStub{
		updated: &activityevents.Event{
			ID: "event-1", UserID: "owner-private", Type: activityevents.TypeTaskAssigned,
			Title: "할 일 배정", DedupeKey: "dedupe-private", CreateAt: 1, UpdateAt: 2, ReadAt: 2,
		},
		markCount: 2,
	}
	h := &handlers{activity: backend}
	patchRequest := requestWithActivityUser(http.MethodPatch, "/api/moyro/v1/me/activity-events/event-1",
		[]byte(`{"read":true,"completed":false,"snoozed_until":5000}`))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("eventID", "event-1")
	patchRequest = patchRequest.WithContext(context.WithValue(patchRequest.Context(), chi.RouteCtxKey, routeContext))
	patchRecorder := httptest.NewRecorder()

	h.patchNativeActivityEvent(patchRecorder, patchRequest)
	if patchRecorder.Code != http.StatusOK {
		t.Fatalf("patch response = %d: %s", patchRecorder.Code, patchRecorder.Body.String())
	}
	if backend.updatePrincipal.UserID != "user-1" || backend.updatePrincipal.Restricted || backend.updateEventID != "event-1" ||
		backend.updatePatch.Read == nil || !*backend.updatePatch.Read ||
		backend.updatePatch.Completed == nil || *backend.updatePatch.Completed ||
		backend.updatePatch.SnoozedUntil == nil || *backend.updatePatch.SnoozedUntil != 5000 {
		t.Fatalf("update scope/patch = principal %#v event %q patch %#v", backend.updatePrincipal, backend.updateEventID, backend.updatePatch)
	}
	if strings.Contains(patchRecorder.Body.String(), "owner-private") || strings.Contains(patchRecorder.Body.String(), "dedupe-private") {
		t.Fatalf("patch response leaked private fields: %s", patchRecorder.Body.String())
	}

	bulkRequest := requestWithActivityUser(http.MethodPost, "/api/moyro/v1/me/activity-events/mark-read",
		[]byte(`{"ids":["event-1","event-2"]}`))
	bulkRecorder := httptest.NewRecorder()
	h.markNativeActivityEventsRead(bulkRecorder, bulkRequest)
	if bulkRecorder.Code != http.StatusOK || strings.TrimSpace(bulkRecorder.Body.String()) != `{"updated":2}` {
		t.Fatalf("bulk response = %d: %s", bulkRecorder.Code, bulkRecorder.Body.String())
	}
	if backend.markPrincipal.UserID != "user-1" || backend.markPrincipal.Restricted || !reflect.DeepEqual(backend.markIDs, []string{"event-1", "event-2"}) {
		t.Fatalf("bulk scope = principal %#v ids %#v", backend.markPrincipal, backend.markIDs)
	}
}

func TestNativeActivityHandlersPropagateCredentialResourceConstraints(t *testing.T) {
	principal := rbac.Principal{
		UserID:            "user-1",
		CredentialID:      "key-1",
		Restricted:        true,
		AllowedTeamIDs:    map[string]struct{}{"team-1": {}},
		AllowedChannelIDs: map[string]struct{}{"channel-1": {}},
	}
	backend := &activityEventBackendStub{
		updated:   &activityevents.Event{ID: "event-1", Type: activityevents.TypeMention, Title: "알림"},
		markCount: 1,
	}
	h := &handlers{activity: backend}

	listRequest := requestWithActivityPrincipal(http.MethodGet, "/api/moyro/v1/me/activity-events", nil, principal)
	h.listNativeActivityEvents(httptest.NewRecorder(), listRequest)
	if !reflect.DeepEqual(backend.listPrincipal, principal) {
		t.Fatalf("list principal = %#v, want %#v", backend.listPrincipal, principal)
	}

	patchRequest := requestWithActivityPrincipal(http.MethodPatch, "/api/moyro/v1/me/activity-events/event-1", []byte(`{"read":true}`), principal)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("eventID", "event-1")
	patchRequest = patchRequest.WithContext(context.WithValue(patchRequest.Context(), chi.RouteCtxKey, routeContext))
	h.patchNativeActivityEvent(httptest.NewRecorder(), patchRequest)
	if !reflect.DeepEqual(backend.updatePrincipal, principal) {
		t.Fatalf("update principal = %#v, want %#v", backend.updatePrincipal, principal)
	}

	markRequest := requestWithActivityPrincipal(http.MethodPost, "/api/moyro/v1/me/activity-events/mark-read", []byte(`{"ids":["event-1"]}`), principal)
	h.markNativeActivityEventsRead(httptest.NewRecorder(), markRequest)
	if !reflect.DeepEqual(backend.markPrincipal, principal) {
		t.Fatalf("mark-read principal = %#v, want %#v", backend.markPrincipal, principal)
	}
}

func TestActivityListOptionsRejectsUnknownFilters(t *testing.T) {
	t.Parallel()
	for _, target := range []string{
		"/activity?limit=0",
		"/activity?limit=101",
		"/activity?unread=sometimes",
		"/activity?type=credential_rotated",
	} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if _, err := activityListOptions(req); err == nil {
			t.Fatalf("activityListOptions(%q) accepted invalid filter", target)
		}
	}
}

func requestWithActivityUser(method, target string, body []byte) *http.Request {
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	return request.WithContext(context.WithValue(request.Context(), userIDKey, "user-1"))
}

func requestWithActivityPrincipal(method, target string, body []byte, principal rbac.Principal) *http.Request {
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	return request.WithContext(setPrincipalOnContext(request.Context(), principal))
}

func assertJSONKeys(t *testing.T, object map[string]json.RawMessage, want []string) {
	t.Helper()
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON keys = %#v, want %#v", got, want)
	}
}
