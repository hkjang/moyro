package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/moddle/moddle/server/internal/config"
)

func requestWithRouteParam(method, path, key, value string, body []byte) *http.Request {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

func TestAdminCompatConfigSnapshot(t *testing.T) {
	h := &handlers{cfg: &config.Config{
		Listen:              ":8065",
		PublicBaseURL:       "http://localhost:8065",
		PluginDir:           "./plugins",
		FileStorageRoot:     "./data/files",
		FileBackend:         "fs",
		LinkPreviewsEnabled: true,
	}}
	req := httptest.NewRequest(http.MethodGet, "/api/v4/config", nil)
	rr := httptest.NewRecorder()

	h.getConfig(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["ServiceSettings"]["SiteURL"] != "http://localhost:8065" {
		t.Fatalf("SiteURL = %#v, want configured URL", got["ServiceSettings"]["SiteURL"])
	}
	if got["PluginSettings"]["Enable"] != true {
		t.Fatalf("PluginSettings.Enable = %#v, want true", got["PluginSettings"]["Enable"])
	}
}

func TestAdminCompatServerBusyLifecycle(t *testing.T) {
	h := &handlers{}
	compatServerBusy.Store(false)
	defer compatServerBusy.Store(false)

	rr := httptest.NewRecorder()
	h.setServerBusy(rr, httptest.NewRequest(http.MethodPost, "/api/v4/server_busy", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("set status = %d, want 200", rr.Code)
	}

	rr = httptest.NewRecorder()
	h.getServerBusy(rr, httptest.NewRequest(http.MethodGet, "/api/v4/server_busy", nil))
	var got map[string]bool
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode busy: %v", err)
	}
	if !got["busy"] {
		t.Fatalf("busy = false, want true")
	}

	rr = httptest.NewRecorder()
	h.clearServerBusy(rr, httptest.NewRequest(http.MethodDelete, "/api/v4/server_busy", nil))
	if rr.Code != http.StatusOK || compatServerBusy.Load() {
		t.Fatalf("clear status=%d busy=%v, want 200/false", rr.Code, compatServerBusy.Load())
	}
}

func TestAdminCompatRolesByNames(t *testing.T) {
	h := &handlers{}
	req := httptest.NewRequest(http.MethodPost, "/api/v4/roles/names", bytes.NewBufferString(`["system_admin","missing"]`))
	rr := httptest.NewRecorder()

	h.getRolesByNames(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got []compatRole
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode roles: %v", err)
	}
	if len(got) != 1 || got[0].Name != "system_admin" {
		t.Fatalf("roles = %#v, want system_admin only", got)
	}
}

func TestAdminCompatJobLifecycle(t *testing.T) {
	h := &handlers{}
	compatJobs.Lock()
	compatJobs.rows = map[string]map[string]any{}
	compatJobs.Unlock()

	createReq := httptest.NewRequest(http.MethodPost, "/api/v4/jobs", bytes.NewBufferString(`{"type":"smoke"}`))
	rr := httptest.NewRecorder()
	h.createJob(rr, createReq)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var created AdminJobForTest
	if err := json.NewDecoder(rr.Body).Decode(&created); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	if created.ID == "" || created.Type != "smoke" || created.Status != "pending" {
		t.Fatalf("created job = %#v", created)
	}

	cancelReq := requestWithRouteParam(http.MethodPost, "/api/v4/jobs/"+created.ID+"/cancel", "jobID", created.ID, nil)
	rr = httptest.NewRecorder()
	h.cancelJob(rr, cancelReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var canceled AdminJobForTest
	if err := json.NewDecoder(rr.Body).Decode(&canceled); err != nil {
		t.Fatalf("decode canceled job: %v", err)
	}
	if canceled.Status != "canceled" {
		t.Fatalf("status = %q, want canceled", canceled.Status)
	}
}

type AdminJobForTest struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Status string `json:"status"`
}
