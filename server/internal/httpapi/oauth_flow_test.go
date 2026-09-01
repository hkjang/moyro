package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/config"
	"github.com/hkjang/moyro/server/internal/oauth"
)

func TestOAuthLoginUsesIndependentFlowCookiesAndPreservesReturnTo(t *testing.T) {
	cfg := &config.Config{
		PublicBaseURL:       "https://chat.example.test",
		OAuthGoogleClientID: "client", OAuthGoogleClientSecret: "secret",
	}
	h := &handlers{cfg: cfg, oauthReg: oauth.NewRegistry(cfg)}
	router := chi.NewRouter()
	router.Get("/api/v4/oauth/{provider}/login", h.oauthLogin)

	requestFlow := func(returnTo string) *http.Cookie {
		r := httptest.NewRequest(http.MethodGet, "/api/v4/oauth/google/login?return_to="+url.QueryEscape(returnTo), nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != http.StatusFound {
			t.Fatalf("login status = %d, body=%s", w.Code, w.Body.String())
		}
		cookies := w.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("flow cookies = %d", len(cookies))
		}
		return cookies[0]
	}

	first := requestFlow("/today?tab=mine")
	second := requestFlow("/workspace/team-a")
	if first.Name == second.Name {
		t.Fatal("parallel OAuth flows reused one fixed cookie name")
	}
	if first.Path != "/api/v4/oauth/google/callback" || !first.HttpOnly || !first.Secure || first.SameSite != http.SameSiteLaxMode {
		t.Fatalf("first flow cookie = %#v", first)
	}
	for cookie, wantReturnTo := range map[*http.Cookie]string{first: "/today?tab=mine", second: "/workspace/team-a"} {
		parts := strings.SplitN(cookie.Value, ":", 3)
		if len(parts) != 3 || parts[0] != "google" || parts[1] == "" {
			t.Fatalf("flow cookie value has invalid bounded envelope: %q", cookie.Value)
		}
		returnTo, err := url.QueryUnescape(parts[2])
		if err != nil || returnTo != wantReturnTo {
			t.Fatalf("return_to = %q, %v; want %q", returnTo, err, wantReturnTo)
		}
	}
}
