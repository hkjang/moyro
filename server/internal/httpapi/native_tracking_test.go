package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hkjang/moyro/server/internal/settings"
	"github.com/hkjang/moyro/server/internal/tracking"
	"github.com/hkjang/moyro/server/internal/webui"
)

// memorySettingsRepository is enough of the settings store for the tracking
// section: one row per (section, key), no revisions, no secrets.
type memorySettingsRepository struct {
	mu   sync.Mutex
	rows map[string]settings.Record
}

func (m *memorySettingsRepository) Get(_ context.Context, section, key string) (settings.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.rows[section+"/"+key]
	if !ok {
		return settings.Record{}, settings.ErrNotFound
	}
	return record, nil
}
func (m *memorySettingsRepository) List(context.Context, string) ([]settings.Record, error) {
	return nil, nil
}
func (m *memorySettingsRepository) Put(_ context.Context, record settings.Record, _ *int64) (settings.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows == nil {
		m.rows = map[string]settings.Record{}
	}
	m.rows[record.Section+"/"+record.Key] = record
	return record, nil
}
func (m *memorySettingsRepository) Delete(context.Context, string, string, *int64) error { return nil }

func trackingTestHandlers(t *testing.T) (*handlers, *memorySettingsRepository) {
	t.Helper()
	repo := &memorySettingsRepository{}
	service, err := settings.New(repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	native := &nativeServices{settings: service}
	if err := native.reloadTracking(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &handlers{native: native, violations: tracking.NewRecorder()}, repo
}

func patchTracking(t *testing.T, h *handlers, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := requestWithRouteParam(http.MethodPatch, "/api/moyro/v1/admin/settings/tracking", "section", "tracking", []byte(body))
	req = req.WithContext(context.WithValue(req.Context(), userIDKey, "admin"))
	h.patchNativeSettings(rec, req)
	return rec
}

// A fresh installation runs with tracking off, and a saved configuration is
// what the page handler reads on the next response — no restart.
func TestTrackingSettingsDefaultOffAndApplyOnSave(t *testing.T) {
	h, repo := trackingTestHandlers(t)
	if got := h.trackingConfig(); got.Enabled || got.Provider != tracking.ProviderNone || got.Active("/today") {
		t.Fatalf("fresh config = %+v, want off", got)
	}
	// The admin screen reads allowed_hosts as an array, never null.
	rec := httptest.NewRecorder()
	h.getNativeSettings(rec, requestWithRouteParam(http.MethodGet, "/api/moyro/v1/admin/settings/tracking", "section", "tracking", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"allowed_hosts":[]`) {
		t.Fatalf("fresh read status=%d body=%s", rec.Code, rec.Body.String())
	}
	if (&handlers{}).trackingConfig().Enabled {
		t.Fatal("without management services tracking must be off")
	}

	rec = patchTracking(t, h, `{"enabled":true,"provider":"momento","momento_url":"https://momento.corp.example/","momento_site_id":"moyro-prd","allowed_hosts":["pixel.corp.example"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var saved tracking.Config
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.MomentoProxy || saved.MomentoURL != "https://momento.corp.example" || saved.AllowedHosts[0] != "https://pixel.corp.example" {
		t.Fatalf("saved = %+v", saved)
	}
	if live := h.trackingConfig(); !live.Active("/today") || !live.ProxiesMomento() {
		t.Fatalf("live config was not applied: %+v", live)
	}
	if _, ok := repo.rows["tracking/config"]; !ok {
		t.Fatal("configuration was not stored")
	}

	// A configuration that cannot work is refused and the live one stays.
	rec = patchTracking(t, h, `{"enabled":true,"provider":"ga4"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "measurement_id") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !h.trackingConfig().Active("/today") {
		t.Fatal("a rejected save must not disturb the live configuration")
	}
	// An oversized snippet is not stored either.
	rec = patchTracking(t, h, `{"enabled":true,"provider":"custom","custom_snippet":"`+strings.Repeat("x", tracking.MaxSnippetBytes+1)+`"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "8192") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Turning it off closes the proxy and stops the page from tracking.
	rec = patchTracking(t, h, `{"enabled":false,"provider":"momento","momento_url":"https://momento.corp.example","momento_site_id":"moyro-prd"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if live := h.trackingConfig(); live.Active("/today") || live.ProxiesMomento() {
		t.Fatalf("tracking still active after being turned off: %+v", live)
	}

	// The read side returns the normalized document.
	rec = httptest.NewRecorder()
	h.getNativeSettings(rec, requestWithRouteParam(http.MethodGet, "/api/moyro/v1/admin/settings/tracking", "section", "tracking", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"momento_site_id":"moyro-prd"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// The whole point of the report endpoint is that a blocked address becomes
// something an administrator can see and allow from the console.
func TestBlockedRequestsAreReportedAndListed(t *testing.T) {
	h, _ := trackingTestHandlers(t)
	report := `{"csp-report":{"blocked-uri":"https://momento.corp.example/collect/v1/events","effective-directive":"connect-src","document-uri":"https://moyro.example/today"}}`
	post := func(body string) int {
		rec := httptest.NewRecorder()
		h.receiveCSPReport(rec, httptest.NewRequest(http.MethodPost, webui.CSPReportPath, strings.NewReader(body)))
		return rec.Code
	}
	// Nothing is kept while tracking is off: no page asked for reports.
	if post(report) != http.StatusNoContent || len(h.violations.List(h.trackingConfig())) != 0 {
		t.Fatal("reports must be ignored while tracking is off")
	}
	if rec := patchTracking(t, h, `{"enabled":true,"provider":"custom","custom_snippet":"<script>fetch('https://hidden.example/x')</script>"}`); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if post(report) != http.StatusNoContent || post(report) != http.StatusNoContent {
		t.Fatal("reports must be answered 204")
	}
	// Not JSON, and too large: accepted and ignored rather than answered with
	// an error the page would log again.
	if post("not json") != http.StatusNoContent || post(strings.Repeat("{", maxCSPReportBytes*2)) != http.StatusNoContent {
		t.Fatal("malformed reports must still be answered 204")
	}

	rec := httptest.NewRecorder()
	h.listTrackingViolations(rec, httptest.NewRequest(http.MethodGet, "/api/moyro/v1/admin/tracking/violations", nil))
	var listed struct{ Items []tracking.Violation }
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 || listed.Items[0].Origin != "https://momento.corp.example" || listed.Items[0].Directive != "connect-src" || listed.Items[0].Count != 2 || listed.Items[0].Allowed {
		t.Fatalf("items=%+v", listed.Items)
	}

	// Allowing the origin marks the report as resolved on the next read.
	if rec := patchTracking(t, h, `{"enabled":true,"provider":"custom","custom_snippet":"<script>fetch('https://hidden.example/x')</script>","allowed_hosts":["https://momento.corp.example"]}`); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if items := h.violations.List(h.trackingConfig()); !items[0].Allowed {
		t.Fatalf("items=%+v", items)
	}
	rec = httptest.NewRecorder()
	h.clearTrackingViolations(rec, httptest.NewRequest(http.MethodDelete, "/api/moyro/v1/admin/tracking/violations", nil))
	if rec.Code != http.StatusNoContent || len(h.violations.List(h.trackingConfig())) != 0 {
		t.Fatal("clear did not forget the reports")
	}
}

// The proxy is the path that keeps a collector out of the policy: it must
// forward under the collector's own path, drop this service's credentials,
// and be closed whenever Momento tracking through it is not on.
func TestMomentoProxyForwardsOnlyWhileConfigured(t *testing.T) {
	var received *http.Request
	var receivedBody string
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Clone(context.Background())
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write([]byte("window.momento=1"))
	}))
	defer collector.Close()

	h, _ := trackingTestHandlers(t)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "http://moyro.example"+path, strings.NewReader(body))
		req.Header.Set("Cookie", "moyro_session=secret")
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("Content-Type", "application/json")
		h.momentoProxy(rec, req)
		return rec
	}
	if rec := call(http.MethodGet, "/momento/tracker.js", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("closed proxy answered %d", rec.Code)
	}

	if rec := patchTracking(t, h, `{"enabled":true,"provider":"momento","momento_url":"`+collector.URL+`/base/","momento_site_id":"moyro-prd","momento_proxy":true}`); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec := call(http.MethodPost, "/momento/collect/v1/events", `{"events":[]}`)
	if rec.Code != http.StatusOK || rec.Body.String() != "window.momento=1" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if received == nil || received.URL.Path != "/base/collect/v1/events" || received.Method != http.MethodPost || receivedBody != `{"events":[]}` {
		t.Fatalf("collector saw %v %s body=%q", received.Method, received.URL.Path, receivedBody)
	}
	if received.Header.Get("Cookie") != "" || received.Header.Get("Authorization") != "" {
		t.Fatalf("credentials leaked to the collector: %v", received.Header)
	}
	if received.Header.Get("X-Forwarded-Host") != "moyro.example" {
		t.Fatalf("forwarded headers = %v", received.Header)
	}
	if rec := call(http.MethodDelete, "/momento/collect", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE answered %d", rec.Code)
	}

	// Direct mode (no proxy) and disabled both close the proxy again.
	if rec := patchTracking(t, h, `{"enabled":true,"provider":"momento","momento_url":"`+collector.URL+`","momento_site_id":"moyro-prd","momento_proxy":false}`); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := call(http.MethodGet, "/momento/tracker.js", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("proxy stayed open in direct mode: %d", rec.Code)
	}
}
