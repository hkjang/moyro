package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestForwardedHeadersRequireTrustedPeer(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://internal.local/path", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("X-Forwarded-For", "198.51.100.7")
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "chat.example.test")
	h := &handlers{}
	if got := h.clientIP(r); got != "203.0.113.9" {
		t.Fatalf("untrusted client IP = %q", got)
	}
	if got, _ := h.externalOrigin(r); got != "http://internal.local" {
		t.Fatalf("untrusted origin = %q", got)
	}
}

func TestTrustedProxyChainSelectsOriginalUntrustedClient(t *testing.T) {
	h := handlersWithSiteSettings(siteSettingsView{
		SiteName: "moyro", TrustedProxyCIDRs: []string{"10.0.0.0/8", "192.0.2.0/24"},
	})
	r := httptest.NewRequest(http.MethodGet, "http://internal.local/path", nil)
	r.RemoteAddr = "10.0.0.4:8443"
	r.Header.Set("X-Forwarded-For", "198.51.100.7, 192.0.2.9")
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "chat.example.test")
	if got := h.clientIP(r); got != "198.51.100.7" {
		t.Fatalf("trusted proxy client IP = %q", got)
	}
	if got, _ := h.externalOrigin(r); got != "https://chat.example.test" {
		t.Fatalf("trusted proxy origin = %q", got)
	}
}

func handlersWithSiteSettings(value siteSettingsView) *handlers {
	native := &nativeServices{}
	native.site.Store(&value)
	return &handlers{native: native}
}

func TestCookieMutationRequiresSameOrigin(t *testing.T) {
	h := handlersWithSiteSettings(siteSettingsView{SiteName: "moyro", PublicBaseURL: "https://chat.example.test"})
	r := httptest.NewRequest(http.MethodPost, "https://chat.example.test/api/v4/posts", nil)
	r.Header.Set("Origin", "https://chat.example.test")
	if !h.validateBrowserOrigin(r) {
		t.Fatal("same origin was rejected")
	}
	r.Header.Set("Origin", "https://evil.example.test")
	if h.validateBrowserOrigin(r) {
		t.Fatal("cross-site origin was accepted")
	}
}
