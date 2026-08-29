package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hkjang/moyro/server/internal/config"
)

func TestGetClientConfig(t *testing.T) {
	h := &handlers{cfg: &config.Config{PublicBaseURL: "https://chat.example.com", LinkPreviewsEnabled: false}}
	req := httptest.NewRequest(http.MethodGet, "/api/v4/config/client", nil)
	rr := httptest.NewRecorder()

	h.getClientConfig(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["SiteURL"] != "https://chat.example.com" {
		t.Fatalf("SiteURL = %q, want configured URL", got["SiteURL"])
	}
	if got["WebsocketURL"] != "wss://chat.example.com/api/v4/websocket" {
		t.Fatalf("WebsocketURL = %q, want wss websocket URL", got["WebsocketURL"])
	}
	if got["EnableLinkPreviews"] != "false" {
		t.Fatalf("EnableLinkPreviews = %q, want false", got["EnableLinkPreviews"])
	}
	if got["EnableCommands"] != "true" || got["EnableCustomEmoji"] != "true" {
		t.Fatalf("expected common client capability flags, got %#v", got)
	}
}

func TestGetClientLicenseRequiresOldFormat(t *testing.T) {
	h := &handlers{}

	req := httptest.NewRequest(http.MethodGet, "/api/v4/license/client", nil)
	rr := httptest.NewRecorder()
	h.getClientLicense(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing format status = %d, want 400", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v4/license/client?format=new", nil)
	rr = httptest.NewRecorder()
	h.getClientLicense(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid format status = %d, want 400", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v4/license/client?format=old", nil)
	rr = httptest.NewRecorder()
	h.getClientLicense(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("old format status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["IsLicensed"] != "false" {
		t.Fatalf("IsLicensed = %q, want false", got["IsLicensed"])
	}
}

func TestGetEnvironmentConfigIsEmptyCompatibilityMap(t *testing.T) {
	h := &handlers{}
	req := httptest.NewRequest(http.MethodGet, "/api/v4/config/environment", nil)
	rr := httptest.NewRecorder()

	h.getEnvironmentConfig(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if rr.Header().Get("Cache-Control") == "" {
		t.Fatalf("Cache-Control header missing")
	}
	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("environment config = %#v, want empty map", got)
	}
}

func TestGetSupportedTimezones(t *testing.T) {
	h := &handlers{}
	req := httptest.NewRequest(http.MethodGet, "/api/v4/system/timezones", nil)
	rr := httptest.NewRecorder()

	h.getSupportedTimezones(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got []string
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("timezones should not be empty")
	}
	foundSeoul := false
	for _, tz := range got {
		if tz == "Asia/Seoul" {
			foundSeoul = true
			break
		}
	}
	if !foundSeoul {
		t.Fatalf("Asia/Seoul missing from supported timezones: %#v", got)
	}
}

func TestWebsocketURLForBase(t *testing.T) {
	tests := []struct {
		base string
		want string
	}{
		{base: "http://localhost:8065", want: "ws://localhost:8065/api/v4/websocket"},
		{base: "https://chat.example.com/base", want: "wss://chat.example.com/api/v4/websocket"},
		{base: "not a url", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.base, func(t *testing.T) {
			if got := websocketURLForBase(tt.base); got != tt.want {
				t.Fatalf("websocketURLForBase(%q) = %q, want %q", tt.base, got, tt.want)
			}
		})
	}
}
