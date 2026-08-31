package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/oidcauth"
)

func TestSanitizeReturnToAcceptsOnlySameOriginPathsWithoutFragments(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"/",
		"/workspace/team-a/channel-a",
		"/settings/security/sessions?from=profile",
		"/search?q=https%3A%2F%2Fexample.test",
	} {
		if got := sanitizeReturnTo(value); got != value {
			t.Errorf("sanitizeReturnTo(%q) = %q, want unchanged", value, got)
		}
	}

	for _, value := range []string{
		"https://attacker.example/",
		"//attacker.example/",
		"/\\attacker.example/",
		"/%5cattacker.example/",
		"/%255cattacker.example/",
		"/%2f%2fattacker.example/",
		"/%252f%252fattacker.example/",
		"/workspace#//attacker.example/",
		"/workspace#profile",
		"/workspace\r\nLocation: https://attacker.example/",
		"javascript:alert(1)",
		"",
	} {
		if got := sanitizeReturnTo(value); got != "" {
			t.Errorf("sanitizeReturnTo(%q) = %q, want rejection", value, got)
		}
	}
}

func TestExternalOriginIgnoresUntrustedForwardingHeaders(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "http://moyro.internal:8065/admin", nil)
	r.Header.Set("Forwarded", `host=attacker.example;proto=https`)
	r.Header.Set("X-Forwarded-Host", "attacker.example")
	r.Header.Set("X-Forwarded-Proto", "https")
	origin, err := externalOrigin(r)
	if err != nil {
		t.Fatalf("externalOrigin() error = %v", err)
	}
	if origin != "http://moyro.internal:8065" {
		t.Fatalf("externalOrigin() = %q, want direct request origin", origin)
	}
}

func TestExternalOriginAcceptsOnlySameHostBrowserOriginScheme(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "http://moyro.internal:8065/admin", nil)
	r.Header.Set("Origin", "https://moyro.internal:8065")
	origin, err := externalOrigin(r)
	if err != nil || origin != "https://moyro.internal:8065" {
		t.Fatalf("same-host browser origin = (%q, %v)", origin, err)
	}

	r.Header.Set("Origin", "https://attacker.example")
	origin, err = externalOrigin(r)
	if err != nil || origin != "http://moyro.internal:8065" {
		t.Fatalf("cross-host browser origin influenced result = (%q, %v)", origin, err)
	}
}

func TestExternalOriginRejectsMalformedHost(t *testing.T) {
	t.Parallel()

	for _, host := range []string{"", "good.example@attacker.example", "good.example/redirect", "good.example,attacker.example", " good.example"} {
		r := httptest.NewRequest(http.MethodPost, "http://moyro.internal/admin", nil)
		r.Host = host
		if origin, err := externalOrigin(r); err == nil {
			t.Errorf("externalOrigin() with Host %q = %q, want error", host, origin)
		}
	}
}

func TestOIDCRestartPreservesStoredCallbackWithoutManagedPublicURL(t *testing.T) {
	t.Parallel()
	const stored = "https://moyro.intranet/api/moyro/v1/auth/oidc/callback"
	if got := oidcRedirectURLForReload(stored, ""); got != stored {
		t.Fatalf("restart callback = %q, want stored callback %q", got, stored)
	}
	if got := oidcRedirectURLForReload(stored, "https://new.intranet/"); got != "https://new.intranet"+oidcCallbackPath {
		t.Fatalf("managed callback = %q", got)
	}
}

func TestRuntimeOIDCDiscoveryStatusReflectsFailedReload(t *testing.T) {
	t.Parallel()

	ready := oidcProviderView{Enabled: true, DiscoveryStatus: "ready"}
	if got := runtimeOIDCDiscoveryStatus(ready, false); got != "error" {
		t.Fatalf("failed reload status = %q, want error", got)
	}
	if got := runtimeOIDCDiscoveryStatus(ready, true); got != "ready" {
		t.Fatalf("live provider status = %q, want ready", got)
	}
	disabled := oidcProviderView{Enabled: false, DiscoveryStatus: "unknown"}
	if got := runtimeOIDCDiscoveryStatus(disabled, false); got != "unknown" {
		t.Fatalf("disabled provider status = %q, want unknown", got)
	}
}

