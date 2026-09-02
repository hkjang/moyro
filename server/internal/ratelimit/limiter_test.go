package ratelimit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientIPIgnoresUntrustedForwardingHeaders(t *testing.T) {
	req := httptest.NewRequest("GET", "http://moyro.local/", nil)
	req.RemoteAddr = "10.20.30.40:54321"
	req.Header.Set("X-Forwarded-For", "203.0.113.10")
	req.Header.Set("X-Real-IP", "203.0.113.11")
	req.Header.Set("True-Client-IP", "203.0.113.12")

	if got := ClientIP(req); got != "10.20.30.40" {
		t.Fatalf("ClientIP = %q, want TCP peer address", got)
	}
}

func TestAllowSpendsBurstThenRefuses(t *testing.T) {
	l := New(1, 2)
	for i := range 2 {
		if !l.Allow("client") {
			t.Fatalf("request %d should have been allowed within the burst", i)
		}
	}
	if l.Allow("client") {
		t.Fatal("third request should have exhausted the burst")
	}
	if !l.Allow("other-client") {
		t.Fatal("a different key must have its own bucket")
	}
}

func TestAllowPrunesStaleBucketsWhileThrottled(t *testing.T) {
	l := New(1, 1)
	now := time.Now()
	l.by["idle"] = &bucket{tokens: 1, lastFill: now.Add(-10 * time.Minute)}
	l.by["busy"] = &bucket{tokens: 0, lastFill: now}
	l.lastGC = now.Add(-2 * time.Minute)

	if l.Allow("busy") {
		t.Fatal("busy bucket should be throttled")
	}
	if _, ok := l.by["idle"]; ok {
		t.Fatal("a refilled, long-untouched bucket should have been pruned")
	}
	if _, ok := l.by["busy"]; !ok {
		t.Fatal("the throttled bucket must survive the prune")
	}
}

// A limiter that refills slowly must say so. Advertising one second on a
// bucket that needs five just makes rejected clients retry four more times.
func TestMiddlewareAdvertisesRealRetryDelay(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rate    float64
		burst   int
		wantSec string
	}{
		{name: "signup bucket refills every five seconds", rate: 0.2, burst: 1, wantSec: "5"},
		{name: "login bucket refills every second", rate: 1, burst: 1, wantSec: "1"},
		{name: "fast bucket floors at one second", rate: 30, burst: 1, wantSec: "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := New(tc.rate, tc.burst).Middleware(ClientIP)(okHandler(t))
			if rec := serve(handler); rec.Code != http.StatusOK {
				t.Fatalf("first request: status = %d, want 200", rec.Code)
			}
			rec := serve(handler)
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("second request: status = %d, want 429", rec.Code)
			}
			if got := rec.Header().Get("Retry-After"); got != tc.wantSec {
				t.Fatalf("Retry-After = %q, want %q", got, tc.wantSec)
			}
			if got := rec.Header().Get("X-Ratelimit-Reset"); got != tc.wantSec {
				t.Fatalf("X-Ratelimit-Reset = %q, want %q", got, tc.wantSec)
			}
		})
	}
}

func TestMiddlewareRejectsWithJSONEnvelope(t *testing.T) {
	handler := New(1, 1).Middleware(ClientIP)(okHandler(t))
	serve(handler)
	rec := serve(handler)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("X-Ratelimit-Limit"); got != "1" {
		t.Fatalf("X-Ratelimit-Limit = %q, want 1", got)
	}
	if got := rec.Header().Get("X-Ratelimit-Remaining"); got != "0" {
		t.Fatalf("X-Ratelimit-Remaining = %q, want 0", got)
	}

	var body struct {
		ID         string `json:"id"`
		Message    string `json:"message"`
		StatusCode int    `json:"status_code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if body.ID != "api.ratelimit.exceeded" || body.StatusCode != http.StatusTooManyRequests || body.Message == "" {
		t.Fatalf("body = %+v, want the shared API error envelope", body)
	}
}

func TestMiddlewareLeavesAllowedResponsesUntouched(t *testing.T) {
	rec := serve(New(1, 1).Middleware(ClientIP)(okHandler(t)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	for _, header := range []string{"Retry-After", "X-Ratelimit-Limit", "X-Ratelimit-Remaining", "X-Ratelimit-Reset"} {
		if got := rec.Header().Get(header); got != "" {
			t.Fatalf("%s = %q on an allowed request, want no header", header, got)
		}
	}
}

// An empty key means the route has no identity to charge — the request must
// pass through rather than share one anonymous bucket with everyone else.
func TestMiddlewareBypassesEmptyKeys(t *testing.T) {
	handler := New(1, 1).Middleware(func(*http.Request) string { return "" })(okHandler(t))
	for i := range 5 {
		if rec := serve(handler); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, rec.Code)
		}
	}
}

func okHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func serve(handler http.Handler) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "http://moyro.local/api/v4/users", nil)
	req.RemoteAddr = "198.51.100.7:41000"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
