package webui

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
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

func TestHandlerDoesNotFallbackForMissingAssetOrMutation(t *testing.T) {
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/assets", nil),
		httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil),
		httptest.NewRequest(http.MethodGet, "/favicon.svg", nil),
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