func TestOIDCProviderConfigMapsInsecureBackchannelOptIn(t *testing.T) {
	t.Parallel()

	cfg := (oidcProviderView{AllowInsecureBackchannel: true}).oidcConfig("secret")
	if !cfg.AllowInsecureBackchannel {
		t.Fatal("allow_insecure_backchannel was not passed to the OIDC manager")
	}
}

type recordingOIDCFlowConsumer struct {
	flow   oidcauth.Flow
	err    error
	states []string
}

func (s *recordingOIDCFlowConsumer) Consume(_ context.Context, state string) (oidcauth.Flow, error) {
	s.states = append(s.states, state)
	return s.flow, s.err
}

func TestConsumeOIDCCallbackTransactionConsumesProviderErrorAndClearsCookie(t *testing.T) {
	t.Parallel()

	store := &recordingOIDCFlowConsumer{flow: oidcauth.Flow{Nonce: "nonce", Verifier: "verifier"}}
	r := httptest.NewRequest(http.MethodGet, oidcCallbackPath+"?state=state-value&error=access_denied", nil)
	r.AddCookie(&http.Cookie{Name: oidcTransactionCookieName("state-value"), Value: "state-value", Path: oidcCallbackPath})
	w := httptest.NewRecorder()

	_, code := consumeOIDCCallbackTransaction(w, r, store, true)
	if code != "provider_access_denied" {
		t.Fatalf("callback error = %q", code)
	}
	if len(store.states) != 1 || store.states[0] != "state-value" {
		t.Fatalf("consumed states = %#v, want exactly the callback state", store.states)
	}
	assertOIDCCookieCleared(t, w.Result(), "state-value")
}

func TestConsumeOIDCCallbackTransactionConsumesStateOnCookieMismatch(t *testing.T) {
	t.Parallel()

	store := &recordingOIDCFlowConsumer{flow: oidcauth.Flow{Nonce: "nonce", Verifier: "verifier"}}
	r := httptest.NewRequest(http.MethodGet, oidcCallbackPath+"?state=callback-state", nil)
	r.AddCookie(&http.Cookie{Name: oidcTransactionCookieName("callback-state"), Value: "different-state", Path: oidcCallbackPath})
	w := httptest.NewRecorder()

	_, code := consumeOIDCCallbackTransaction(w, r, store, false)
	if code != "state_mismatch" {
		t.Fatalf("callback error = %q, want state_mismatch", code)
	}
	if len(store.states) != 1 || store.states[0] != "callback-state" {
		t.Fatalf("consumed states = %#v", store.states)
	}
	assertOIDCCookieCleared(t, w.Result(), "callback-state")
}

func TestConsumeOIDCCallbackTransactionRejectsConsumedOrMissingState(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		cookie    string
		storeErr  error
		queryPart string
	}{
		{name: "missing browser cookie", queryPart: "?state=state-value"},
		{name: "already consumed", queryPart: "?state=state-value", cookie: "state-value", storeErr: oidcauth.ErrInvalidFlow},
		{name: "missing state", queryPart: "?error=access_denied", cookie: "state-value", storeErr: oidcauth.ErrInvalidFlow},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &recordingOIDCFlowConsumer{err: test.storeErr}
			r := httptest.NewRequest(http.MethodGet, oidcCallbackPath+test.queryPart, nil)
			if test.cookie != "" {
				r.AddCookie(&http.Cookie{Name: oidcTransactionCookieName(r.URL.Query().Get("state")), Value: test.cookie})
			}
			w := httptest.NewRecorder()
			_, code := consumeOIDCCallbackTransaction(w, r, store, false)
			if code != "state_mismatch" {
				t.Fatalf("callback error = %q, want state_mismatch", code)
			}
			if len(store.states) != 1 {
				t.Fatalf("Consume calls = %d, want 1", len(store.states))
			}
		})
	}
}

