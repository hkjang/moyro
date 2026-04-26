package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func requestWithRouteParams(method, path string, params map[string]string, body []byte) *http.Request {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	routeCtx := chi.NewRouteContext()
	for key, value := range params {
		routeCtx.URLParams.Add(key, value)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

func TestEnterpriseCompatDialogSubmit(t *testing.T) {
	h := &handlers{}
	req := httptest.NewRequest(http.MethodPost, "/api/v4/actions/dialogs/submit", bytes.NewBufferString(`{"submission":{}}`))
	rr := httptest.NewRecorder()

	h.submitDialog(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		Status string            `json:"status"`
		Errors map[string]string `json:"errors"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Status != "OK" || len(got.Errors) != 0 {
		t.Fatalf("submit response = %#v, want OK with no field errors", got)
	}
}

func TestEnterpriseCompatContentFlaggingPostKeepsRouteID(t *testing.T) {
	h := &handlers{}
	req := requestWithRouteParam(http.MethodGet, "/api/v4/content_flagging/post/post-1", "postID", "post-1", nil)
	rr := httptest.NewRecorder()

	h.getContentFlaggingPost(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["post_id"] != "post-1" || got["status"] != "none" {
		t.Fatalf("content flagging post = %#v", got)
	}
}

func TestEnterpriseCompatDataRetentionCreateAssignsID(t *testing.T) {
	h := &handlers{}
	req := httptest.NewRequest(http.MethodPost, "/api/v4/data_retention/policies", bytes.NewBufferString(`{"display_name":"Finance"}`))
	rr := httptest.NewRecorder()

	h.createDataRetentionPolicy(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["id"] == "" || got["display_name"] != "Finance" {
		t.Fatalf("created policy = %#v", got)
	}
}

func TestEnterpriseCompatRemoteClusterAndPropertyValueRouteIDs(t *testing.T) {
	h := &handlers{}

	remoteReq := requestWithRouteParam(http.MethodGet, "/api/v4/remotecluster/remote-1", "remoteID", "remote-1", nil)
	remoteRR := httptest.NewRecorder()
	h.getRemoteCluster(remoteRR, remoteReq)
	if remoteRR.Code != http.StatusOK {
		t.Fatalf("remote status = %d, want 200; body=%s", remoteRR.Code, remoteRR.Body.String())
	}
	var remote map[string]any
	if err := json.NewDecoder(remoteRR.Body).Decode(&remote); err != nil {
		t.Fatalf("decode remote: %v", err)
	}
	if remote["id"] != "remote-1" {
		t.Fatalf("remote id = %#v, want route id", remote["id"])
	}

	valueReq := requestWithRouteParams(
		http.MethodGet,
		"/api/v4/properties/groups/group-a/target-b/values/field-c",
		map[string]string{"groupID": "group-a", "targetID": "target-b", "fieldID": "field-c"},
		nil,
	)
	valueRR := httptest.NewRecorder()
	h.getPropertyValue(valueRR, valueReq)
	if valueRR.Code != http.StatusOK {
		t.Fatalf("property status = %d, want 200; body=%s", valueRR.Code, valueRR.Body.String())
	}
	var value map[string]any
	if err := json.NewDecoder(valueRR.Body).Decode(&value); err != nil {
		t.Fatalf("decode property value: %v", err)
	}
	if value["group_id"] != "group-a" || value["target_id"] != "target-b" || value["field_id"] != "field-c" {
		t.Fatalf("property value = %#v", value)
	}
}
