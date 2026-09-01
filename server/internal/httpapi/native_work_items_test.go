package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/activityevents"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/workitems"
	"github.com/hkjang/moyro/server/internal/ws"
)

func TestWorkManagementHandlersRejectRestrictedKeysWithoutExactGrant(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, method, path string
		handle             func(*handlers, http.ResponseWriter, *http.Request)
	}{
		{name: "list needs read", method: http.MethodGet, path: "/api/moyro/v1/me/work-items", handle: (*handlers).listNativeWorkItems},
		{name: "create needs write", method: http.MethodPost, path: "/api/moyro/v1/me/work-items", handle: (*handlers).createNativeWorkItem},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(test.method, test.path, nil)
			request = request.WithContext(setPrincipalOnContext(request.Context(), rbac.Principal{
				UserID: "user-1", CredentialID: "key-1", Restricted: true,
				GrantedPermissions: map[string]struct{}{rbac.PermissionUseAI: {}},
			}))
			response := httptest.NewRecorder()
			test.handle(&handlers{}, response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
			}
		})
	}
}

func TestDecodeNativeWorkItemBodyIsStrictAndSupportsDomainSizedUTF8(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "valid", body: `{"kind":"task","title":"확인","source_post_id":"post-1","idempotency_key":"key-1"}`},
		{name: "unicode description at domain limit", body: `{"description":"` + strings.Repeat("😀", 10_000) + `"}`},
		{name: "unknown field", body: `{"title":"확인","secret":"no"}`, wantErr: true},
		{name: "trailing object", body: `{"title":"확인"}{}`, wantErr: true},
		{name: "empty", body: ``, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest("POST", "/api/moyro/v1/me/work-items", strings.NewReader(test.body))
			response := httptest.NewRecorder()
			var decoded createNativeWorkItemRequest
			err := decodeNativeWorkItemBody(response, request, &decoded)
			if (err != nil) != test.wantErr {
				t.Fatalf("decode error = %v, wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func TestParseNativeWorkItemListOptionsRejectsAmbiguousPageSizes(t *testing.T) {
	t.Parallel()
	valid, err := parseNativeWorkItemListOptions(url.Values{
		"kind": {" task "}, "status": {" open "}, "cursor": {" cursor "}, "per_page": {"100"},
	})
	if err != nil {
		t.Fatalf("parse valid query: %v", err)
	}
	if valid.Kind != workitems.KindTask || valid.Status != workitems.StatusOpen || valid.Cursor != "cursor" || valid.PageSize != 100 {
		t.Fatalf("parsed options = %#v", valid)
	}
	for _, raw := range []string{"garbage", "0", "-1", "101", "1.5"} {
		if _, err := parseNativeWorkItemListOptions(url.Values{"per_page": {raw}}); err == nil {
			t.Fatalf("per_page=%q unexpectedly accepted", raw)
		}
	}
}

func TestEmitAssignedWorkItemUsesRecipientScopedSafeActivity(t *testing.T) {
	t.Parallel()
	emitter := &recordingActivityEmitter{}
	h := &handlers{activityEmit: emitter}
	request := requestWithActivityUser(http.MethodPost, "/api/moyro/v1/me/work-items", nil)
	item := workitems.Item{
		ID: "work-1", Kind: workitems.KindTask, Title: "  운영 점검\n담당 확인  ",
		Description: "activity event에 포함하면 안 되는 상세 정보 secret-token",
		CreatedBy:   "user-1", AssigneeID: "user-2", TeamID: "team-1", ChannelID: "channel-1",
		SourcePostID: "post-1", UpdateAt: 1234,
	}

	h.emitAssignedWorkItem(request, item)
	if len(emitter.inputs) != 1 {
		t.Fatalf("activity inputs = %#v", emitter.inputs)
	}
	input := emitter.inputs[0]
	if input.UserID != "user-2" || input.ActorID != "user-1" || input.Type != activityevents.TypeTaskAssigned ||
		input.DedupeKey != "work-1:1234" || input.ResourceType != "work_item" || input.ResourceID != "work-1" ||
		input.PostID != "post-1" || input.Summary != "운영 점검 담당 확인" {
		t.Fatalf("activity input = %#v", input)
	}
	if strings.Contains(input.Summary, "secret-token") {
		t.Fatalf("activity summary leaked description: %#v", input)
	}

	h.emitAssignedWorkItem(request, workitems.Item{Kind: workitems.KindDecision, AssigneeID: "user-2"})
	h.emitAssignedWorkItem(request, workitems.Item{Kind: workitems.KindTask})
	if len(emitter.inputs) != 1 {
		t.Fatalf("non-assignment emitted activity: %#v", emitter.inputs)
	}
}

func TestBroadcastWorkItemIntersectsRecipientsWithCurrentChannelMembership(t *testing.T) {
	hub := ws.NewHub()
	var assigneeMember atomic.Bool
	assigneeMember.Store(true)
	resolved := make(chan struct{}, 8)
	hub.SetAudienceResolver(func(_ context.Context, _ ws.Broadcast) (map[string]struct{}, error) {
		audience := map[string]struct{}{"creator": {}}
		if assigneeMember.Load() {
			audience["assignee"] = struct{}{}
		}
		resolved <- struct{}{}
		return audience, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hub.Run(ctx)
	creator := &ws.Client{UserID: "creator", Send: make(chan []byte, 4)}
	assignee := &ws.Client{UserID: "assignee", Send: make(chan []byte, 4)}
	hub.Register(creator)
	hub.Register(assignee)
	deadline := time.Now().Add(time.Second)
	for hub.ClientCount() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if hub.ClientCount() != 2 {
		t.Fatalf("registered websocket clients = %d", hub.ClientCount())
	}

	h := &handlers{hub: hub}
	item := workitems.Item{
		ID: "work-1", Kind: workitems.KindTask, CreatedBy: "creator", AssigneeID: "assignee",
		TeamID: "team-1", ChannelID: "channel-1",
	}
	h.broadcastWorkItem(item)
	for range 2 {
		select {
		case <-resolved:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for live audience resolution")
		}
	}
	assertWorkItemEvent := func(name string, client *ws.Client) {
		t.Helper()
		select {
		case raw := <-client.Send:
			var event ws.Event
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatalf("decode %s event: %v", name, err)
			}
			if event.Event != "work_item_changed" || event.Broadcast.ChannelID != "channel-1" || event.Broadcast.TeamID != "team-1" {
				t.Fatalf("%s event = %#v", name, event)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s event", name)
		}
	}
	assertWorkItemEvent("creator", creator)
	assertWorkItemEvent("assignee", assignee)

	assigneeMember.Store(false)
	h.broadcastWorkItem(item)
	for range 2 {
		select {
		case <-resolved:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for revoked audience resolution")
		}
	}
	assertWorkItemEvent("creator after assignee revocation", creator)
	select {
	case raw := <-assignee.Send:
		t.Fatalf("revoked assignee received work-item event: %s", raw)
	case <-time.After(100 * time.Millisecond):
	}
}