func TestConsumeOIDCCallbackTransactionReturnsFlowOnBoundSuccess(t *testing.T) {
	t.Parallel()

	want := oidcauth.Flow{Nonce: "nonce", Verifier: "verifier", ReturnTo: "/workspace"}
	store := &recordingOIDCFlowConsumer{flow: want}
	r := httptest.NewRequest(http.MethodGet, oidcCallbackPath+"?state=state-value&code=code-value", nil)
	r.AddCookie(&http.Cookie{Name: oidcTransactionCookieName("state-value"), Value: "state-value"})
	w := httptest.NewRecorder()

	got, code := consumeOIDCCallbackTransaction(w, r, store, false)
	if code != "" || got != want {
		t.Fatalf("callback = (%+v, %q), want (%+v, empty)", got, code, want)
	}
}

func TestSetOIDCTransactionCookieSecurityAttributes(t *testing.T) {
	t.Parallel()
	if oidcTransactionCookieName("state-value") == oidcTransactionCookieName("another-state") {
		t.Fatal("independent OIDC flows shared a transaction cookie name")
	}

	w := httptest.NewRecorder()
	setOIDCTransactionCookie(w, "state-value", time.Now().Add(10*time.Minute).UnixMilli(), true)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != oidcTransactionCookieName("state-value") || cookie.Value != "state-value" || cookie.Path != oidcCallbackPath {
		t.Fatalf("unexpected transaction cookie: %+v", cookie)
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge <= 0 {
		t.Fatalf("transaction cookie lacks security attributes: %+v", cookie)
	}
}

func TestSetSSOHandoffCookieSecurityAttributes(t *testing.T) {
	t.Parallel()
	if ssoHandoffCookieName("handoff-code") == ssoHandoffCookieName("another-code") {
		t.Fatal("independent SSO flows shared a handoff cookie name")
	}

	w := httptest.NewRecorder()
	setSSOHandoffCookie(w, "handoff-code", "browser-binding", time.Now().Add(5*time.Minute).UnixMilli(), true)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != ssoHandoffCookieName("handoff-code") || cookie.Value != "browser-binding" || cookie.Path != ssoSessionExchangePath {
		t.Fatalf("unexpected SSO handoff cookie: %+v", cookie)
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge <= 0 {
		t.Fatalf("SSO handoff cookie lacks security attributes: %+v", cookie)
	}

	clearRecorder := httptest.NewRecorder()
	clearSSOHandoffCookie(clearRecorder, "handoff-code", true)
	assertSSOHandoffCookieCleared(t, clearRecorder.Result(), "handoff-code")
}

func assertOIDCCookieCleared(t *testing.T, response *http.Response, state string) {
	t.Helper()
	cookies := response.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("clear cookies = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != oidcTransactionCookieName(state) || cookie.Path != oidcCallbackPath || cookie.MaxAge >= 0 || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("transaction cookie was not securely cleared: %+v", cookie)
	}
}

func assertSSOHandoffCookieCleared(t *testing.T, response *http.Response, code string) {
	t.Helper()
	cookies := response.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("SSO clear cookies = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != ssoHandoffCookieName(code) || cookie.Path != ssoSessionExchangePath || cookie.MaxAge >= 0 ||
		!cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("SSO handoff cookie was not securely cleared: %+v", cookie)
	}
}

func TestSanitizeProviderError(t *testing.T) {
	t.Parallel()

	if got := sanitizeProviderError("temporarily_unavailable"); got != "temporarily_unavailable" {
		t.Fatalf("safe provider error = %q", got)
	}
	for _, value := range []string{"bad/value", "bad value", "bad#value", string(make([]byte, 65))} {
		if got := sanitizeProviderError(value); got != "error" {
			t.Errorf("sanitizeProviderError(%q) = %q, want error", value, got)
		}
	}
}
