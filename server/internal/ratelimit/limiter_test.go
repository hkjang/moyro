package ratelimit

import (
	"net/http/httptest"
	"testing"
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
