package webui

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/hkjang/moyro/server/internal/tracking"
)

func testHandler(t *testing.T) *Handler {
	t.Helper()
	h, err := NewFS(fstest.MapFS{
		"index.html":          {Data: []byte("<!doctype html><title>moyro</title>")},
		"assets/app-1234.js":  {Data: []byte("console.log('moyro')")},
		"assets/app-1234.css": {Data: []byte("body{}")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestNewFSRequiresIndex(t *testing.T) {
	_, err := NewFS(fstest.MapFS{})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("NewFS error = %v, want fs.ErrNotExist", err)
	}
}

func TestHandlerServesAssetWithImmutableCache(t *testing.T) {
	rr := httptest.NewRecorder()
	testHandler(t).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/assets/app-1234.js", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "console.log('moyro')" {
		t.Fatalf("asset response: status=%d body=%q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
}

func TestHandlerFallsBackForBrowserRoute(t *testing.T) {
	rr := httptest.NewRecorder()
	testHandler(t).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/channels/team/general", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "<!doctype html><title>moyro</title>" {
		t.Fatalf("SPA response: status=%d body=%q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-cache, no-store, must-revalidate" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestHandlerFallsBackForDottedPluginSettingsRoute(t *testing.T) {
	for _, route := range []string{
		"/settings/plugins/com.mattermost.echosummary",
		"/admin/integrations/plugins/com.mattermost.echosummary",
	} {
		rr := httptest.NewRecorder()
		testHandler(t).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, route, nil))
		if rr.Code != http.StatusOK || rr.Body.String() != "<!doctype html><title>moyro</title>" {
			t.Fatalf("plugin settings SPA response for %s: status=%d body=%q", route, rr.Code, rr.Body.String())
		}
		if got := rr.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Fatalf("Content-Type for %s = %q", route, got)
		}
	}
}

func TestHandlerDoesNotFallbackForMissingAssetOrMutation(t *testing.T) {
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/assets", nil),
		httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil),
		httptest.NewRequest(http.MethodGet, "/favicon.svg", nil),
		httptest.NewRequest(http.MethodGet, "/settings/plugins/com.mattermost.echosummary/missing.js", nil),
		httptest.NewRequest(http.MethodGet, "/admin/integrations/plugins/com.mattermost.echosummary/missing.js", nil),
		httptest.NewRequest(http.MethodGet, "/api/v4/missing", nil),
		httptest.NewRequest(http.MethodGet, "/hooks/missing", nil),
		httptest.NewRequest(http.MethodGet, "/mcp/missing", nil),
		httptest.NewRequest(http.MethodGet, "/metrics", nil),
		httptest.NewRequest(http.MethodGet, "/metrics/missing", nil),
		httptest.NewRequest(http.MethodGet, "/healthz/missing", nil),
		httptest.NewRequest(http.MethodPost, "/channels/team/general", nil),
	} {
		rr := httptest.NewRecorder()
		testHandler(t).ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s %s status=%d, want 404", req.Method, req.URL.Path, rr.Code)
		}
	}
}

// TestHandlerSetsContentSecurityPolicy pins the browser policy: scripts only
// from the bundle and the plugin runtime's object URLs, connections only to
// this origin's HTTP and WebSocket endpoints, and no framing.
func TestHandlerSetsContentSecurityPolicy(t *testing.T) {
	handler := testHandler(t)
	for _, path := range []string{"/", "/assets/app.js", "/workspace/team-1"} {
		req := httptest.NewRequest(http.MethodGet, "http://moyro.example"+path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		csp := rec.Header().Get("Content-Security-Policy")
		if csp == "" {
			t.Fatalf("%s: no Content-Security-Policy header", path)
		}
		for _, directive := range []string{
			"script-src 'self' blob:",
			"connect-src 'self' ws://moyro.example wss://moyro.example",
			"frame-ancestors 'none'",
			"object-src 'none'",
			"style-src 'self' 'unsafe-inline'",
		} {
			if !strings.Contains(csp, directive) {
				t.Fatalf("%s: policy %q lacks %q", path, csp, directive)
			}
		}
		if strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "http:") && !strings.Contains(csp, "ws://") {
			t.Fatalf("%s: policy %q is broader than intended", path, csp)
		}
	}
}

func trackedHandler(t *testing.T, config tracking.Config) *Handler {
	t.Helper()
	h, err := NewFS(fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><html><head><title>moyro</title></head><body><div id=\"root\"></div></body></html>")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	h.SetTracking(func() tracking.Config { return config })
	return h
}

func momentoConfig(proxy bool) tracking.Config {
	config := tracking.Default()
	config.Enabled = true
	config.Provider = tracking.ProviderMomento
	config.MomentoURL = "https://momento.corp.example"
	config.MomentoSiteID = "moyro-prd"
	config.MomentoProxy = proxy
	return config
}

var nonceInPolicy = regexp.MustCompile(`'nonce-([^']+)'`)

