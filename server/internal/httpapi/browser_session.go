package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/hkjang/moyro/server/internal/audit"
	"github.com/hkjang/moyro/server/internal/auth"
)

const browserSessionCookie = "moyro_browser_session"

func decodeSingleJSON(w http.ResponseWriter, r *http.Request, limit int64, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON object")
		}
		return err
	}
	return nil
}

func (h *handlers) setBrowserSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: browserSessionCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: h.oauthSecureCookies(r), SameSite: http.SameSiteLaxMode,
	})
}

func (h *handlers) clearBrowserSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: browserSessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: h.oauthSecureCookies(r), SameSite: http.SameSiteLaxMode,
	})
}

func browserSessionToken(r *http.Request) string {
	cookie, err := r.Cookie(browserSessionCookie)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func requestSessionToken(r *http.Request) (string, bool) {
	if token := extractBearer(r); token != "" {
		return token, false
	}
	if token := browserSessionToken(r); token != "" {
		return token, true
	}
	return "", false
}

func isSafeBrowserMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// validateBrowserOrigin is the CSRF boundary for HttpOnly-cookie requests and
// WebSocket upgrades. Bearer/PAT clients remain independent of browser Origin.
func (h *handlers) validateBrowserOrigin(r *http.Request) bool {
	raw := strings.TrimSpace(r.Header.Get("Origin"))
	if raw == "" {
		return false
	}
	origin, err := url.Parse(raw)
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") ||
		origin.Host == "" || origin.User != nil || origin.Path != "" ||
		origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	want, err := url.Parse(h.effectivePublicBaseURL(r))
	return err == nil && strings.EqualFold(origin.Scheme, want.Scheme) && strings.EqualFold(origin.Host, want.Host)
}

// nativeBrowserLogin gives the first-party webapp a cookie-only login path.
// Mattermost/API clients continue to use /api/v4/users/login and receive the
// existing bearer response contract.
func (h *handlers) nativeBrowserLogin(w http.ResponseWriter, r *http.Request) {
	if !h.validateBrowserOrigin(r) {
		writeError(w, http.StatusForbidden, "api.context.csrf.app_error", "browser request origin is invalid")
		return
	}
	var req loginReq
	if err := decodeSingleJSON(w, r, 32<<10, &req); err != nil {
		writeError(w, http.StatusBadRequest, "api.user.login.invalid_body.app_error", "invalid login request")
		return
	}
	loginID := req.identifier()
	if loginID == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "api.user.login.missing_credentials.app_error", "login id and password are required")
		return
	}
	u, token, err := h.auth.LoginWithDevice(r.Context(), loginID, req.Password, req.DeviceID)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			if h.audit != nil {
				h.audit.LogAsync("", audit.ActionUserLoginFailed, loginID, map[string]any{"ip": h.clientIP(r)})
			}
			writeError(w, http.StatusUnauthorized, "api.user.login.invalid_credentials", "invalid credentials")
			return
		}
		writeError(w, http.StatusInternalServerError, "api.user.login.app_error", "could not create the browser session")
		return
	}
	h.setBrowserSessionCookie(w, r, token)
	w.Header().Set("Cache-Control", "no-store")
	if h.audit != nil {
		h.audit.LogAsync(u.ID, audit.ActionUserLogin, u.Username, map[string]any{"ip": h.clientIP(r), "session_type": "browser"})
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": u})
}

// nativeBrowserAdopt is a one-release-safe migration path for web sessions
// created before browser credentials moved out of sessionStorage. The caller
// must present a valid bearer once; the response rotates browser usage to an
// HttpOnly cookie and never echoes the credential.
func (h *handlers) nativeBrowserAdopt(w http.ResponseWriter, r *http.Request) {
	token := extractBearer(r)
	if token == "" {
		writeError(w, http.StatusBadRequest, "api.moyro.session.bearer_required", "a bearer session is required")
		return
	}
	u, err := h.auth.UserByID(r.Context(), userID(r))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "invalid session")
		return
	}
	h.setBrowserSessionCookie(w, r, token)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}