// TestTrackingSnippetAndPolicyShareOneNonce is the whole contract: the page,
// the injected snippet and the policy header have to agree on a nonce that
// is fresh per response, and nothing else in the policy may loosen.
func TestTrackingSnippetAndPolicyShareOneNonce(t *testing.T) {
	handler := trackedHandler(t, momentoConfig(true))
	seen := map[string]bool{}
	for _, path := range []string{"/", "/today", "/workspace/team-1/channel/c"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://moyro.example"+path, nil))
		body, csp := rec.Body.String(), rec.Header().Get("Content-Security-Policy")
		match := nonceInPolicy.FindStringSubmatch(csp)
		if match == nil {
			t.Fatalf("%s: policy %q names no nonce", path, csp)
		}
		nonce := match[1]
		if seen[nonce] {
			t.Fatalf("%s: nonce %q was reused across responses", path, nonce)
		}
		seen[nonce] = true
		if !strings.Contains(body, `<script nonce="`+nonce+`" async src="/momento/tracker.js"`) {
			t.Fatalf("%s: snippet missing or carries another nonce: %s", path, body)
		}
		if !strings.Contains(body, `data-endpoint="/momento"`) || strings.Contains(body, "momento.corp.example") {
			t.Fatalf("%s: proxied snippet must not name the collector: %s", path, body)
		}
		if strings.Index(body, "/momento/tracker.js") > strings.Index(body, "</head>") {
			t.Fatalf("%s: snippet is not in head: %s", path, body)
		}
		for _, directive := range []string{
			"script-src 'self' blob: 'nonce-" + nonce + "'",
			"connect-src 'self' ws://moyro.example wss://moyro.example",
			"report-uri " + CSPReportPath,
			"object-src 'none'",
		} {
			if !strings.Contains(csp, directive) {
				t.Fatalf("%s: policy %q lacks %q", path, csp, directive)
			}
		}
		scriptSrc := csp[strings.Index(csp, "script-src"):]
		scriptSrc = scriptSrc[:strings.Index(scriptSrc, ";")]
		if strings.Contains(scriptSrc, "unsafe-inline") || strings.Contains(csp, "momento.corp.example") {
			t.Fatalf("%s: policy %q is broader than the snippet needs", path, csp)
		}
		if rec.Header().Get("Last-Modified") != "" {
			t.Fatalf("%s: a nonced page must not be served conditionally", path)
		}
	}
}

func TestTrackingLeavesAdminAndReservedRoutesAlone(t *testing.T) {
	handler := trackedHandler(t, momentoConfig(true))
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/admin/site", http.StatusOK},
		{"/settings/profile", http.StatusOK},
		{"/api/v4/users/me", http.StatusNotFound},
		{"/healthz", http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://moyro.example"+tc.path, nil))
		if rec.Code != tc.status {
			t.Fatalf("%s: status=%d want %d", tc.path, rec.Code, tc.status)
		}
		csp := rec.Header().Get("Content-Security-Policy")
		if strings.Contains(rec.Body.String(), "tracker.js") || strings.Contains(csp, "nonce-") || strings.Contains(csp, "report-uri") {
			t.Fatalf("%s: tracked although it must not be: csp=%q body=%q", tc.path, csp, rec.Body.String())
		}
		if !strings.Contains(csp, "script-src 'self' blob:;") {
			t.Fatalf("%s: policy %q is not the strict default", tc.path, csp)
		}
	}

	included := momentoConfig(true)
	included.IncludeAdmin = true
	rec := httptest.NewRecorder()
	trackedHandler(t, included).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://moyro.example/admin/site", nil))
	if !strings.Contains(rec.Body.String(), "tracker.js") {
		t.Fatal("include_admin should attach the snippet to the console")
	}
}

func TestTrackingOffRestoresTheStrictPolicy(t *testing.T) {
	config := momentoConfig(false)
	config.Enabled = false
	handler := trackedHandler(t, config)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://moyro.example/today", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	if strings.Contains(csp, "nonce-") || strings.Contains(csp, "report-uri") || strings.Contains(csp, "momento") {
		t.Fatalf("policy %q still carries tracking", csp)
	}
	if got := rec.Body.String(); got != "<!doctype html><html><head><title>moyro</title></head><body><div id=\"root\"></div></body></html>" {
		t.Fatalf("page was altered while tracking is off: %s", got)
	}
}

func TestTrackingWithoutProxyAllowsTheCollectorOriginAndBodyPlacement(t *testing.T) {
	config := momentoConfig(false)
	config.Placement = tracking.PlacementBody
	config.AllowedHosts = []string{"pixel.corp.example"}
	handler := trackedHandler(t, config)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://moyro.example/today", nil))
	body, csp := rec.Body.String(), rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(body, `src="https://momento.corp.example/tracker.js"`) {
		t.Fatalf("direct snippet missing: %s", body)
	}
	if strings.Index(body, "tracker.js") < strings.Index(body, "</head>") || strings.Index(body, "tracker.js") > strings.Index(body, "</body>") {
		t.Fatalf("snippet is not at the end of body: %s", body)
	}
	for _, directive := range []string{
		"https://momento.corp.example https://pixel.corp.example; style-src",
		"connect-src 'self' ws://moyro.example wss://moyro.example https://momento.corp.example https://pixel.corp.example",
		"img-src 'self' data: blob: https://momento.corp.example https://pixel.corp.example",
	} {
		if !strings.Contains(csp, directive) {
			t.Fatalf("policy %q lacks %q", csp, directive)
		}
	}
}
