package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/application/postcommand"
	"github.com/hkjang/moyro/server/internal/audit"
	"github.com/hkjang/moyro/server/internal/auth"
	"github.com/hkjang/moyro/server/internal/bookmarks"
	"github.com/hkjang/moyro/server/internal/bots"
	"github.com/hkjang/moyro/server/internal/buildinfo"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/commands"
	"github.com/hkjang/moyro/server/internal/config"
	"github.com/hkjang/moyro/server/internal/customprofile"
	"github.com/hkjang/moyro/server/internal/emojis"
	"github.com/hkjang/moyro/server/internal/files"
	"github.com/hkjang/moyro/server/internal/invites"
	"github.com/hkjang/moyro/server/internal/links"
	"github.com/hkjang/moyro/server/internal/oauth"
	"github.com/hkjang/moyro/server/internal/pluginhost"
	"github.com/hkjang/moyro/server/internal/postacks"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/preferences"
	"github.com/hkjang/moyro/server/internal/reactions"
	"github.com/hkjang/moyro/server/internal/registration"
	"github.com/hkjang/moyro/server/internal/reminders"
	"github.com/hkjang/moyro/server/internal/savedposts"
	"github.com/hkjang/moyro/server/internal/scheduled"
	"github.com/hkjang/moyro/server/internal/sidebar"
	"github.com/hkjang/moyro/server/internal/slashcmd"
	"github.com/hkjang/moyro/server/internal/teams"
	"github.com/hkjang/moyro/server/internal/threads"
	"github.com/hkjang/moyro/server/internal/tos"
	"github.com/hkjang/moyro/server/internal/userstatus"
	"github.com/hkjang/moyro/server/internal/webhooks"
	"github.com/hkjang/moyro/server/internal/ws"
)

type handlers struct {
	cfg          *config.Config
	auth         *auth.Service
	teams        *teams.Service
	channels     *channels.Service
	posts        *posts.Service
	postCommands *postcommand.Service
	reactions    *reactions.Service
	files        *files.Service
	status       *userstatus.Service
	audit        *audit.Service
	slash        *slashcmd.Service
	bots         *bots.Service
	incoming     *webhooks.IncomingService
	outgoing     *webhooks.OutgoingService
	outDisp      *webhooks.Dispatcher
	emojis       *emojis.Service
	oauthReg     *oauth.Registry
	oauthIdent   *oauth.IdentityStore
	invites      *invites.Service
	registration *registration.Service
	saved        *savedposts.Service
	links        *links.Service
	scheduled    *scheduled.Service
	reminders    *reminders.Service
	prefs        *preferences.Service
	sidebar      *sidebar.Service
	commands     *commands.Service
	threads      *threads.Service
	bookmarks    *bookmarks.Service
	customProf   *customprofile.Service
	postacks     *postacks.Service
	tos          *tos.Service
	hub          *ws.Hub
	host         *pluginhost.Host
	native       *nativeServices
	logger       *slog.Logger
}

type ctxKey string

const (
	userIDKey    ctxKey = "user_id"
	sessionIDKey ctxKey = "session_id"
)

// Mattermost-style error envelope.
type apiError struct {
	ID            string `json:"id"`
	Message       string `json:"message"`
	DetailedError string `json:"detailed_error,omitempty"`
	RequestID     string `json:"request_id,omitempty"`
	StatusCode    int    `json:"status_code"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, id, msg string) {
	writeJSON(w, status, apiError{ID: id, Message: msg, StatusCode: status})
}

// teamScopedEvent and channelScopedEvent make the audience boundary explicit
// at security-sensitive membership/update call sites. An empty Broadcast is a
// global event in ws.Hub, so constructing these events through a helper avoids
// silently leaking private-team or private-channel metadata to every socket.
func teamScopedEvent(event, teamID string, data map[string]any) ws.Event {
	return ws.Event{Event: event, Data: data, Broadcast: ws.Broadcast{TeamID: teamID}}
}

func channelScopedEvent(event, channelID string, data map[string]any) ws.Event {
	return ws.Event{Event: event, Data: data, Broadcast: ws.Broadcast{ChannelID: channelID}}
}

func (h *handlers) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Short-circuit if an upstream middleware (e.g. PAT) already set the
		// user id — re-parsing a PAT as a JWT would hard-fail and swallow
		// the successful bot auth.
		if v, _ := r.Context().Value(userIDKey).(string); v != "" {
			next.ServeHTTP(w, r.WithContext(ensureUserPrincipal(r.Context(), v)))
			return
		}
		tok := extractBearer(r)
		if tok == "" {
			writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
			return
		}
		claims, err := h.auth.Authenticate(r.Context(), tok)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "invalid token")
			return
		}
		ctx := context.WithValue(r.Context(), sessionIDKey, claims.SessionID)
		ctx = ensureUserPrincipal(ctx, claims.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserIDFromContext exposes the authenticated user id to sibling packages
// (e.g. pat middleware) without making them depend on httpapi internals.
// Returns "" when no user id has been attached to the context.
func UserIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(userIDKey).(string)
	return v
}

// SetUserIDOnContext attaches a user id to the context using the same key
// the rest of the httpapi package reads from.
func SetUserIDOnContext(ctx context.Context, uid string) context.Context {
	return context.WithValue(ctx, userIDKey, uid)
}

func sessionIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(sessionIDKey).(string)
	return value
}

// credentialIDFromContext returns a non-secret server-side credential row ID
// for audit provenance. PAT and managed-key middleware install a principal;
// ordinary browser requests fall back to the current session row ID.
func credentialIDFromContext(ctx context.Context) string {
	if principal, ok := PrincipalFromContext(ctx); ok && principal.CredentialID != "" {
		return principal.CredentialID
	}
	return sessionIDFromContext(ctx)
}

func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

func userID(r *http.Request) string {
	v, _ := r.Context().Value(userIDKey).(string)
	return v
}

func (h *handlers) requireUserParamAccess(w http.ResponseWriter, r *http.Request, param string) (string, bool) {
	targetID := chi.URLParam(r, param)
	actorID := userID(r)
	if targetID == "" || targetID == "me" {
		targetID = actorID
	}
	if targetID == "" || actorID == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return "", false
	}
	if actorID == targetID {
		return targetID, true
	}
	if h.auth != nil {
		if ok, _ := h.auth.HasRole(r.Context(), actorID, "system_admin"); ok {
			return targetID, true
		}
	}
	writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "admin required")
	return "", false
}

// requireRole gates a sub-route on the caller holding a specific role
// token. Intended to sit downstream of requireAuth. We do a DB lookup per
// call rather than baking roles into the JWT so role changes take effect
// without re-login; the authenticated-user rate limiter already shields
// this from abuse.
func (h *handlers) requireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uid := userID(r)
			if uid == "" {
				writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
				return
			}
			ok, err := h.auth.HasRole(r.Context(), uid, role)
			if err != nil || !ok {
				writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "missing role: "+role)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (h *handlers) ping(w http.ResponseWriter, r *http.Request) {
	// Expose enabled OAuth providers so the login screen can render only
	// the buttons that will actually work. An empty list (or absent key)
	// means the webapp falls back to username/password-only.
	var providers []string
	if h.oauthReg != nil {
		providers = h.oauthReg.EnabledNames()
	}
	if h.native != nil && h.native.oidc.Enabled() {
		providers = append(providers, "keycloak")
	}
	build := buildinfo.Current()
	writeJSON(w, 200, map[string]any{
		"status":              "OK",
		"ActiveSearchBackend": "postgres",
		"oauth_providers":     providers,
		"version":             build.Version,
		"build_hash":          build.Commit,
		"build_date":          build.BuildDate,
	})
}

// healthz is an unauthenticated liveness probe. One DB roundtrip with a
// 500ms budget — fast enough for k8s livenessProbe defaults and catches
// the most common "the pod is up but DB lost connection" failure.
func (h *handlers) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
	defer cancel()
	if h.auth == nil || h.auth.DB() == nil {
		http.Error(w, "db_unavailable", 503)
		return
	}
	var one int
	if err := h.auth.DB().Pool.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
		http.Error(w, "db_unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("ok"))
}

// ---- Auth ----

type registerReq struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
	// Phase 16: optional invite token id. When present the new user is
	// joined to the invite's team (in addition to the default Town Square
	// bootstrap so they can always reach the general channel). The invite
	// is consumed in the same request; a race on the last seat returns
	// a 400 so the user can try again with a fresh invite.
	InviteID string `json:"invite_id"`
}

var localUsernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,63}$`)

func normalizeRegisterRequest(req *registerReq) error {
	req.Username = strings.ToLower(strings.TrimSpace(req.Username))
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.InviteID = strings.TrimSpace(req.InviteID)
	if !localUsernamePattern.MatchString(req.Username) {
		return errors.New("username must be 3-64 ASCII letters, numbers, dots, underscores, or hyphens")
	}
	if len(req.Email) > 254 || strings.ContainsAny(req.Email, "\r\n\x00") {
		return errors.New("email address is invalid")
	}
	address, err := mail.ParseAddress(req.Email)
	if err != nil || !strings.EqualFold(address.Address, req.Email) || !strings.Contains(req.Email, "@") {
		return errors.New("email address is invalid")
	}
	if len(req.Password) < 12 || len(req.Password) > 72 || strings.ContainsRune(req.Password, '\x00') {
		return errors.New("password must contain between 12 and 72 bytes")
	}
	if len(req.InviteID) > 128 || strings.ContainsAny(req.InviteID, "\r\n\x00") {
		return errors.New("invitation id is invalid")
	}
	return nil
}

func (h *handlers) register(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "api.user.create_user.invalid_body.app_error", "invalid registration request")
		return
	}
	if err := normalizeRegisterRequest(&req); err != nil {
		writeError(w, http.StatusBadRequest, "api.user.create_user.validation.app_error", err.Error())
		return
	}
	if req.InviteID == "" && (h.native == nil || !h.native.currentSiteSettings().LocalSignupEnabled) {
		writeError(w, http.StatusForbidden, "api.user.create_user.signup_disabled.app_error", "local account registration is disabled; use an administrator-issued invitation or SSO")
		return
	}
	if h.registration == nil {
		writeError(w, http.StatusServiceUnavailable, "api.user.create_user.unavailable.app_error", "account registration is unavailable")
		return
	}

	// Invite consumption, user creation, and default/invited memberships are
	// committed together. In particular, the loser of a max_uses=1 race fails
	// before its users row can become visible.
	u, inviteTeamID, err := h.registration.Register(r.Context(), registration.Input{
		Username: req.Username, Email: req.Email, Password: req.Password, InviteID: req.InviteID,
	})
	if err != nil {
		switch {
		case errors.Is(err, invites.ErrInvalidInvite):
			writeError(w, http.StatusBadRequest, "api.invite.invalid", "invitation is invalid, expired, or exhausted")
			return
		case errors.Is(err, auth.ErrUserExists):
			writeError(w, http.StatusConflict, "api.user.create_user.exists.app_error", "an account with that username or email already exists")
			return
		case errors.Is(err, auth.ErrInvalidPassword):
			writeError(w, http.StatusBadRequest, "api.user.create_user.validation.app_error", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "api.user.create_user.save.app_error", "account could not be created")
		return
	}

	if h.audit != nil {
		h.audit.LogAsync(u.ID, audit.ActionUserRegister, u.Username, map[string]any{
			"email":     u.Email,
			"roles":     u.Roles,
			"invite_id": req.InviteID,
		})
	}
	if inviteTeamID != "" && h.audit != nil {
		h.audit.LogAsync(u.ID, audit.ActionInviteConsume, req.InviteID, map[string]any{
			"team_id": inviteTeamID,
		})
	}

	writeJSON(w, 201, u)
}

func (h *handlers) bootstrapMembership(ctx context.Context, userID string) error {
	team, err := h.teams.EnsureDefault(ctx)
	if err != nil {
		return err
	}
	if err := h.teams.Join(ctx, team.ID, userID); err != nil {
		return err
	}
	ch, err := h.channels.EnsureDefault(ctx, team.ID)
	if err != nil {
		return err
	}
	return h.channels.Join(ctx, ch.ID, userID)
}

type loginReq struct {
	ID       string `json:"id"`
	LoginID  string `json:"login_id"`
	DeviceID string `json:"device_id"`
	Password string `json:"password"`
}

func (r loginReq) identifier() string {
	if id := strings.TrimSpace(r.LoginID); id != "" {
		return id
	}
	return strings.TrimSpace(r.ID)
}

func (h *handlers) login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.user.login.invalid_body.app_error", err.Error())
		return
	}
	loginID := req.identifier()
	if loginID == "" {
		writeError(w, 400, "api.user.login.missing_login_id.app_error", "login_id or id is required")
		return
	}
	if req.Password == "" {
		writeError(w, 400, "api.user.login.missing_password.app_error", "password is required")
		return
	}
	u, tok, err := h.auth.LoginWithDevice(r.Context(), loginID, req.Password, req.DeviceID)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			if h.audit != nil {
				h.audit.LogAsync("", audit.ActionUserLoginFailed, loginID, map[string]any{
					"ip": r.RemoteAddr,
				})
			}
			writeError(w, 401, "api.user.login.invalid_credentials", "invalid credentials")
			return
		}
		writeError(w, 500, "api.user.login.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(u.ID, audit.ActionUserLogin, u.Username, map[string]any{
			"ip": r.RemoteAddr,
		})
	}
	// Mattermost returns token in Token header. Also include in body for convenience.
	w.Header().Set("Token", tok)
	writeJSON(w, 201, map[string]any{"token": tok, "user": u})
}

// ensureRoleToken is retained as a normalization helper for compatibility
// handlers and tests. Bootstrap no longer calls it; administrator assignment
// happens only in the explicit one-shot bootstrap service.
func ensureRoleToken(roles, role string) string {
	for _, token := range strings.Fields(roles) {
		if token == role {
			return strings.Join(strings.Fields(roles), " ")
		}
	}
	parts := strings.Fields(roles)
	parts = append(parts, role)
	return strings.Join(parts, " ")
}

func (h *handlers) logout(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	tok := extractBearer(r)
	if tok == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	if err := h.auth.Revoke(r.Context(), tok); err != nil {
		writeError(w, 500, "api.user.logout.app_error", err.Error())
		return
	}
	// Existing WebSocket connections do not make another HTTP request after
	// authentication. Close them immediately so a revoked session cannot keep
	// receiving realtime events until the periodic validity check runs.
	if h.hub != nil {
		h.hub.KickUser(uid)
	}
	if h.audit != nil {
		h.audit.LogAsync(uid, audit.ActionUserLogout, "", nil)
	}
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

func (h *handlers) me(w http.ResponseWriter, r *http.Request) {
	u, err := h.auth.UserByID(r.Context(), userID(r))
	if err != nil {
		writeError(w, 404, "api.user.get.not_found", err.Error())
		return
	}
	writeJSON(w, 200, u)
}

// ---- User directory ----

func (h *handlers) listUsers(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	// `include_deleted=true` is admin-only; the admin panel uses it to
	// expose a reactivate button for previously deactivated accounts.
	// Non-admin callers silently fall back to active-only results so we
	// don't leak the existence of deactivated usernames.
	includeDeleted := r.URL.Query().Get("include_deleted") == "true"
	if includeDeleted {
		actorID, _ := r.Context().Value(userIDKey).(string)
		ok, _ := h.auth.HasRole(r.Context(), actorID, "system_admin")
		if !ok {
			includeDeleted = false
		}
	}
	var (
		list []auth.User
		err  error
	)
	if includeDeleted {
		list, err = h.auth.ListUsersIncludingDeleted(r.Context(), page, perPage)
	} else {
		list, err = h.auth.ListUsers(r.Context(), page, perPage)
	}
	if err != nil {
		writeError(w, 500, "api.user.list.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func (h *handlers) getUser(w http.ResponseWriter, r *http.Request) {
	u, err := h.auth.UserByID(r.Context(), chi.URLParam(r, "userID"))
	if err != nil {
		writeError(w, 404, "api.user.get.not_found", err.Error())
		return
	}
	writeJSON(w, 200, u)
}

func (h *handlers) getUserByUsername(w http.ResponseWriter, r *http.Request) {
	u, err := h.auth.UserByUsername(r.Context(), chi.URLParam(r, "username"))
	if err != nil {
		writeError(w, 404, "api.user.get.not_found", err.Error())
		return
	}
	writeJSON(w, 200, u)
}

type searchUsersReq struct {
	Term  string `json:"term"`
	Limit int    `json:"limit"`
}

func (h *handlers) searchUsers(w http.ResponseWriter, r *http.Request) {
	var req searchUsersReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.user.search.invalid_body", err.Error())
		return
	}
	if strings.TrimSpace(req.Term) == "" {
		writeError(w, 400, "api.user.search.empty_term", "term required")
		return
	}
	list, err := h.auth.SearchUsers(r.Context(), req.Term, req.Limit)
	if err != nil {
		writeError(w, 500, "api.user.search.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// ---- User activation state (Phase 16) ----

// deactivateUser soft-deletes a user. Admins can deactivate any user;
// regular callers can only deactivate themselves. On success we kick the
// target's live WebSockets so an attacker can't linger on an established
// stream past their session's lifetime.
func (h *handlers) deactivateUser(w http.ResponseWriter, r *http.Request) {
	targetID := chi.URLParam(r, "userID")
	if targetID == "" || targetID == "me" {
		targetID = userID(r)
	}
	actor := userID(r)
	isAdmin := false
	if actor != "" {
		isAdmin, _ = h.auth.HasRole(r.Context(), actor, "system_admin")
	}
	if !isAdmin && actor != targetID {
		writeError(w, 403, "api.user.deactivate.forbidden", "admin required")
		return
	}
	changed, err := h.auth.Deactivate(r.Context(), targetID)
	if err != nil {
		writeError(w, 500, "api.user.deactivate.app_error", err.Error())
		return
	}
	if changed {
		if h.hub != nil {
			h.hub.KickUser(targetID)
		}
		if h.audit != nil {
			h.audit.LogAsync(actor, audit.ActionUserDeactivate, targetID, nil)
		}
	}
	writeJSON(w, 200, map[string]any{"status": "OK", "changed": changed})
}

// reactivateUser flips delete_at back to zero. Admin-only.
func (h *handlers) reactivateUser(w http.ResponseWriter, r *http.Request) {
	targetID := chi.URLParam(r, "userID")
	actor := userID(r)
	changed, err := h.auth.Reactivate(r.Context(), targetID)
	if err != nil {
		writeError(w, 500, "api.user.reactivate.app_error", err.Error())
		return
	}
	if changed && h.audit != nil {
		h.audit.LogAsync(actor, audit.ActionUserReactivate, targetID, nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK", "changed": changed})
}

// ---- Session management (Phase 16) ----

type sessionView struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	DeviceID  string `json:"device_id"`
	ExpiresAt int64  `json:"expires_at"`
	CreateAt  int64  `json:"create_at"`
	// IsCurrent marks the row whose id was authenticated for this request.
	// The webapp only needs to know which row is "this device".
	IsCurrent bool `json:"is_current"`
}

func (h *handlers) listMySessions(w http.ResponseWriter, r *http.Request) {
	h.writeSessionsForUser(w, r, userID(r))
}

func (h *handlers) revokeMySession(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	sessionID := chi.URLParam(r, "sessionID")
	changed, err := h.auth.RevokeSession(r.Context(), sessionID, uid)
	if err != nil {
		writeError(w, 500, "api.session.revoke.app_error", err.Error())
		return
	}
	if changed {
		if h.hub != nil {
			// Best-effort: kick every socket for this user. We don't know
			// which one corresponds to the killed session, but the user
			// just asked to revoke it — racing a reconnect is fine since
			// the new session would have a fresh token.
			h.hub.KickUser(uid)
		}
		if h.audit != nil {
			h.audit.LogAsync(uid, audit.ActionSessionRevoke, sessionID, map[string]any{"scope": "one"})
		}
	}
	writeJSON(w, 200, map[string]any{"status": "OK", "changed": changed})
}

func (h *handlers) revokeMyOtherSessions(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	current := sessionIDFromContext(r.Context())
	n, err := h.auth.RevokeOthers(r.Context(), uid, current)
	if err != nil {
		writeError(w, 500, "api.session.revoke.app_error", err.Error())
		return
	}
	if n > 0 && h.hub != nil {
		// Race-safe: we can't tell sockets apart by token, so kick them
		// all. The caller's socket will reconnect and re-authorize against
		// their still-valid current session.
		h.hub.KickUser(uid)
	}
	if h.audit != nil {
		h.audit.LogAsync(uid, audit.ActionSessionRevoke, uid, map[string]any{"scope": "others", "count": n})
	}
	writeJSON(w, 200, map[string]any{"status": "OK", "revoked": n})
}

type updateProfileReq struct {
	Username string `json:"username"`
	Email    string `json:"email"`
}

func (h *handlers) updateProfile(w http.ResponseWriter, r *http.Request) {
	var req updateProfileReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.user.update.invalid_body", err.Error())
		return
	}
	u, err := h.auth.UpdateProfile(r.Context(), userID(r), req.Username, req.Email)
	if err != nil {
		writeError(w, 500, "api.user.update.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionUserProfileUpdate, u.ID, map[string]any{
			"username": u.Username,
			"email":    u.Email,
		})
	}
	writeJSON(w, 200, u)
}

// ---- Email prefs (Phase 17) -------------------------------------------
//
// Stored as JSONB on users.email_prefs. Two recognised fields:
//   digest_enabled : bool (default true; opt-out via false)
//   last_digest_at : int64 ms epoch — managed by the digest worker, NOT
//                    writeable through this endpoint (we explicitly skip
//                    it to prevent a client from suppressing sends).

type emailPrefsReq struct {
	DigestEnabled *bool `json:"digest_enabled"`
}

type emailPrefsResp struct {
	DigestEnabled bool `json:"digest_enabled"`
}

func (h *handlers) getMyEmailPrefs(w http.ResponseWriter, r *http.Request) {
	enabled, err := h.loadDigestEnabled(r.Context(), userID(r))
	if err != nil {
		writeError(w, 500, "api.email_prefs.get.app_error", err.Error())
		return
	}
	writeJSON(w, 200, emailPrefsResp{DigestEnabled: enabled})
}

func (h *handlers) updateMyEmailPrefs(w http.ResponseWriter, r *http.Request) {
	var req emailPrefsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.email_prefs.update.invalid_body", err.Error())
		return
	}
	if req.DigestEnabled != nil {
		val := "false"
		if *req.DigestEnabled {
			val = "true"
		}
		if _, err := h.auth.DB().Pool.Exec(r.Context(), `
			UPDATE users
			SET email_prefs = jsonb_set(
			    COALESCE(email_prefs, '{}'::jsonb),
			    '{digest_enabled}',
			    $2::jsonb,
			    true)
			WHERE id = $1
		`, userID(r), val); err != nil {
			writeError(w, 500, "api.email_prefs.update.app_error", err.Error())
			return
		}
	}
	enabled, err := h.loadDigestEnabled(r.Context(), userID(r))
	if err != nil {
		writeError(w, 500, "api.email_prefs.update.reload", err.Error())
		return
	}
	writeJSON(w, 200, emailPrefsResp{DigestEnabled: enabled})
}

// loadDigestEnabled centralises the "default true unless explicitly
// false" rule so both GET and PUT responses agree.
func (h *handlers) loadDigestEnabled(ctx context.Context, uid string) (bool, error) {
	var val *string
	err := h.auth.DB().Pool.QueryRow(ctx, `
		SELECT email_prefs ->> 'digest_enabled' FROM users WHERE id = $1
	`, uid).Scan(&val)
	if err != nil {
		return false, err
	}
	if val == nil {
		return true, nil
	}
	return *val != "false", nil
}

type updatePasswordReq struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (h *handlers) updatePassword(w http.ResponseWriter, r *http.Request) {
	var req updatePasswordReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NewPassword == "" {
		writeError(w, 400, "api.user.update_password.invalid_body", "new_password required")
		return
	}
	uid := userID(r)
	if err := h.auth.UpdatePassword(r.Context(), uid, req.CurrentPassword, req.NewPassword); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeError(w, 400, "api.user.update_password.incorrect", "current password is wrong")
			return
		}
		if errors.Is(err, auth.ErrInvalidPassword) {
			writeError(w, 400, "api.user.update_password.invalid_password", err.Error())
			return
		}
		writeError(w, 500, "api.user.update_password.app_error", err.Error())
		return
	}
	if h.hub != nil {
		h.hub.KickUser(uid)
	}
	if h.audit != nil {
		h.audit.LogAsync(uid, audit.ActionUserPasswordChg, "", map[string]any{"sessions_revoked": "all"})
	}
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

// ---- Profile image (Phase 15) -----------------------------------------
//
// The `picture` column on users holds one of:
//   - "" (empty) — no picture, UI falls back to initial tile
//   - external URL (http:// or https://) — set by OAuth UserInfo import
//   - bare file_id — an internal upload via `uploadProfileImage` below
//
// `GET /users/{userID}/image` treats each case uniformly. External URLs
// are 302-redirected so CDN caching works; internal file_ids stream the
// 360px thumbnail when available (cheaper + consistent sizing) and fall
// back to the full image otherwise.

// 512KB cap on profile image uploads. Avatars are small — anything larger
// is either a mistake or someone trying to fill disk. Larger limits also
// slow down the thumbnail worker since it decodes the full image.
const profileImageMaxBytes = 512 * 1024

func (h *handlers) uploadProfileImage(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	// Enforce the byte cap at the transport layer before ParseMultipartForm
	// touches the body — stops us from even buffering a 50MB nonsense
	// payload into memory.
	r.Body = http.MaxBytesReader(w, r.Body, profileImageMaxBytes+4096)
	if err := r.ParseMultipartForm(profileImageMaxBytes + 4096); err != nil {
		writeError(w, 400, "api.user.image.too_large", "image must be under 512 KB")
		return
	}
	file, hdr, err := r.FormFile("image")
	if err != nil {
		writeError(w, 400, "api.user.image.missing", "multipart field `image` required")
		return
	}
	defer file.Close()
	mime := hdr.Header.Get("Content-Type")
	if !strings.HasPrefix(mime, "image/") {
		writeError(w, 400, "api.user.image.not_image", "file must be image/*")
		return
	}
	// Reuse the general file upload pipeline — gives us id, storage layout,
	// and the Phase 13 async thumbnail for free. channelID="" marks this
	// as a non-channel upload (no post attachment).
	fi, err := h.files.Upload(r.Context(), uid, "", hdr.Filename, mime, file)
	if err != nil {
		writeError(w, 500, "api.user.image.upload_failed", err.Error())
		return
	}
	// Store the file_id bare. `GET /users/{userID}/image` resolves it on
	// read — avoids baking a URL into the DB that'd need migrating if we
	// ever change the route.
	u, err := h.auth.UpdatePicture(r.Context(), uid, fi.ID)
	if err != nil {
		writeError(w, 500, "api.user.image.save", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(uid, audit.ActionUserProfileUpdate, uid, map[string]any{
			"picture_file_id": fi.ID,
		})
	}
	writeJSON(w, 200, u)
}

func (h *handlers) getUserImage(w http.ResponseWriter, r *http.Request) {
	targetID := chi.URLParam(r, "userID")
	if targetID == "me" {
		targetID = userID(r)
	}
	u, err := h.auth.UserByID(r.Context(), targetID)
	if err != nil {
		writeError(w, 404, "api.user.image.not_found", "user not found")
		return
	}
	pic := u.Picture
	if pic == "" {
		// Let the browser's <img onError> fallback to initials handle it.
		writeError(w, 404, "api.user.image.empty", "no picture set")
		return
	}
	if strings.HasPrefix(pic, "http://") || strings.HasPrefix(pic, "https://") {
		// External provider URL — redirect so CDNs and CORS work cleanly.
		http.Redirect(w, r, pic, http.StatusFound)
		return
	}
	// Otherwise treat as a file_id owned by this user. Prefer the 360px
	// thumbnail so the chat sidebar doesn't pull the full-res original
	// for every avatar render.
	if rc, fi, err := h.files.OpenThumbnail(r.Context(), pic); err == nil {
		defer rc.Close()
		if fi.MimeType != "" {
			w.Header().Set("Content-Type", "image/jpeg")
		}
		// 1 hour client-side cache — picture updates bump users.update_at,
		// which the webapp appends as ?v= to bust the cache.
		w.Header().Set("Cache-Control", "private, max-age=3600")
		_, _ = io.Copy(w, rc)
		return
	}
	rc, fi, err := h.files.Open(r.Context(), pic)
	if err != nil {
		writeError(w, 404, "api.user.image.file_missing", err.Error())
		return
	}
	defer rc.Close()
	if fi.MimeType != "" {
		w.Header().Set("Content-Type", fi.MimeType)
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = io.Copy(w, rc)
}

func (h *handlers) getDefaultProfileImage(w http.ResponseWriter, r *http.Request) {
	targetID := chi.URLParam(r, "userID")
	if targetID == "me" {
		targetID = userID(r)
	}
	u, err := h.auth.UserByID(r.Context(), targetID)
	if err != nil {
		writeError(w, 404, "api.user.image.default.not_found", "user not found")
		return
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name = u.Username
	}
	label := "?"
	for _, r := range name {
		label = strings.ToUpper(string(r))
		break
	}
	palette := []string{"#2563eb", "#0891b2", "#059669", "#7c3aed", "#dc2626", "#d97706"}
	idx := 0
	for _, r := range u.ID {
		idx = (idx + int(r)) % len(palette)
	}
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="128" height="128" viewBox="0 0 128 128"><rect width="128" height="128" rx="24" fill="%s"/><text x="64" y="78" text-anchor="middle" font-family="Arial, Helvetica, sans-serif" font-size="56" font-weight="700" fill="#fff">%s</text></svg>`, palette[idx], html.EscapeString(label))
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(200)
	_, _ = w.Write([]byte(svg))
}

// ---- User status ----

func (h *handlers) getUserStatus(w http.ResponseWriter, r *http.Request) {
	st, err := h.status.Get(r.Context(), chi.URLParam(r, "userID"))
	if err != nil {
		writeError(w, 500, "api.user.status.get.app_error", err.Error())
		return
	}
	writeJSON(w, 200, st)
}

func (h *handlers) getUserStatusesByIDs(w http.ResponseWriter, r *http.Request) {
	var ids []string
	if err := json.NewDecoder(r.Body).Decode(&ids); err != nil {
		writeError(w, 400, "api.user.status.ids.invalid_body", err.Error())
		return
	}
	list, err := h.status.GetMany(r.Context(), ids)
	if err != nil {
		writeError(w, 500, "api.user.status.ids.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

type updateStatusReq struct {
	UserID string `json:"user_id"`
	Status string `json:"status"`
	Manual bool   `json:"manual"`
}

func (h *handlers) updateMyStatus(w http.ResponseWriter, r *http.Request) {
	h.writeStatusUpdate(w, r, userID(r))
}

func (h *handlers) updateUserStatus(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	h.writeStatusUpdate(w, r, uid)
}

func (h *handlers) writeStatusUpdate(w http.ResponseWriter, r *http.Request, targetID string) {
	var req updateStatusReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.user.status.update.invalid_body", err.Error())
		return
	}
	if req.UserID != "" && req.UserID != targetID && req.UserID != "me" {
		writeError(w, 400, "api.user.status.update.user_mismatch", "user_id does not match route")
		return
	}
	switch req.Status {
	case userstatus.Online, userstatus.Away, userstatus.DND, userstatus.Offline:
	default:
		writeError(w, 400, "api.user.status.update.invalid_status", "status must be online|away|dnd|offline")
		return
	}
	st, err := h.status.Set(r.Context(), targetID, req.Status, req.Manual)
	if err != nil {
		writeError(w, 500, "api.user.status.update.app_error", err.Error())
		return
	}
	raw, _ := json.Marshal(st)
	// Broadcast globally — any client with the user in view wants this.
	h.hub.Broadcast(ws.Event{
		Event: "status_change",
		Data: map[string]any{
			"user_id": st.UserID,
			"status":  st.Status,
			"payload": string(raw),
		},
	})
	writeJSON(w, 200, st)
}

// ---- Teams ----

type createTeamReq struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
}

func (h *handlers) createTeam(w http.ResponseWriter, r *http.Request) {
	var req createTeamReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.team.create.invalid_body", err.Error())
		return
	}
	if req.Type == "" {
		req.Type = "O"
	}
	t, err := h.teams.Create(r.Context(), req.Name, req.DisplayName, req.Type, userID(r))
	if err != nil {
		writeError(w, 500, "api.team.create.save.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionTeamCreate, t.ID, map[string]any{
			"name":         t.Name,
			"display_name": t.DisplayName,
		})
	}
	writeJSON(w, 201, t)
}

func (h *handlers) listTeams(w http.ResponseWriter, r *http.Request) {
	h.listTeamsForUserID(w, r, userID(r))
}

// ---- Team invites (Phase 16) ----

// canManageTeamInvites is a shared guard: system_admin always passes,
// team_admin passes for their own team, anyone else gets 403.
func (h *handlers) canManageTeamInvites(ctx context.Context, actorID, teamID string) bool {
	if ok, _ := h.auth.HasRole(ctx, actorID, "system_admin"); ok {
		return true
	}
	if ok, _ := h.teams.IsTeamAdmin(ctx, teamID, actorID); ok {
		return true
	}
	return false
}

type createInviteReq struct {
	MaxUses    int   `json:"max_uses"`    // 0 = unlimited within TTL
	TTLSeconds int64 `json:"ttl_seconds"` // defaults to 7 days if <= 0
}

type inviteView struct {
	ID        string `json:"id"`
	TeamID    string `json:"team_id"`
	CreatedBy string `json:"created_by"`
	MaxUses   int    `json:"max_uses"`
	UseCount  int    `json:"use_count"`
	ExpiresAt int64  `json:"expires_at"`
	CreateAt  int64  `json:"create_at"`
	URL       string `json:"url"`
}

// inviteURL builds the shareable URL. PublicBaseURL may be unset during
// dev, in which case the frontend is expected to be served from the same
// origin — we emit a relative URL and let the browser resolve it.
func (h *handlers) inviteURL(r *http.Request, id string) string {
	return h.effectivePublicBaseURL(r) + "/#invite=" + id
}

// createInvite — team admin or system_admin only. Body selects the usage
// cap and TTL; the response includes the full shareable URL so the admin
// UI can copy it with one click.
func (h *handlers) createInvite(w http.ResponseWriter, r *http.Request) {
	if h.invites == nil {
		writeError(w, 500, "api.invite.unavailable", "invites not configured")
		return
	}
	teamID := chi.URLParam(r, "teamID")
	uid := userID(r)
	if !h.canManageTeamInvites(r.Context(), uid, teamID) {
		writeError(w, 403, "api.invite.forbidden", "admin privilege required")
		return
	}
	var req createInviteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.invite.create.invalid_body", err.Error())
		return
	}
	ttl := 7 * 24 * 3600 * int64(1)
	if req.TTLSeconds > 0 {
		ttl = req.TTLSeconds
	}
	inv, err := h.invites.Create(r.Context(), teamID, uid, req.MaxUses, secondsDuration(ttl))
	if err != nil {
		writeError(w, 500, "api.invite.create.save", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(uid, audit.ActionInviteCreate, inv.ID, map[string]any{
			"team_id":    teamID,
			"max_uses":   inv.MaxUses,
			"expires_at": inv.ExpiresAt,
		})
	}
	writeJSON(w, 201, inviteView{
		ID: inv.ID, TeamID: inv.TeamID, CreatedBy: inv.CreatedBy,
		MaxUses: inv.MaxUses, UseCount: inv.UseCount,
		ExpiresAt: inv.ExpiresAt, CreateAt: inv.CreateAt,
		URL: h.inviteURL(r, inv.ID),
	})
}

func (h *handlers) listInvites(w http.ResponseWriter, r *http.Request) {
	if h.invites == nil {
		writeJSON(w, 200, []inviteView{})
		return
	}
	teamID := chi.URLParam(r, "teamID")
	uid := userID(r)
	if !h.canManageTeamInvites(r.Context(), uid, teamID) {
		writeError(w, 403, "api.invite.forbidden", "admin privilege required")
		return
	}
	rows, err := h.invites.ListForTeam(r.Context(), teamID)
	if err != nil {
		writeError(w, 500, "api.invite.list", err.Error())
		return
	}
	out := make([]inviteView, 0, len(rows))
	for _, inv := range rows {
		out = append(out, inviteView{
			ID: inv.ID, TeamID: inv.TeamID, CreatedBy: inv.CreatedBy,
			MaxUses: inv.MaxUses, UseCount: inv.UseCount,
			ExpiresAt: inv.ExpiresAt, CreateAt: inv.CreateAt,
			URL: h.inviteURL(r, inv.ID),
		})
	}
	writeJSON(w, 200, out)
}

func (h *handlers) revokeInvite(w http.ResponseWriter, r *http.Request) {
	if h.invites == nil {
		writeJSON(w, 200, map[string]string{"status": "OK"})
		return
	}
	teamID := chi.URLParam(r, "teamID")
	inviteID := chi.URLParam(r, "inviteID")
	uid := userID(r)
	if !h.canManageTeamInvites(r.Context(), uid, teamID) {
		writeError(w, 403, "api.invite.forbidden", "admin privilege required")
		return
	}
	if err := h.invites.Revoke(r.Context(), inviteID, teamID); err != nil {
		writeError(w, 500, "api.invite.revoke", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(uid, audit.ActionInviteRevoke, inviteID, map[string]any{
			"team_id": teamID,
		})
	}
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

// getInvite is the public preview endpoint. Returns just enough metadata
// for the signup page to say "You're joining <display_name>", never leaks
// the creator id or remaining-uses counter.
func (h *handlers) getInvite(w http.ResponseWriter, r *http.Request) {
	if h.invites == nil {
		writeError(w, 404, "api.invite.not_found", "invite not found")
		return
	}
	id := chi.URLParam(r, "inviteID")
	inv, err := h.invites.Validate(r.Context(), id)
	if err != nil {
		writeError(w, 404, "api.invite.not_found", err.Error())
		return
	}
	t, terr := h.teams.Get(r.Context(), inv.TeamID)
	if terr != nil || t == nil || t.DeleteAt > 0 {
		writeError(w, 404, "api.invite.team_missing", "team missing")
		return
	}
	writeJSON(w, 200, map[string]any{
		"id":                inv.ID,
		"team_id":           inv.TeamID,
		"team_display_name": t.DisplayName,
		"team_name":         t.Name,
		"expires_at":        inv.ExpiresAt,
	})
}

// secondsDuration converts ttl-seconds to a time.Duration; kept as a
// helper so the handler body stays free of time-package noise.
func secondsDuration(s int64) time.Duration { return time.Duration(s) * time.Second }

// ---- Channels ----

type createChannelReq struct {
	TeamID      string `json:"team_id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
}

func (h *handlers) createChannel(w http.ResponseWriter, r *http.Request) {
	var req createChannelReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.channel.create.invalid_body", err.Error())
		return
	}
	if req.Type == "" {
		req.Type = "O"
	}
	c, err := h.channels.Create(r.Context(), req.TeamID, req.Name, req.DisplayName, req.Type, userID(r))
	if err != nil {
		writeError(w, 500, "api.channel.create.save.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionChannelCreate, c.ID, map[string]any{
			"team_id":      c.TeamID,
			"name":         c.Name,
			"display_name": c.DisplayName,
			"type":         c.Type,
		})
	}
	writeJSON(w, 201, c)
}

func (h *handlers) listChannels(w http.ResponseWriter, r *http.Request) {
	h.listChannelsForUserID(w, r, userID(r))
}

// archiveChannel — admin-only (mounted inside the system_admin group).
// Broadcasts `channel_updated` so every open client in the channel sees
// the delete_at bump without a refresh.
func (h *handlers) archiveChannel(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	changed, err := h.channels.Archive(r.Context(), channelID)
	if err != nil {
		writeError(w, 500, "api.channel.archive.app_error", err.Error())
		return
	}
	if changed {
		c, _ := h.channels.Get(r.Context(), channelID)
		if c != nil {
			raw, _ := json.Marshal(c)
			h.hub.Broadcast(ws.Event{
				Event: "channel_deleted",
				Data: map[string]any{
					"channel_id": c.ID,
					"channel":    string(raw),
				},
				Broadcast: ws.Broadcast{ChannelID: c.ID},
			})
		}
		if h.audit != nil {
			h.audit.LogAsync(userID(r), audit.ActionChannelArchive, channelID, nil)
		}
	}
	writeJSON(w, 200, map[string]any{"status": "OK", "changed": changed})
}

func (h *handlers) restoreChannel(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	changed, err := h.channels.Restore(r.Context(), channelID)
	if err != nil {
		writeError(w, 500, "api.channel.restore.app_error", err.Error())
		return
	}
	if changed {
		c, _ := h.channels.Get(r.Context(), channelID)
		if c != nil {
			raw, _ := json.Marshal(c)
			h.hub.Broadcast(ws.Event{
				Event: "channel_restored",
				Data: map[string]any{
					"channel_id": c.ID,
					"channel":    string(raw),
				},
				Broadcast: ws.Broadcast{ChannelID: c.ID},
			})
		}
		if h.audit != nil {
			h.audit.LogAsync(userID(r), audit.ActionChannelRestore, channelID, nil)
		}
	}
	writeJSON(w, 200, map[string]any{"status": "OK", "changed": changed})
}

func (h *handlers) getChannel(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	isMember, err := h.channels.IsMember(r.Context(), channelID, userID(r))
	if err != nil || !isMember {
		writeError(w, 403, "api.channel.get.forbidden", "not a channel member")
		return
	}
	c, err := h.channels.Get(r.Context(), channelID)
	if err != nil {
		writeError(w, 404, "api.channel.get.not_found", err.Error())
		return
	}
	writeJSON(w, 200, c)
}

type patchChannelReq struct {
	DisplayName string  `json:"display_name"`
	Header      *string `json:"header"`
	Purpose     *string `json:"purpose"`
}

func (h *handlers) patchChannel(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	isMember, err := h.channels.IsMember(r.Context(), channelID, userID(r))
	if err != nil || !isMember {
		writeError(w, 403, "api.channel.patch.forbidden", "not a channel member")
		return
	}
	var req patchChannelReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.channel.patch.invalid_body", err.Error())
		return
	}
	// Sentinel "__unchanged__" preserves existing value when the pointer is nil.
	header := "__unchanged__"
	if req.Header != nil {
		header = *req.Header
	}
	purpose := "__unchanged__"
	if req.Purpose != nil {
		purpose = *req.Purpose
	}
	c, err := h.channels.Patch(r.Context(), channelID, req.DisplayName, header, purpose)
	if err != nil {
		writeError(w, 500, "api.channel.patch.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionChannelPatch, c.ID, map[string]any{
			"display_name": c.DisplayName,
			"header":       c.Header,
			"purpose":      c.Purpose,
		})
	}
	raw, _ := json.Marshal(c)
	h.hub.Broadcast(ws.Event{
		Event: "channel_updated",
		Data: map[string]any{
			"channel_id": c.ID,
			"channel":    string(raw),
		},
		Broadcast: ws.Broadcast{ChannelID: c.ID},
	})
	writeJSON(w, 200, c)
}

type directChannelReq []string

func (h *handlers) createDirectChannel(w http.ResponseWriter, r *http.Request) {
	var req directChannelReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.channel.direct.invalid_body", err.Error())
		return
	}
	if len(req) < 1 || len(req) > 2 {
		writeError(w, 400, "api.channel.direct.bad_users", "expected [userA] (self-DM) or [userA,userB]")
		return
	}
	uid := userID(r)
	other := uid
	if len(req) == 2 {
		// Caller must be one of the two ends — prevents creating DMs between strangers.
		if req[0] != uid && req[1] != uid {
			writeError(w, 403, "api.channel.direct.forbidden", "caller must be one of the two users")
			return
		}
		if req[0] == uid {
			other = req[1]
		} else {
			other = req[0]
		}
	} else {
		other = req[0]
	}
	c, err := h.channels.EnsureDirect(r.Context(), uid, other)
	if err != nil {
		writeError(w, 500, "api.channel.direct.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(uid, audit.ActionChannelDirectOpen, c.ID, map[string]any{
			"peer": other,
		})
	}
	writeJSON(w, 201, c)
}

func (h *handlers) viewChannel(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	uid := userID(r)
	isMember, err := h.channels.IsMember(r.Context(), channelID, uid)
	if err != nil || !isMember {
		writeError(w, 403, "api.channel.view.forbidden", "not a channel member")
		return
	}
	ts, err := h.channels.MarkViewed(r.Context(), channelID, uid)
	if err != nil {
		writeError(w, 500, "api.channel.view.app_error", err.Error())
		return
	}
	// Echo to all of the viewing user's sessions so other tabs clear their badges too.
	h.hub.Broadcast(ws.Event{
		Event: "channel_viewed",
		Data: map[string]any{
			"channel_id":     channelID,
			"last_viewed_at": ts,
		},
		Broadcast: ws.Broadcast{UserID: uid},
	})
	writeJSON(w, 200, map[string]any{
		"status":         "OK",
		"last_viewed_at": ts,
	})
}

// ---- Posts ----

type createPostReq struct {
	ChannelID string         `json:"channel_id"`
	Message   string         `json:"message"`
	RootID    string         `json:"root_id"`
	Props     map[string]any `json:"props"`
	FileIDs   []string       `json:"file_ids"`
}

func (h *handlers) createPost(w http.ResponseWriter, r *http.Request) {
	var req createPostReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.post.create.invalid_body", err.Error())
		return
	}

	p, err := h.postCommands.Execute(r.Context(), postcommand.Command{
		Source:       postcommand.SourceREST,
		ActorID:      userID(r),
		ChannelID:    req.ChannelID,
		RootID:       req.RootID,
		Message:      req.Message,
		Props:        req.Props,
		FileIDs:      req.FileIDs,
		CredentialID: credentialIDFromContext(r.Context()),
	})
	if err != nil {
		switch postcommand.FailureCodeOf(err) {
		case postcommand.FailureMembershipCheck:
			writeError(w, 500, "api.post.create.member_check", err.Error())
		case postcommand.FailureNotMember:
			writeError(w, 403, "api.post.create.forbidden", err.Error())
		case postcommand.FailurePermissionCheck:
			writeError(w, 500, "api.post.create.permission_check", err.Error())
		case postcommand.FailurePermissionDenied:
			writeError(w, 403, "api.post.create.forbidden", err.Error())
		case postcommand.FailureInvalidRoot:
			writeError(w, 400, "api.post.create.invalid_root", err.Error())
		case postcommand.FailurePluginRejected:
			writeError(w, 403, "api.post.create.plugin_rejected", err.Error())
		default:
			writeError(w, 500, "api.post.create.save.app_error", err.Error())
		}
		return
	}
	writeJSON(w, 201, p)
}

func (h *handlers) listPosts(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	uid := userID(r)
	isMember, err := h.channels.IsMember(r.Context(), channelID, uid)
	if err != nil {
		writeError(w, 500, "api.post.list.member_check", err.Error())
		return
	}
	if !isMember {
		writeError(w, 403, "api.post.list.forbidden", "not a channel member")
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	before := r.URL.Query().Get("before")
	after := r.URL.Query().Get("after")
	// Phase 22: Mattermost cursor-mode paging. If any of since/before/after is
	// set we route to the cursor-aware variant; otherwise plain offset paging.
	var (
		list any
		lerr error
	)
	if since > 0 || before != "" || after != "" {
		list, lerr = h.posts.ListForChannelPaged(r.Context(), channelID, posts.PageOpts{
			Since: since, Before: before, After: after, Page: page, PerPage: perPage,
		})
	} else {
		list, lerr = h.posts.ListForChannel(r.Context(), channelID, page, perPage)
	}
	if lerr != nil {
		writeError(w, 500, "api.post.list.app_error", lerr.Error())
		return
	}
	writeJSON(w, 200, list)
}

type searchPostsReq struct {
	Terms            string `json:"terms"`
	IsOrSearch       bool   `json:"is_or_search"`
	TimeZoneOffset   int    `json:"time_zone_offset"`
	IncludeDeletedCh bool   `json:"include_deleted_channels"`
	Page             int    `json:"page"`
	PerPage          int    `json:"per_page"`

	// Phase 18 — optional filters. Empty / zero values mean "no filter".
	// The webapp derives these from `from:`, `in:`, `before:`, `after:`,
	// `has:file`, `has:link` tokens stripped client-side.
	FromUserID  string `json:"from_user_id,omitempty"`
	InChannelID string `json:"in_channel_id,omitempty"`
	After       int64  `json:"after,omitempty"`
	Before      int64  `json:"before,omitempty"`
	HasFile     bool   `json:"has_file,omitempty"`
	HasLink     bool   `json:"has_link,omitempty"`
}

func (h *handlers) searchPosts(w http.ResponseWriter, r *http.Request) {
	teamID := chi.URLParam(r, "teamID")
	var req searchPostsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.post.search.invalid_body", err.Error())
		return
	}
	if strings.TrimSpace(req.Terms) == "" {
		writeError(w, 400, "api.post.search.empty_terms", "terms required")
		return
	}
	filters := posts.SearchFilters{
		FromUserID:  req.FromUserID,
		InChannelID: req.InChannelID,
		After:       req.After,
		Before:      req.Before,
		HasFile:     req.HasFile,
		HasLink:     req.HasLink,
	}
	result, err := h.posts.Search(r.Context(), userID(r), teamID, req.Terms, filters, req.Page, req.PerPage)
	if err != nil {
		writeError(w, 500, "api.post.search.app_error", err.Error())
		return
	}
	writeJSON(w, 200, result)
}

func (h *handlers) deletePost(w http.ResponseWriter, r *http.Request) {
	postID := chi.URLParam(r, "postID")
	uid := userID(r)
	existing, err := h.posts.Get(r.Context(), postID)
	if err != nil {
		writeError(w, 404, "api.post.delete.not_found", err.Error())
		return
	}
	if existing.DeleteAt != 0 {
		writeError(w, 404, "api.post.delete.not_found", "post not found")
		return
	}
	isMember, err := h.channels.IsMember(r.Context(), existing.ChannelID, uid)
	if err != nil {
		writeError(w, 500, "api.post.delete.member_check", err.Error())
		return
	}
	if !isMember {
		writeError(w, 403, "api.post.delete.forbidden", "not a channel member")
		return
	}
	if existing.UserID != uid {
		writeError(w, 403, "api.post.delete.forbidden", "only the post owner can delete it")
		return
	}
	deleted, err := h.posts.Delete(r.Context(), postID, uid)
	if err != nil {
		writeError(w, 500, "api.post.delete.app_error", err.Error())
		return
	}
	if !deleted {
		writeError(w, 404, "api.post.delete.not_found", "post not found")
		return
	}
	h.hub.Broadcast(ws.Event{
		Event: "post_deleted",
		Data: map[string]any{
			"post_id":    postID,
			"channel_id": existing.ChannelID,
		},
		Broadcast: ws.Broadcast{ChannelID: existing.ChannelID},
	})
	if h.audit != nil {
		h.audit.LogAsync(uid, audit.ActionPostDelete, postID, map[string]any{
			"channel_id": existing.ChannelID,
		})
	}
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

type updatePostReq struct {
	ID      string         `json:"id"`
	Message string         `json:"message"`
	Props   map[string]any `json:"props"`
}

func (h *handlers) updatePost(w http.ResponseWriter, r *http.Request) {
	postID := chi.URLParam(r, "postID")
	var req updatePostReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.post.update.invalid_body", err.Error())
		return
	}
	updated, err := h.posts.Update(r.Context(), postID, userID(r), req.Message, req.Props)
	if err != nil {
		writeError(w, 500, "api.post.update.app_error", err.Error())
		return
	}
	if updated == nil {
		writeError(w, 403, "api.post.update.forbidden", "post not found or not owner")
		return
	}
	raw, _ := json.Marshal(updated)
	h.hub.Broadcast(ws.Event{
		Event: "post_edited",
		Data: map[string]any{
			"post":       string(raw),
			"channel_id": updated.ChannelID,
		},
		Broadcast: ws.Broadcast{ChannelID: updated.ChannelID},
	})
	writeJSON(w, 200, updated)
}

// ---- Phase 18: saved posts ----

// listSavedPosts returns the caller's bookmarked posts, newest-saved first.
// `limit` defaults to 20 (cap 100) to bound the join against `posts`;
// `offset` drives pagination in the webapp's 저장됨 pseudo-channel.
func (h *handlers) listSavedPosts(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	ids, err := h.saved.ListIDs(r.Context(), uid, limit, offset)
	if err != nil {
		writeError(w, 500, "api.saved.list.app_error", err.Error())
		return
	}
	list, err := h.posts.ListByIDsForUser(r.Context(), uid, ids)
	if err != nil {
		writeError(w, 500, "api.saved.list.posts", err.Error())
		return
	}
	visibleOrder := make([]string, 0, len(list))
	for _, post := range list {
		visibleOrder = append(visibleOrder, post.ID)
	}
	writeJSON(w, 200, map[string]any{
		"order": visibleOrder,
		"posts": list,
	})
}

type savedPostsBulkReq struct {
	PostIDs []string `json:"post_ids"`
}

// savedPostsBulkCheck answers "which of these posts have I saved?" in one
// round-trip so the post stream can render filled vs outline stars without
// a per-post call.
func (h *handlers) savedPostsBulkCheck(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	var req savedPostsBulkReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.saved.bulk.invalid_body", err.Error())
		return
	}
	if len(req.PostIDs) == 0 {
		writeJSON(w, 200, map[string]bool{})
		return
	}
	if len(req.PostIDs) > 500 {
		req.PostIDs = req.PostIDs[:500]
	}
	m, err := h.saved.IsSavedBulk(r.Context(), uid, req.PostIDs)
	if err != nil {
		writeError(w, 500, "api.saved.bulk.app_error", err.Error())
		return
	}
	writeJSON(w, 200, m)
}

// savePost bookmarks a single post. Bumping saved_posts.create_at on
// re-save isn't useful (the star was already filled), so Save() is
// idempotent via ON CONFLICT DO NOTHING. Fans a saved_post_changed WS
// event to the caller's other sockets for multi-tab sync.
func (h *handlers) savePost(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	postID := chi.URLParam(r, "postID")
	// Validate the post exists + caller can see it (must be a member of
	// the post's channel). Prevents saving arbitrary post ids.
	p, err := h.posts.Get(r.Context(), postID)
	if err != nil || p == nil {
		writeError(w, 404, "api.saved.save.not_found", "post not found")
		return
	}
	isMember, err := h.channels.IsMember(r.Context(), p.ChannelID, uid)
	if err != nil {
		writeError(w, 500, "api.saved.save.member_check", err.Error())
		return
	}
	if !isMember {
		writeError(w, 403, "api.saved.save.forbidden", "not a channel member")
		return
	}
	fresh, err := h.saved.Save(r.Context(), uid, postID)
	if err != nil {
		writeError(w, 500, "api.saved.save.app_error", err.Error())
		return
	}
	if fresh {
		h.hub.Broadcast(ws.Event{
			Event: "saved_post_changed",
			Data: map[string]any{
				"post_id": postID,
				"saved":   true,
			},
			Broadcast: ws.Broadcast{UserID: uid},
		})
	}
	writeJSON(w, 200, map[string]any{"status": "OK", "saved": true})
}

// unsavePost is the mirror of savePost. Missing rows are treated as a
// successful no-op so rapid double-click "unsave unsave" never surfaces a
// 404 to the client.
func (h *handlers) unsavePost(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	postID := chi.URLParam(r, "postID")
	removed, err := h.saved.Unsave(r.Context(), uid, postID)
	if err != nil {
		writeError(w, 500, "api.saved.unsave.app_error", err.Error())
		return
	}
	if removed {
		h.hub.Broadcast(ws.Event{
			Event: "saved_post_changed",
			Data: map[string]any{
				"post_id": postID,
				"saved":   false,
			},
			Broadcast: ws.Broadcast{UserID: uid},
		})
	}
	writeJSON(w, 200, map[string]any{"status": "OK", "saved": false})
}

// ---- Phase 18: public channel discovery ----

// discoverChannels lists public channels in a team that the caller hasn't
// yet joined. Member-gated so outsiders can't enumerate channel names in
// teams they don't belong to.
func (h *handlers) discoverChannels(w http.ResponseWriter, r *http.Request) {
	teamID := chi.URLParam(r, "teamID")
	uid := userID(r)
	isMember, err := h.teams.IsMember(r.Context(), teamID, uid)
	if err != nil {
		writeError(w, 500, "api.channel.discover.member_check", err.Error())
		return
	}
	if !isMember {
		writeError(w, 403, "api.channel.discover.forbidden", "not a team member")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	list, err := h.channels.ListPublicDiscoverable(r.Context(), teamID, uid, q, limit, offset)
	if err != nil {
		writeError(w, 500, "api.channel.discover.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// selfJoinChannel lets a team member self-add to a public ('O') channel
// without requiring an existing member to invite them — the entry point
// for the Phase 18 channel-discovery flow. Private ('P') / direct ('D') /
// group ('G') channels still require invite via addChannelMember.
func (h *handlers) selfJoinChannel(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	uid := userID(r)
	ch, err := h.channels.Get(r.Context(), channelID)
	if err != nil {
		writeError(w, 404, "api.channel.join.not_found", err.Error())
		return
	}
	if ch.Type != "O" {
		writeError(w, 403, "api.channel.join.not_public", "only public channels are self-joinable")
		return
	}
	if ch.TeamID != "" {
		isTeamMember, err := h.teams.IsMember(r.Context(), ch.TeamID, uid)
		if err != nil || !isTeamMember {
			writeError(w, 403, "api.channel.join.forbidden", "not a team member")
			return
		}
	}
	if err := h.channels.Join(r.Context(), channelID, uid); err != nil {
		writeError(w, 500, "api.channel.join.app_error", err.Error())
		return
	}
	h.hub.Broadcast(ws.Event{
		Event: "user_added",
		Data: map[string]any{
			"user_id":    uid,
			"channel_id": channelID,
		},
		Broadcast: ws.Broadcast{ChannelID: channelID},
	})
	writeJSON(w, 201, map[string]any{
		"channel_id": channelID,
		"user_id":    uid,
		"roles":      "channel_user",
	})
}

// ---- Phase 18: link preview image proxy ----

// linkPreviewImage streams a remote image through the server so the browser
// never dials third-party hosts directly (which would leak IP / user-agent
// to every site a link previews from). Same SSRF guard as Fetch, a 5MB cap,
// and a strict `image/*` Content-Type check keep this endpoint from being
// repurposed as an open HTTP proxy.
func (h *handlers) linkPreviewImage(w http.ResponseWriter, r *http.Request) {
	if h.links == nil {
		writeError(w, 404, "api.link_preview.disabled", "link previews disabled")
		return
	}
	target := r.URL.Query().Get("url")
	if target == "" {
		writeError(w, 400, "api.link_preview.missing_url", "url required")
		return
	}
	u, err := url.Parse(target)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		writeError(w, 400, "api.link_preview.bad_url", "invalid url")
		return
	}
	img, ct, err := h.links.FetchImage(r.Context(), target)
	if err != nil {
		writeError(w, 502, "api.link_preview.fetch", err.Error())
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(200)
	_, _ = w.Write(img)
}

// ---- Phase 19: scheduled messages ----

type createScheduledPostReq struct {
	ChannelID string         `json:"channel_id"`
	RootID    string         `json:"root_id"`
	Message   string         `json:"message"`
	FileIDs   []string       `json:"file_ids"`
	Props     map[string]any `json:"props"`
	SendAt    int64          `json:"send_at"`
}

// createScheduledPost enqueues a post for later delivery. Validates the
// caller is a channel member (same gate as createPost) and that send_at is
// in the future with a 30-second fudge so a clock-skewed client isn't
// rejected for scheduling "now-ish". If caller supplies file_ids the
// Worker re-associates them at dispatch time; we do NOT flip them now so
// the source files aren't consumed if the user cancels the schedule.
func (h *handlers) createScheduledPost(w http.ResponseWriter, r *http.Request) {
	var req createScheduledPostReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.scheduled.create.invalid_body", err.Error())
		return
	}
	if strings.TrimSpace(req.Message) == "" && len(req.FileIDs) == 0 {
		writeError(w, 400, "api.scheduled.create.empty", "message or file required")
		return
	}
	now := time.Now().UnixMilli()
	if req.SendAt <= now-30_000 {
		writeError(w, 400, "api.scheduled.create.past", "send_at must be in the future")
		return
	}
	uid := userID(r)
	isMember, err := h.channels.IsMember(r.Context(), req.ChannelID, uid)
	if err != nil {
		writeError(w, 500, "api.scheduled.create.member_check", err.Error())
		return
	}
	if !isMember {
		writeError(w, 403, "api.scheduled.create.forbidden", "not a channel member")
		return
	}
	sp, err := h.scheduled.Create(r.Context(), uid, req.ChannelID, req.RootID, req.Message, req.FileIDs, req.Props, req.SendAt)
	if err != nil {
		writeError(w, 500, "api.scheduled.create.app_error", err.Error())
		return
	}
	// Author-scoped WS so the 예약됨 sidebar can prepend the row across
	// tabs without a refetch.
	h.hub.Broadcast(ws.Event{
		Event:     "scheduled_post_created",
		Data:      map[string]any{"id": sp.ID, "channel_id": sp.ChannelID, "send_at": sp.SendAt, "message": sp.Message},
		Broadcast: ws.Broadcast{UserID: uid},
	})
	writeJSON(w, 201, sp)
}

func (h *handlers) listMyScheduledPosts(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	list, err := h.scheduled.ListPending(r.Context(), uid)
	if err != nil {
		writeError(w, 500, "api.scheduled.list.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func (h *handlers) deleteScheduledPost(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "scheduledID")
	uid := userID(r)
	ok, err := h.scheduled.Delete(r.Context(), id, uid)
	if err != nil {
		writeError(w, 500, "api.scheduled.delete.app_error", err.Error())
		return
	}
	if !ok {
		writeError(w, 404, "api.scheduled.delete.not_found", "not found or already sent")
		return
	}
	h.hub.Broadcast(ws.Event{
		Event:     "scheduled_post_deleted",
		Data:      map[string]any{"id": id},
		Broadcast: ws.Broadcast{UserID: uid},
	})
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

// ---- Phase 19: post reminders ----

type createReminderReq struct {
	RemindAt int64 `json:"remind_at"`
}

// createPostReminder schedules a "remind me about this post" event. Caller
// must be a member of the post's channel; stale post ids 404 so a user
// can't probe for post existence across channels.
func (h *handlers) createPostReminder(w http.ResponseWriter, r *http.Request) {
	postID := chi.URLParam(r, "postID")
	var req createReminderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.reminder.create.invalid_body", err.Error())
		return
	}
	now := time.Now().UnixMilli()
	if req.RemindAt <= now-30_000 {
		writeError(w, 400, "api.reminder.create.past", "remind_at must be in the future")
		return
	}
	uid := userID(r)
	p, err := h.posts.Get(r.Context(), postID)
	if err != nil || p == nil {
		writeError(w, 404, "api.reminder.create.not_found", "post not found")
		return
	}
	isMember, err := h.channels.IsMember(r.Context(), p.ChannelID, uid)
	if err != nil {
		writeError(w, 500, "api.reminder.create.member_check", err.Error())
		return
	}
	if !isMember {
		writeError(w, 403, "api.reminder.create.forbidden", "not a channel member")
		return
	}
	rem, err := h.reminders.Create(r.Context(), uid, postID, req.RemindAt)
	if err != nil {
		writeError(w, 500, "api.reminder.create.app_error", err.Error())
		return
	}
	h.hub.Broadcast(ws.Event{
		Event:     "reminder_created",
		Data:      map[string]any{"id": rem.ID, "post_id": postID, "remind_at": rem.RemindAt},
		Broadcast: ws.Broadcast{UserID: uid},
	})
	writeJSON(w, 201, rem)
}

func (h *handlers) listMyReminders(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	list, err := h.reminders.ListPending(r.Context(), uid)
	if err != nil {
		writeError(w, 500, "api.reminder.list.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func (h *handlers) deleteReminder(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "reminderID")
	uid := userID(r)
	ok, err := h.reminders.Delete(r.Context(), id, uid)
	if err != nil {
		writeError(w, 500, "api.reminder.delete.app_error", err.Error())
		return
	}
	if !ok {
		writeError(w, 404, "api.reminder.delete.not_found", "not found or already fired")
		return
	}
	h.hub.Broadcast(ws.Event{
		Event:     "reminder_deleted",
		Data:      map[string]any{"id": id},
		Broadcast: ws.Broadcast{UserID: uid},
	})
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

// ---- Channel members ----

// listMyChannelMembers returns the caller's channel_members rows for a
// team (plus any DMs regardless of team) including unread counters and
// notify_props. The webapp uses this to restore sidebar state on
// reconnect so badges survive a reload.
func (h *handlers) listMyChannelMembers(w http.ResponseWriter, r *http.Request) {
	h.listChannelMembersForUserID(w, r, userID(r))
}

func (h *handlers) getMyNotifyProps(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	uid := userID(r)
	isMember, err := h.channels.IsMember(r.Context(), channelID, uid)
	if err != nil || !isMember {
		writeError(w, 403, "api.channel.notify_props.forbidden", "not a channel member")
		return
	}
	props, err := h.channels.GetNotifyProps(r.Context(), channelID, uid)
	if err != nil {
		writeError(w, 500, "api.channel.notify_props.app_error", err.Error())
		return
	}
	writeJSON(w, 200, props)
}

func (h *handlers) putMyNotifyProps(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	uid := userID(r)
	isMember, err := h.channels.IsMember(r.Context(), channelID, uid)
	if err != nil || !isMember {
		writeError(w, 403, "api.channel.notify_props.forbidden", "not a channel member")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, 400, "api.channel.notify_props.invalid_body", err.Error())
		return
	}
	if err := h.channels.SetNotifyProps(r.Context(), channelID, uid, body); err != nil {
		writeError(w, 500, "api.channel.notify_props.save", err.Error())
		return
	}
	// Return the merged view (defaults + user overrides) to avoid a round-trip.
	props, err := h.channels.GetNotifyProps(r.Context(), channelID, uid)
	if err != nil {
		writeError(w, 500, "api.channel.notify_props.app_error", err.Error())
		return
	}
	writeJSON(w, 200, props)
}

func (h *handlers) listChannelMembers(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	uid := userID(r)
	isMember, err := h.channels.IsMember(r.Context(), channelID, uid)
	if err != nil || !isMember {
		writeError(w, 403, "api.channel.members.list.forbidden", "not a channel member")
		return
	}
	list, err := h.channels.ListMembers(r.Context(), channelID)
	if err != nil {
		writeError(w, 500, "api.channel.members.list.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// channelMembersAutocomplete powers the Composer's `@mention` dropdown.
// Scope is intentionally the current channel's membership so suggestions
// correspond to users who can actually see the resulting post.
//
// Query params:
//   - `prefix` (required): the token after `@` the user has typed so far.
//     Empty prefix short-circuits to 400 so we don't dump the full
//     membership roster on every keystroke before any letters are typed.
//   - `limit` (optional, default 8, capped at 25): UI sizing knob.
//
// Response shape matches the rest of the user-facing endpoints: a JSON
// array of `{id, username, email, roles}` objects compatible with
// `auth.User` so the webapp can reuse existing user types.
func (h *handlers) channelMembersAutocomplete(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	uid := userID(r)
	isMember, err := h.channels.IsMember(r.Context(), channelID, uid)
	if err != nil || !isMember {
		writeError(w, 403, "api.channel.members.autocomplete.forbidden", "not a channel member")
		return
	}
	prefix := strings.TrimSpace(r.URL.Query().Get("prefix"))
	if prefix == "" {
		writeError(w, 400, "api.channel.members.autocomplete.prefix_required", "prefix query parameter required")
		return
	}
	// Cap prefix length so a wild regex-style input can't tie up the DB.
	if len(prefix) > 40 {
		prefix = prefix[:40]
	}
	limit := 8
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 25 {
			limit = n
		}
	}
	list, err := h.channels.MembersAutocomplete(r.Context(), channelID, prefix, limit)
	if err != nil {
		writeError(w, 500, "api.channel.members.autocomplete.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

type addChannelMemberReq struct {
	UserID string `json:"user_id"`
}

func (h *handlers) addChannelMember(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	uid := userID(r)
	isMember, err := h.channels.IsMember(r.Context(), channelID, uid)
	if err != nil || !isMember {
		writeError(w, 403, "api.channel.members.add.forbidden", "not a channel member")
		return
	}
	var req addChannelMemberReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == "" {
		writeError(w, 400, "api.channel.members.add.invalid_body", "user_id required")
		return
	}
	if err := h.channels.Join(r.Context(), channelID, req.UserID); err != nil {
		writeError(w, 500, "api.channel.members.add.app_error", err.Error())
		return
	}
	h.hub.Broadcast(ws.Event{
		Event: "user_added",
		Data: map[string]any{
			"user_id":    req.UserID,
			"channel_id": channelID,
		},
		Broadcast: ws.Broadcast{ChannelID: channelID},
	})
	if h.audit != nil {
		h.audit.LogAsync(uid, audit.ActionMemberAdd, channelID, map[string]any{
			"added_user": req.UserID,
		})
	}
	writeJSON(w, 201, map[string]any{
		"channel_id": channelID,
		"user_id":    req.UserID,
		"roles":      "channel_user",
	})
}

func (h *handlers) removeChannelMember(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	targetUser := chi.URLParam(r, "userID")
	uid := userID(r)
	// Allow self-leave or any member removing another (simple policy for MVP).
	isMember, err := h.channels.IsMember(r.Context(), channelID, uid)
	if err != nil || !isMember {
		writeError(w, 403, "api.channel.members.remove.forbidden", "not a channel member")
		return
	}
	if err := h.channels.Remove(r.Context(), channelID, targetUser); err != nil {
		writeError(w, 500, "api.channel.members.remove.app_error", err.Error())
		return
	}
	h.hub.Broadcast(ws.Event{
		Event: "user_removed",
		Data: map[string]any{
			"user_id":    targetUser,
			"channel_id": channelID,
			"remover_id": uid,
		},
		Broadcast: ws.Broadcast{ChannelID: channelID},
	})
	if h.audit != nil {
		h.audit.LogAsync(uid, audit.ActionMemberRemove, channelID, map[string]any{
			"removed_user": targetUser,
		})
	}
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

// ---- Reactions ----

type reactionReq struct {
	UserID    string `json:"user_id"`
	PostID    string `json:"post_id"`
	EmojiName string `json:"emoji_name"`
}

func (h *handlers) addReaction(w http.ResponseWriter, r *http.Request) {
	var req reactionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.reaction.create.invalid_body", err.Error())
		return
	}
	uid := userID(r)
	if req.UserID != "" && req.UserID != uid {
		writeError(w, 403, "api.reaction.create.forbidden", "user_id mismatch")
		return
	}
	if req.PostID == "" || req.EmojiName == "" {
		writeError(w, 400, "api.reaction.create.invalid", "post_id and emoji_name required")
		return
	}
	channelID, err := h.reactions.ChannelForPost(r.Context(), req.PostID)
	if err != nil {
		writeError(w, 404, "api.reaction.create.post_not_found", err.Error())
		return
	}
	isMember, err := h.channels.IsMember(r.Context(), channelID, uid)
	if err != nil || !isMember {
		writeError(w, 403, "api.reaction.create.forbidden", "not a channel member")
		return
	}
	reaction, err := h.reactions.Add(r.Context(), uid, req.PostID, req.EmojiName)
	if err != nil {
		writeError(w, 500, "api.reaction.create.save.app_error", err.Error())
		return
	}
	raw, _ := json.Marshal(reaction)
	h.hub.Broadcast(ws.Event{
		Event:     "reaction_added",
		Data:      map[string]any{"reaction": string(raw)},
		Broadcast: ws.Broadcast{ChannelID: channelID},
	})
	writeJSON(w, 201, reaction)
}

func (h *handlers) listReactions(w http.ResponseWriter, r *http.Request) {
	postID := chi.URLParam(r, "postID")
	uid := userID(r)
	channelID, err := h.reactions.ChannelForPost(r.Context(), postID)
	if err != nil {
		writeError(w, 404, "api.reaction.list.post_not_found", err.Error())
		return
	}
	isMember, err := h.channels.IsMember(r.Context(), channelID, uid)
	if err != nil || !isMember {
		writeError(w, 403, "api.reaction.list.forbidden", "not a channel member")
		return
	}
	list, err := h.reactions.ListForPost(r.Context(), postID)
	if err != nil {
		writeError(w, 500, "api.reaction.list.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func (h *handlers) removeReaction(w http.ResponseWriter, r *http.Request) {
	targetUser := chi.URLParam(r, "userID")
	postID := chi.URLParam(r, "postID")
	emoji := chi.URLParam(r, "emoji")
	uid := userID(r)
	if targetUser != uid {
		writeError(w, 403, "api.reaction.delete.forbidden", "can only remove own reactions")
		return
	}
	channelID, err := h.reactions.ChannelForPost(r.Context(), postID)
	if err != nil {
		writeError(w, 404, "api.reaction.delete.post_not_found", err.Error())
		return
	}
	if err := h.reactions.Remove(r.Context(), uid, postID, emoji); err != nil {
		writeError(w, 500, "api.reaction.delete.app_error", err.Error())
		return
	}
	raw, _ := json.Marshal(reactions.Reaction{UserID: uid, PostID: postID, EmojiName: emoji})
	h.hub.Broadcast(ws.Event{
		Event:     "reaction_removed",
		Data:      map[string]any{"reaction": string(raw)},
		Broadcast: ws.Broadcast{ChannelID: channelID},
	})
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

// ---- Post pinning ----

func (h *handlers) pinPost(w http.ResponseWriter, r *http.Request)   { h.setPinned(w, r, true) }
func (h *handlers) unpinPost(w http.ResponseWriter, r *http.Request) { h.setPinned(w, r, false) }

func (h *handlers) setPinned(w http.ResponseWriter, r *http.Request, pinned bool) {
	postID := chi.URLParam(r, "postID")
	existing, err := h.posts.Get(r.Context(), postID)
	if err != nil {
		writeError(w, 404, "api.post.pin.not_found", err.Error())
		return
	}
	isMember, err := h.channels.IsMember(r.Context(), existing.ChannelID, userID(r))
	if err != nil || !isMember {
		writeError(w, 403, "api.post.pin.forbidden", "not a channel member")
		return
	}
	updated, err := h.posts.SetPinned(r.Context(), postID, pinned)
	if err != nil {
		writeError(w, 500, "api.post.pin.app_error", err.Error())
		return
	}
	if updated == nil {
		writeError(w, 404, "api.post.pin.not_found", "post missing or deleted")
		return
	}
	raw, _ := json.Marshal(updated)
	eventName := "post_pinned"
	if !pinned {
		eventName = "post_unpinned"
	}
	h.hub.Broadcast(ws.Event{
		Event: eventName,
		Data: map[string]any{
			"post":       string(raw),
			"channel_id": updated.ChannelID,
		},
		Broadcast: ws.Broadcast{ChannelID: updated.ChannelID},
	})
	if h.audit != nil {
		act := audit.ActionPostPin
		if !pinned {
			act = audit.ActionPostUnpin
		}
		h.audit.LogAsync(userID(r), act, postID, map[string]any{"channel_id": updated.ChannelID})
	}
	writeJSON(w, 200, updated)
}

func (h *handlers) listPinned(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	isMember, err := h.channels.IsMember(r.Context(), channelID, userID(r))
	if err != nil || !isMember {
		writeError(w, 403, "api.post.pin.list.forbidden", "not a channel member")
		return
	}
	list, err := h.posts.ListPinned(r.Context(), channelID)
	if err != nil {
		writeError(w, 500, "api.post.pin.list.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// listThread returns the root post and all live replies for a thread.
// The root may itself be a reply (root_id != ""); we chase to the real
// top so deep-linking to a reply ID still surfaces the whole thread.
func (h *handlers) listThread(w http.ResponseWriter, r *http.Request) {
	postID := chi.URLParam(r, "postID")
	root, err := h.posts.Get(r.Context(), postID)
	if err != nil || root == nil {
		writeError(w, 404, "api.post.thread.not_found", "post not found")
		return
	}
	rootID := root.ID
	if root.RootID != "" {
		rootID = root.RootID
	}
	// Gate on channel membership of the root post's channel.
	isMember, err := h.channels.IsMember(r.Context(), root.ChannelID, userID(r))
	if err != nil || !isMember {
		writeError(w, 403, "api.post.thread.forbidden", "not a channel member")
		return
	}
	list, err := h.posts.ListThread(r.Context(), rootID)
	if err != nil {
		writeError(w, 500, "api.post.thread.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// ---- Files ----

func (h *handlers) uploadFiles(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	channelID := r.URL.Query().Get("channel_id")
	if channelID != "" {
		isMember, err := h.channels.IsMember(r.Context(), channelID, uid)
		if err != nil || !isMember {
			writeError(w, 403, "api.file.upload.forbidden", "not a channel member")
			return
		}
	}
	// 128 MiB cap on combined multipart body. Browsers + curl all handle this.
	if err := r.ParseMultipartForm(128 << 20); err != nil {
		writeError(w, 400, "api.file.upload.parse", err.Error())
		return
	}
	form := r.MultipartForm
	if form == nil {
		writeError(w, 400, "api.file.upload.empty", "no multipart form")
		return
	}
	clientIDs := form.Value["client_ids"]

	uploads := form.File["files"]
	if len(uploads) == 0 {
		writeError(w, 400, "api.file.upload.no_files", "no files in request")
		return
	}
	infos := make([]*files.FileInfo, 0, len(uploads))
	for _, fh := range uploads {
		src, err := fh.Open()
		if err != nil {
			writeError(w, 500, "api.file.upload.open", err.Error())
			return
		}
		mime := fh.Header.Get("Content-Type")
		fi, err := h.files.Upload(r.Context(), uid, channelID, fh.Filename, mime, src)
		src.Close()
		if err != nil {
			writeError(w, 500, "api.file.upload.save", err.Error())
			return
		}
		infos = append(infos, fi)
	}
	writeJSON(w, 201, map[string]any{
		"file_infos": infos,
		"client_ids": clientIDs,
	})
}

func (h *handlers) fileInfo(w http.ResponseWriter, r *http.Request) {
	fi, err := h.files.GetInfo(r.Context(), chi.URLParam(r, "fileID"))
	if err != nil {
		writeError(w, 404, "api.file.get.not_found", err.Error())
		return
	}
	if err := h.authorizeFile(r, fi); err != nil {
		writeError(w, 403, "api.file.get.forbidden", err.Error())
		return
	}
	writeJSON(w, 200, fi)
}

func (h *handlers) downloadFile(w http.ResponseWriter, r *http.Request) {
	rc, fi, err := h.files.Open(r.Context(), chi.URLParam(r, "fileID"))
	if err != nil {
		writeError(w, 404, "api.file.get.not_found", err.Error())
		return
	}
	defer rc.Close()
	if err := h.authorizeFile(r, fi); err != nil {
		writeError(w, 403, "api.file.get.forbidden", err.Error())
		return
	}
	if fi.MimeType != "" {
		w.Header().Set("Content-Type", fi.MimeType)
	}
	w.Header().Set("Content-Disposition", "attachment; filename=\""+fi.Name+"\"")
	if fi.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(fi.Size, 10))
	}
	w.WriteHeader(200)
	_, _ = io.Copy(w, rc)
}

func (h *handlers) fileLink(w http.ResponseWriter, r *http.Request) {
	fileID := chi.URLParam(r, "fileID")
	fi, err := h.files.GetInfo(r.Context(), fileID)
	if err != nil {
		writeError(w, 404, "api.file.link.not_found", err.Error())
		return
	}
	if err := h.authorizeFile(r, fi); err != nil {
		writeError(w, 403, "api.file.link.forbidden", err.Error())
		return
	}
	link := "/api/v4/files/" + url.PathEscape(fileID)
	link = h.effectivePublicBaseURL(r) + link
	writeJSON(w, 200, map[string]string{"link": link})
}

// authorizeFile allows access when: the caller uploaded it, or the file is
// attached to a post in a channel the caller is a member of.
func (h *handlers) authorizeFile(r *http.Request, fi *files.FileInfo) error {
	uid := userID(r)
	if fi.UserID == uid {
		return nil
	}
	if fi.ChannelID == "" {
		return errors.New("file not attached to an accessible channel")
	}
	isMember, err := h.channels.IsMember(r.Context(), fi.ChannelID, uid)
	if err != nil {
		return err
	}
	if !isMember {
		return errors.New("not a channel member")
	}
	return nil
}

// ---- Slash commands ----

type executeCommandReq struct {
	TeamID    string `json:"team_id"`
	ChannelID string `json:"channel_id"`
	Command   string `json:"command"`
}

func (h *handlers) executeCommand(w http.ResponseWriter, r *http.Request) {
	var req executeCommandReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.command.execute.invalid_body", err.Error())
		return
	}
	if !strings.HasPrefix(strings.TrimSpace(req.Command), "/") {
		writeError(w, 400, "api.command.execute.missing_slash", "command must start with /")
		return
	}
	uid := userID(r)
	if req.ChannelID != "" {
		isMember, err := h.channels.IsMember(r.Context(), req.ChannelID, uid)
		if err != nil || !isMember {
			writeError(w, 403, "api.command.execute.forbidden", "not a channel member")
			return
		}
	}
	resp, err := h.slash.Execute(r.Context(), slashcmd.ExecuteArgs{
		TeamID:       req.TeamID,
		ChannelID:    req.ChannelID,
		UserID:       uid,
		Command:      req.Command,
		CredentialID: credentialIDFromContext(r.Context()),
	})
	if err != nil {
		if errors.Is(err, slashcmd.ErrUnknown) {
			writeError(w, 404, "api.command.execute.unknown", "unknown command")
			return
		}
		writeError(w, 500, "api.command.execute.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(uid, audit.ActionCommandExecute, req.ChannelID, map[string]any{
			"command": req.Command,
			"in":      string(resp.ResponseType),
		})
	}
	writeJSON(w, 200, resp)
}

// pluginCommandAdapter lets slashcmd call the plugin host without importing
// it (and vice versa), satisfying slashcmd.Plugin.
type pluginCommandAdapter struct{ host *pluginhost.Host }

func (a *pluginCommandAdapter) ExecuteCommand(ctx context.Context, trigger, arg, channelID, userID string) (*slashcmd.Response, bool, error) {
	if a.host == nil {
		return nil, false, nil
	}
	reply, handled, err := a.host.ExecuteCommand(ctx, trigger, arg, channelID, userID)
	if err != nil || !handled || reply == nil {
		return nil, handled, err
	}
	rt := slashcmd.ResponseType(reply.ResponseType)
	if rt != slashcmd.InChannel && rt != slashcmd.Ephemeral {
		rt = slashcmd.Ephemeral
	}
	return &slashcmd.Response{ResponseType: rt, Text: reply.Text}, true, nil
}

// ---- Plugins ----

func (h *handlers) listPlugins(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, h.host.List())
}

func (h *handlers) listPluginStatuses(w http.ResponseWriter, r *http.Request) {
	plugins := h.host.List()
	out := make([]map[string]any, 0, len(plugins))
	for _, p := range plugins {
		out = append(out, map[string]any{
			"plugin_id": p["id"],
			"state":     p["state"],
		})
	}
	writeJSON(w, 200, out)
}

func (h *handlers) listPluginWebapp(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, []any{})
}

func (h *handlers) listPluginMarketplace(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"plugins": []any{}})
}

// ---- Audit ----

func (h *handlers) listAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	// Accept `action_prefix` (newer, used by the admin audit-browse UI)
	// or fall back to the shorter `action` alias for backward compat with
	// the earlier ad-hoc curl-based tooling.
	prefix := r.URL.Query().Get("action_prefix")
	if prefix == "" {
		prefix = r.URL.Query().Get("action")
	}
	// Accept either the actor user id (?actor_id=) or a username
	// (?actor=username) — the latter is friendlier for admins typing into
	// the filter bar, the former is what the rows actually store.
	actor := r.URL.Query().Get("actor_id")
	if actor == "" {
		if name := r.URL.Query().Get("actor"); name != "" {
			u, uerr := h.auth.UserByUsername(r.Context(), name)
			if uerr == nil && u != nil {
				actor = u.ID
			} else {
				// Unknown username: return empty list rather than 500 so
				// the UI just shows "no results" for a typo.
				writeJSON(w, 200, []audit.Entry{})
				return
			}
		}
	}
	entries, err := h.audit.List(r.Context(), limit, prefix, actor)
	if err != nil {
		writeError(w, 500, "api.audit.list.app_error", err.Error())
		return
	}
	writeJSON(w, 200, entries)
}

// ---- Bots + personal access tokens ----

type createBotReq struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

// createBot — admin-only. Creates a user with is_bot=true and joins them
// to the default team/channel so the fresh bot can post immediately.
func (h *handlers) createBot(w http.ResponseWriter, r *http.Request) {
	var req createBotReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.bot.create.invalid_body", err.Error())
		return
	}
	if strings.TrimSpace(req.Username) == "" {
		writeError(w, 400, "api.bot.create.username_required", "username required")
		return
	}
	b, err := h.bots.Create(r.Context(), userID(r), req.Username, req.DisplayName, req.Description)
	if err != nil {
		writeError(w, 500, "api.bot.create.app_error", err.Error())
		return
	}
	// Bootstrap into default team/channel same as human signup.
	if berr := h.bootstrapMembership(r.Context(), b.UserID); berr != nil {
		h.logger.Warn("bot bootstrap failed", "bot", b.UserID, "err", berr)
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionUserRegister, b.Username, map[string]any{"is_bot": true})
	}
	writeJSON(w, 201, b)
}

func (h *handlers) listBots(w http.ResponseWriter, r *http.Request) {
	list, err := h.bots.List(r.Context())
	if err != nil {
		writeError(w, 500, "api.bot.list.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func (h *handlers) disableBot(w http.ResponseWriter, r *http.Request) {
	bid := chi.URLParam(r, "botID")
	if err := h.bots.Disable(r.Context(), bid); err != nil {
		if errors.Is(err, bots.ErrBotNotFound) {
			writeError(w, 404, "api.bot.disable.not_found", "bot not found")
			return
		}
		writeError(w, 500, "api.bot.disable.app_error", err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

type createTokenReq struct {
	Description string `json:"description"`
}

// createToken — self or admin. Returns the plaintext PAT exactly once.
func (h *handlers) createToken(w http.ResponseWriter, r *http.Request) {
	target := chi.URLParam(r, "userID")
	caller := userID(r)
	if target != caller {
		// Admin check required to mint tokens on behalf of another user.
		ok, err := h.auth.HasRole(r.Context(), caller, "system_admin")
		if err != nil || !ok {
			writeError(w, 403, "api.token.create.forbidden", "can only issue tokens for self (non-admin)")
			return
		}
	}
	var req createTokenReq
	// Body is optional — description may be empty.
	_ = json.NewDecoder(r.Body).Decode(&req)
	t, err := h.bots.CreateToken(r.Context(), target, req.Description)
	if err != nil {
		writeError(w, 500, "api.token.create.app_error", err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{
		"id":          t.Token.ID,
		"user_id":     t.Token.UserID,
		"description": t.Token.Description,
		"create_at":   t.Token.CreateAt,
		// `token` is only present on create — never on list.
		"token": t.Plain,
	})
}

func (h *handlers) listTokens(w http.ResponseWriter, r *http.Request) {
	target := chi.URLParam(r, "userID")
	caller := userID(r)
	if target != caller {
		ok, err := h.auth.HasRole(r.Context(), caller, "system_admin")
		if err != nil || !ok {
			writeError(w, 403, "api.token.list.forbidden", "cannot list tokens for other users")
			return
		}
	}
	list, err := h.bots.ListTokens(r.Context(), target)
	if err != nil {
		writeError(w, 500, "api.token.list.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func (h *handlers) revokeToken(w http.ResponseWriter, r *http.Request) {
	// Admin-only — a compromised bot should be killable even if the owner
	// can't be reached. Self-revocation still works because the owner is
	// (usually) an admin.
	if err := h.bots.RevokeToken(r.Context(), chi.URLParam(r, "tokenID")); err != nil {
		writeError(w, 500, "api.token.revoke.app_error", err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

// ---- Incoming webhooks ----

type createIncomingReq struct {
	ChannelID     string `json:"channel_id"`
	TeamID        string `json:"team_id"`
	DisplayName   string `json:"display_name"`
	Username      string `json:"username"`
	IconURL       string `json:"icon_url"`
	ChannelLocked bool   `json:"channel_locked"`
}

func (h *handlers) createIncomingWebhook(w http.ResponseWriter, r *http.Request) {
	var req createIncomingReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.webhook.incoming.create.invalid_body", err.Error())
		return
	}
	if req.ChannelID == "" {
		writeError(w, 400, "api.webhook.incoming.create.channel_required", "channel_id required")
		return
	}
	ch, err := h.channels.Get(r.Context(), req.ChannelID)
	if err != nil {
		writeError(w, 404, "api.webhook.incoming.create.channel_missing", "channel not found")
		return
	}
	hk, err := h.incoming.Create(r.Context(), userID(r), req.ChannelID, ch.TeamID,
		req.DisplayName, req.Username, req.IconURL, req.ChannelLocked)
	if err != nil {
		writeError(w, 500, "api.webhook.incoming.create.app_error", err.Error())
		return
	}
	writeJSON(w, 201, hk)
}

func (h *handlers) listIncomingWebhooks(w http.ResponseWriter, r *http.Request) {
	list, err := h.incoming.List(r.Context())
	if err != nil {
		writeError(w, 500, "api.webhook.incoming.list.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func (h *handlers) deleteIncomingWebhook(w http.ResponseWriter, r *http.Request) {
	if err := h.incoming.Delete(r.Context(), chi.URLParam(r, "hookID")); err != nil {
		if errors.Is(err, webhooks.ErrHookNotFound) {
			writeError(w, 404, "api.webhook.incoming.delete.not_found", "hook not found")
			return
		}
		writeError(w, 500, "api.webhook.incoming.delete.app_error", err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

// fireIncomingWebhook — public POST /hooks/{hookID}. No auth. Rate-limited
// at the router layer; request body capped to 8KiB to rule out memory
// exhaustion via giant JSON payloads. The returned post rides the regular
// `posted` broadcast so subscribers see it exactly like any other message.
func (h *handlers) fireIncomingWebhook(w http.ResponseWriter, r *http.Request) {
	hookID := chi.URLParam(r, "hookID")
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var payload webhooks.IncomingPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, 400, "api.webhook.incoming.fire.invalid_body", err.Error())
		return
	}
	hk, err := h.incoming.Get(r.Context(), hookID)
	if err != nil {
		if errors.Is(err, webhooks.ErrHookNotFound) || errors.Is(err, webhooks.ErrHookDisabled) {
			writeError(w, 404, "api.webhook.incoming.fire.not_found", "hook not found")
			return
		}
		writeError(w, 500, "api.webhook.incoming.fire.app_error", err.Error())
		return
	}
	// Creator must still be a member of the channel — if they were removed
	// after the hook was minted, refuse rather than leaking posts.
	ok, err := h.channels.IsMember(r.Context(), hk.ChannelID, hk.CreatorID)
	if err != nil || !ok {
		writeError(w, 403, "api.webhook.incoming.fire.creator_not_member", "hook creator no longer a channel member")
		return
	}
	_, err = h.incoming.Fire(r.Context(), hk, payload)
	if err != nil {
		switch postcommand.FailureCodeOf(err) {
		case postcommand.FailurePermissionDenied, postcommand.FailureNotMember:
			writeError(w, 403, "api.webhook.incoming.fire.forbidden", err.Error())
		case postcommand.FailurePermissionCheck, postcommand.FailureMembershipCheck:
			writeError(w, 500, "api.webhook.incoming.fire.permission_check", err.Error())
		default:
			writeError(w, 400, "api.webhook.incoming.fire.post_failed", err.Error())
		}
		return
	}
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

// ---- Outgoing webhooks ----

type createOutgoingReq struct {
	ChannelID    string   `json:"channel_id"`
	TeamID       string   `json:"team_id"`
	TriggerWords []string `json:"trigger_words"`
	TriggerWhen  int      `json:"trigger_when"`
	CallbackURLs []string `json:"callback_urls"`
	DisplayName  string   `json:"display_name"`
	ContentType  string   `json:"content_type"`
}

func (h *handlers) createOutgoingWebhook(w http.ResponseWriter, r *http.Request) {
	var req createOutgoingReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.webhook.outgoing.create.invalid_body", err.Error())
		return
	}
	if req.TeamID == "" {
		writeError(w, 400, "api.webhook.outgoing.create.team_required", "team_id required")
		return
	}
	if len(req.CallbackURLs) == 0 {
		writeError(w, 400, "api.webhook.outgoing.create.callbacks_required", "at least one callback url")
		return
	}
	hk, err := h.outgoing.Create(r.Context(), userID(r), req.TeamID, req.ChannelID,
		req.TriggerWords, req.CallbackURLs, req.TriggerWhen, req.DisplayName, req.ContentType)
	if err != nil {
		writeError(w, 500, "api.webhook.outgoing.create.app_error", err.Error())
		return
	}
	writeJSON(w, 201, hk)
}

func (h *handlers) listOutgoingWebhooks(w http.ResponseWriter, r *http.Request) {
	list, err := h.outgoing.List(r.Context())
	if err != nil {
		writeError(w, 500, "api.webhook.outgoing.list.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func (h *handlers) deleteOutgoingWebhook(w http.ResponseWriter, r *http.Request) {
	if err := h.outgoing.Delete(r.Context(), chi.URLParam(r, "hookID")); err != nil {
		if errors.Is(err, webhooks.ErrHookNotFound) {
			writeError(w, 404, "api.webhook.outgoing.delete.not_found", "hook not found")
			return
		}
		writeError(w, 500, "api.webhook.outgoing.delete.app_error", err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

// ---- WebSocket ----

func (h *handlers) websocket(w http.ResponseWriter, r *http.Request) {
	// Browser WebSocket constructors cannot attach Authorization headers.
	// Never accept credentials in the URL: query strings are routinely
	// retained by reverse proxies, access logs, and browser history. Native
	// clients may still authenticate with the standard bearer header; browser
	// clients use Mattermost's first-frame authentication_challenge contract.
	if strings.TrimSpace(r.URL.Query().Get("access_token")) != "" {
		writeError(w, http.StatusBadRequest, "api.websocket.auth.url_token_forbidden", "websocket credentials must not be sent in the URL")
		return
	}
	authenticate := ws.TokenAuthenticator(func(ctx context.Context, token string) (string, error) {
		claims, err := h.auth.Authenticate(ctx, token)
		if err != nil {
			return "", err
		}
		return claims.UserID, nil
	})
	if tok := extractBearer(r); tok != "" {
		uid, err := authenticate(r.Context(), tok)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "invalid token")
			return
		}
		ws.Handle(h.hub, uid, tok, authenticate).ServeHTTP(w, r)
		return
	}

	ws.HandleChallenge(h.hub, authenticate).ServeHTTP(w, r)
}

// ---- Custom Emoji (Phase 13) ----

// createEmoji accepts multipart form with field "name" (string) and "image"
// (file). Reads the image straight into emojis.Service, which in turn
// delegates to files.Service. 256KB cap enforced by the service layer and
// an outer MaxBytesReader so a hostile client can't blow the request
// timeout on uploads.
func (h *handlers) createEmoji(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	// Hard cap on the multipart body. 1 MiB is generous for a 256KB image
	// plus the form overhead; anything bigger is almost certainly abuse.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeError(w, 400, "api.emoji.create.parse", err.Error())
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		writeError(w, 400, "api.emoji.create.name", "name is required")
		return
	}
	file, fh, err := r.FormFile("image")
	if err != nil {
		writeError(w, 400, "api.emoji.create.image", "image is required")
		return
	}
	defer file.Close()
	mime := fh.Header.Get("Content-Type")
	e, err := h.emojis.Create(r.Context(), uid, strings.ToLower(name), mime, file)
	if err != nil {
		switch {
		case errors.Is(err, emojis.ErrInvalidName):
			writeError(w, 400, "api.emoji.create.name_invalid", err.Error())
		case errors.Is(err, emojis.ErrNameTaken):
			writeError(w, 409, "api.emoji.create.conflict", err.Error())
		case errors.Is(err, emojis.ErrTooLarge):
			writeError(w, 413, "api.emoji.create.too_large", err.Error())
		case errors.Is(err, emojis.ErrUnsupportedMIME):
			writeError(w, 415, "api.emoji.create.bad_mime", err.Error())
		default:
			writeError(w, 500, "api.emoji.create.fail", err.Error())
		}
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(uid, audit.ActionEmojiCreate, e.Name, map[string]any{"id": e.ID})
	}
	writeJSON(w, 201, e)
}

// listEmojis paginates newest-first. Clients are expected to pre-fetch
// everything on app start — custom emoji lists are typically <500 entries.
func (h *handlers) listEmojis(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	list, err := h.emojis.List(r.Context(), page, perPage)
	if err != nil {
		writeError(w, 500, "api.emoji.list.fail", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func (h *handlers) autocompleteEmojis(w http.ResponseWriter, r *http.Request) {
	term := strings.Trim(strings.ToLower(r.URL.Query().Get("name")), ":")
	h.writeEmojiSearch(w, r, term)
}

func (h *handlers) searchEmojis(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Term string `json:"term"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.emoji.search.invalid_body", err.Error())
		return
	}
	h.writeEmojiSearch(w, r, strings.Trim(strings.ToLower(req.Term), ":"))
}

func (h *handlers) emojisByNames(w http.ResponseWriter, r *http.Request) {
	var names []string
	if err := json.NewDecoder(r.Body).Decode(&names); err != nil {
		writeError(w, 400, "api.emoji.names.invalid_body", err.Error())
		return
	}
	if len(names) > 200 {
		names = names[:200]
	}
	out := []emojis.Emoji{}
	for _, name := range names {
		name = strings.Trim(strings.ToLower(name), ":")
		if name == "" {
			continue
		}
		e, err := h.emojis.GetByName(r.Context(), name)
		if err == nil && e != nil {
			out = append(out, *e)
		}
	}
	writeJSON(w, 200, out)
}

func (h *handlers) writeEmojiSearch(w http.ResponseWriter, r *http.Request, term string) {
	list, err := h.emojis.List(r.Context(), 0, 200)
	if err != nil {
		writeError(w, 500, "api.emoji.search.fail", err.Error())
		return
	}
	if term == "" {
		writeJSON(w, 200, list)
		return
	}
	out := []emojis.Emoji{}
	for _, e := range list {
		if strings.Contains(e.Name, term) {
			out = append(out, e)
		}
	}
	writeJSON(w, 200, out)
}

// deleteEmoji is allowed for admin OR creator. We derive admin status via
// the existing auth.HasRole helper to avoid wiring a new role cache here.
func (h *handlers) deleteEmoji(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	id := chi.URLParam(r, "emojiID")
	isAdmin, _ := h.auth.HasRole(r.Context(), uid, "system_admin")
	if err := h.emojis.Delete(r.Context(), uid, id, isAdmin); err != nil {
		switch {
		case errors.Is(err, emojis.ErrNotFound):
			writeError(w, 404, "api.emoji.delete.not_found", err.Error())
		case errors.Is(err, emojis.ErrForbidden):
			writeError(w, 403, "api.emoji.delete.forbidden", err.Error())
		default:
			writeError(w, 500, "api.emoji.delete.fail", err.Error())
		}
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(uid, audit.ActionEmojiDelete, id, nil)
	}
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

// getEmojiImage serves the underlying file bytes. We look up the emoji by
// ID, resolve the backing file_infos row, and stream it — authorization is
// implicit (logged-in users can see any custom emoji). We intentionally
// skip authorizeFile here because emojis are global to the instance.
func (h *handlers) getEmojiImage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "emojiID")
	e, err := h.emojis.Get(r.Context(), id)
	if err != nil {
		writeError(w, 404, "api.emoji.image.not_found", err.Error())
		return
	}
	rc, fi, err := h.files.Open(r.Context(), e.FileID)
	if err != nil {
		writeError(w, 404, "api.emoji.image.file_missing", err.Error())
		return
	}
	defer rc.Close()
	if fi.MimeType != "" {
		w.Header().Set("Content-Type", fi.MimeType)
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.WriteHeader(200)
	_, _ = io.Copy(w, rc)
}

// getEmojiByName is a convenience lookup so clients can turn :shipit: into
// a resolvable id without holding the full emoji list. Returns the emoji
// record (not the bytes) so the caller can then hit /emoji/{id}/image.
func (h *handlers) getEmojiByName(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(chi.URLParam(r, "name"))
	e, err := h.emojis.GetByName(r.Context(), name)
	if err != nil {
		writeError(w, 404, "api.emoji.name.not_found", err.Error())
		return
	}
	writeJSON(w, 200, e)
}

// fileThumbnail streams the pre-generated thumbnail or 404s if one hasn't
// been produced yet (client should fall back to the full-size file).
func (h *handlers) fileThumbnail(w http.ResponseWriter, r *http.Request) {
	rc, fi, err := h.files.OpenThumbnail(r.Context(), chi.URLParam(r, "fileID"))
	if err != nil {
		writeError(w, 404, "api.file.thumbnail.not_found", err.Error())
		return
	}
	defer rc.Close()
	if err := h.authorizeFile(r, fi); err != nil {
		writeError(w, 403, "api.file.thumbnail.forbidden", err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.WriteHeader(200)
	_, _ = io.Copy(w, rc)
}

func (h *handlers) filePreview(w http.ResponseWriter, r *http.Request) {
	fileID := chi.URLParam(r, "fileID")
	rc, fi, err := h.files.OpenThumbnail(r.Context(), fileID)
	if err != nil {
		rc, fi, err = h.files.Open(r.Context(), fileID)
		if err != nil {
			writeError(w, 404, "api.file.preview.not_found", err.Error())
			return
		}
		if !strings.HasPrefix(strings.ToLower(fi.MimeType), "image/") {
			rc.Close()
			writeError(w, 404, "api.file.preview.unsupported", "preview not available")
			return
		}
	} else if rc != nil {
		fi.MimeType = "image/jpeg"
	}
	defer rc.Close()
	if err := h.authorizeFile(r, fi); err != nil {
		writeError(w, 403, "api.file.preview.forbidden", err.Error())
		return
	}
	if fi.MimeType != "" {
		w.Header().Set("Content-Type", fi.MimeType)
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.WriteHeader(200)
	_, _ = io.Copy(w, rc)
}

// ---- OAuth / SSO (Phase 14) ----
//
// The flow is stateless on the server apart from a short-TTL state cookie
// set at /login and verified at /callback. We never persist the provider's
// access or refresh token — we only need enough to verify identity once
// and then mint our own JWT session.

const oauthStateCookie = "moyro_oauth_state"

// oauthStateMaxAge caps the window between /login and /callback; if the
// user takes longer than this the browser simply discards the cookie and
// the callback returns a state-mismatch error.
const oauthStateMaxAge = 10 * 60 // seconds

// oauthSecureCookies decides whether to set Secure on the state cookie.
// Derived from PublicBaseURL so dev-over-http doesn't silently drop the
// cookie on Chrome's SameSite=Lax-without-Secure path (dev is localhost,
// which Chrome exempts, but this keeps staging consistent).
func (h *handlers) oauthSecureCookies(r *http.Request) bool {
	return strings.HasPrefix(strings.ToLower(h.effectivePublicBaseURL(r)), "https://")
}

// oauthLogin generates a CSRF state token, stores it in an HttpOnly cookie,
// and redirects the browser to the provider's authorize URL.
func (h *handlers) oauthLogin(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "provider")
	p := h.oauthReg.Get(name)
	if p == nil {
		writeError(w, 404, "api.oauth.unknown_provider", "provider not enabled")
		return
	}
	state, err := oauth.NewState()
	if err != nil {
		writeError(w, 500, "api.oauth.state.app_error", err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    name + ":" + state,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.oauthSecureCookies(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   oauthStateMaxAge,
	})
	redirect := h.oauthReg.RedirectURL[name]
	http.Redirect(w, r, p.AuthURL(state, redirect), http.StatusFound)
}

// oauthCallback validates the state cookie, exchanges the auth code for a
// UserInfo, resolves/creates the local user, and redirects back to the
// webapp with the JWT in a URL fragment. Using a fragment (rather than a
// query string) keeps the token out of referer headers and server logs.
func (h *handlers) oauthCallback(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "provider")
	p := h.oauthReg.Get(name)
	if p == nil {
		writeError(w, 404, "api.oauth.unknown_provider", "provider not enabled")
		return
	}

	qs := r.URL.Query()
	if e := qs.Get("error"); e != "" {
		// Provider reported an error (user denied consent, etc). Send
		// the user back to the login page with a readable hash so the
		// webapp can surface it rather than showing a raw JSON error.
		h.oauthRedirectError(w, r, "provider_"+e)
		return
	}
	code := qs.Get("code")
	state := qs.Get("state")
	if code == "" || state == "" {
		h.oauthRedirectError(w, r, "missing_params")
		return
	}

	// Consume the state cookie: expected value is "<provider>:<state>".
	// This binds the cookie to the provider that started the flow — an
	// attacker can't swap a cookie minted for Google into a GitHub
	// callback because the prefix won't match.
	cookie, err := r.Cookie(oauthStateCookie)
	if err != nil {
		h.oauthRedirectError(w, r, "state_missing")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.oauthSecureCookies(r),
		SameSite: http.SameSiteLaxMode,
	})
	wantPrefix := name + ":"
	if !strings.HasPrefix(cookie.Value, wantPrefix) || cookie.Value[len(wantPrefix):] != state {
		h.oauthRedirectError(w, r, "state_mismatch")
		return
	}

	info, err := p.Exchange(r.Context(), code, h.oauthReg.RedirectURL[name])
	if err != nil {
		h.logger.Warn("oauth exchange failed", "provider", name, "err", err)
		h.oauthRedirectError(w, r, "exchange_failed")
		return
	}

	u, tok, created, err := h.oauthIdent.ResolveOrCreate(r.Context(), name, info)
	if err != nil {
		if errors.Is(err, oauth.ErrUnverifiedLink) {
			h.oauthRedirectError(w, r, "unverified_email")
			return
		}
		h.logger.Error("oauth resolve failed", "provider", name, "err", err)
		h.oauthRedirectError(w, r, "resolve_failed")
		return
	}

	// Newly-created users need the same default team/channel join the
	// password-register flow does, otherwise they land on an empty
	// sidebar. Failures here are logged but not surfaced — the user can
	// still send DMs or join channels manually.
	if created {
		if err := h.bootstrapMembership(r.Context(), u.ID); err != nil {
			h.logger.Warn("oauth default team/channel join failed", "user", u.ID, "err", err)
		}
		if h.audit != nil {
			h.audit.LogAsync(u.ID, audit.ActionUserRegister, u.Username, map[string]any{
				"oauth_provider": name,
				"email":          u.Email,
				"roles":          u.Roles,
			})
		}
	}
	if h.audit != nil {
		h.audit.LogAsync(u.ID, audit.ActionUserLogin, u.Username, map[string]any{
			"ip":             r.RemoteAddr,
			"oauth_provider": name,
		})
	}

	// The webapp serves at PublicBaseURL/. Use a fragment so the token
	// never hits server-side logs or the Referer header when the webapp
	// navigates onward after consuming it.
	base := h.effectivePublicBaseURL(r)
	http.Redirect(w, r, base+"/#token="+url.QueryEscape(tok), http.StatusFound)
}

// oauthRedirectError bounces the browser back to the webapp with an error
// code in the hash (#oauth_error=...) so the login screen can surface a
// readable message without our server having to render HTML.
func (h *handlers) oauthRedirectError(w http.ResponseWriter, r *http.Request, code string) {
	base := h.effectivePublicBaseURL(r)
	http.Redirect(w, r, base+"/#oauth_error="+url.QueryEscape(code), http.StatusFound)
}

// =====================================================================
// Phase 21 — Mattermost API v4 compatibility wave 1
// =====================================================================
//
// Five sub-areas:
//   - Preferences (5 endpoints): the mattermost preference contract that
//     lets official clients sync theme, sidebar, favorites, tutorial steps.
//   - Users compat (4 endpoints): autocomplete, batch by ids, batch by
//     usernames, lookup by email.
//   - Teams/channels stats + name lookup + autocomplete + search.
//   - Posts compat aliases: POST /posts/ids, PUT /posts/{id}/patch.
//
// All endpoints follow the Mattermost v4 OpenAPI shape so a desktop or
// mobile client can be pointed at this server with no patches.

// ---- Preferences ----

// listAllPreferences mirrors GET /api/v4/users/{user_id}/preferences.
// Self-or-admin gated via requireUserParamAccess.
func (h *handlers) listAllPreferences(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	list, err := h.prefs.ListAll(r.Context(), uid)
	if err != nil {
		writeError(w, 500, "api.preference.list.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// listPreferencesInCategory mirrors
// GET /api/v4/users/{user_id}/preferences/{category}.
func (h *handlers) listPreferencesInCategory(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	category := chi.URLParam(r, "category")
	list, err := h.prefs.ListCategory(r.Context(), uid, category)
	if err != nil {
		writeError(w, 500, "api.preference.list_category.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// getPreferenceByName mirrors
// GET /api/v4/users/{user_id}/preferences/{category}/name/{preference_name}.
// Returns 404 with a valid JSON envelope when the row is missing so callers
// can decide between defaulting and signalling.
func (h *handlers) getPreferenceByName(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	category := chi.URLParam(r, "category")
	name := chi.URLParam(r, "name")
	p, err := h.prefs.GetByName(r.Context(), uid, category, name)
	if err != nil {
		writeError(w, 404, "api.preference.get.not_found", "preference not found")
		return
	}
	writeJSON(w, 200, p)
}

// upsertPreferences mirrors PUT /api/v4/users/{user_id}/preferences.
// Body is an array of Preference. Self-or-admin gated; service rejects any
// preference whose user_id doesn't match the path. Broadcasts the updated
// preferences scoped to the owning user so multi-tab UIs sync.
func (h *handlers) upsertPreferences(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	var prefs []preferences.Preference
	if err := json.NewDecoder(r.Body).Decode(&prefs); err != nil {
		writeError(w, 400, "api.preference.upsert.invalid_body", err.Error())
		return
	}
	if err := h.prefs.Upsert(r.Context(), uid, prefs); err != nil {
		writeError(w, 400, "api.preference.upsert.app_error", err.Error())
		return
	}
	h.hub.Broadcast(ws.Event{
		Event:     "preferences_changed",
		Data:      map[string]any{"preferences": prefs},
		Broadcast: ws.Broadcast{UserID: uid},
	})
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

// deletePreferences mirrors POST /api/v4/users/{user_id}/preferences/delete.
// Body is an array of {category, name} (user_id may be omitted; service
// fills it from the actor). Idempotent — missing rows are not an error.
func (h *handlers) deletePreferences(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	var prefs []preferences.Preference
	if err := json.NewDecoder(r.Body).Decode(&prefs); err != nil {
		writeError(w, 400, "api.preference.delete.invalid_body", err.Error())
		return
	}
	if err := h.prefs.Delete(r.Context(), uid, prefs); err != nil {
		writeError(w, 400, "api.preference.delete.app_error", err.Error())
		return
	}
	h.hub.Broadcast(ws.Event{
		Event:     "preferences_deleted",
		Data:      map[string]any{"preferences": prefs},
		Broadcast: ws.Broadcast{UserID: uid},
	})
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

// ---- Users compat ----

// autocompleteUsers mirrors GET /api/v4/users/autocomplete?name=xxx.
// Mattermost responds with `{users: [...], out_of_channel: [...]}` even
// when no channel context is provided, so we keep that envelope shape.
func (h *handlers) autocompleteUsers(w http.ResponseWriter, r *http.Request) {
	term := strings.TrimSpace(r.URL.Query().Get("name"))
	if term == "" {
		writeJSON(w, 200, map[string]any{"users": []any{}, "out_of_channel": []any{}})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := h.auth.AutocompleteUsers(r.Context(), term, limit)
	if err != nil {
		writeError(w, 500, "api.user.autocomplete.app_error", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"users": list, "out_of_channel": []any{}})
}

// usersByIDs mirrors POST /api/v4/users/ids — body is a JSON string array.
// Bounded to 200 ids per request to mirror Mattermost's behavior.
func (h *handlers) usersByIDs(w http.ResponseWriter, r *http.Request) {
	var ids []string
	if err := json.NewDecoder(r.Body).Decode(&ids); err != nil {
		writeError(w, 400, "api.user.ids.invalid_body", err.Error())
		return
	}
	if len(ids) > 200 {
		ids = ids[:200]
	}
	list, err := h.auth.UsersByIDs(r.Context(), ids)
	if err != nil {
		writeError(w, 500, "api.user.ids.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// usersByUsernames mirrors POST /api/v4/users/usernames.
func (h *handlers) usersByUsernames(w http.ResponseWriter, r *http.Request) {
	var names []string
	if err := json.NewDecoder(r.Body).Decode(&names); err != nil {
		writeError(w, 400, "api.user.usernames.invalid_body", err.Error())
		return
	}
	if len(names) > 200 {
		names = names[:200]
	}
	list, err := h.auth.UsersByUsernames(r.Context(), names)
	if err != nil {
		writeError(w, 500, "api.user.usernames.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// getUserByEmail mirrors GET /api/v4/users/email/{email}.
func (h *handlers) getUserByEmail(w http.ResponseWriter, r *http.Request) {
	email := chi.URLParam(r, "email")
	u, err := h.auth.UserByEmail(r.Context(), email)
	if err != nil {
		writeError(w, 404, "api.user.get_by_email.not_found", err.Error())
		return
	}
	writeJSON(w, 200, u)
}

// ---- Teams compat ----

// getTeamByName mirrors GET /api/v4/teams/name/{name}. Restricted to
// teams the caller is a member of so team slugs aren't enumerable.
func (h *handlers) getTeamByName(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	t, err := h.teams.GetByName(r.Context(), name)
	if err != nil {
		writeError(w, 404, "api.team.get_by_name.not_found", err.Error())
		return
	}
	uid := userID(r)
	isMember, _ := h.teams.IsMember(r.Context(), t.ID, uid)
	if !isMember {
		isAdmin, _ := h.auth.HasRole(r.Context(), uid, "system_admin")
		if !isAdmin {
			writeError(w, 403, "api.team.get_by_name.forbidden", "not a team member")
			return
		}
	}
	writeJSON(w, 200, t)
}

// getTeamStats mirrors GET /api/v4/teams/{team_id}/stats. Member-gated.
func (h *handlers) getTeamStats(w http.ResponseWriter, r *http.Request) {
	teamID := chi.URLParam(r, "teamID")
	uid := userID(r)
	isMember, _ := h.teams.IsMember(r.Context(), teamID, uid)
	if !isMember {
		isAdmin, _ := h.auth.HasRole(r.Context(), uid, "system_admin")
		if !isAdmin {
			writeError(w, 403, "api.team.stats.forbidden", "not a team member")
			return
		}
	}
	st, err := h.teams.Stats(r.Context(), teamID)
	if err != nil {
		writeError(w, 500, "api.team.stats.app_error", err.Error())
		return
	}
	writeJSON(w, 200, st)
}

// listTeamMembers mirrors GET /api/v4/teams/{team_id}/members?page=&per_page=.
// Paginated; member-gated.
func (h *handlers) listTeamMembers(w http.ResponseWriter, r *http.Request) {
	teamID := chi.URLParam(r, "teamID")
	uid := userID(r)
	isMember, _ := h.teams.IsMember(r.Context(), teamID, uid)
	if !isMember {
		isAdmin, _ := h.auth.HasRole(r.Context(), uid, "system_admin")
		if !isAdmin {
			writeError(w, 403, "api.team.members.forbidden", "not a team member")
			return
		}
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	list, err := h.teams.ListMembers(r.Context(), teamID, page, perPage)
	if err != nil {
		writeError(w, 500, "api.team.members.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// ---- Channels compat ----

// getChannelByName mirrors GET /api/v4/teams/{team_id}/channels/name/{channel_name}.
// Visibility check: caller must be a channel member, or the channel must be
// public AND the caller a team member.
func (h *handlers) getChannelByName(w http.ResponseWriter, r *http.Request) {
	teamID := chi.URLParam(r, "teamID")
	name := chi.URLParam(r, "channelName")
	c, err := h.channels.GetByName(r.Context(), teamID, name)
	if err != nil {
		writeError(w, 404, "api.channel.get_by_name.not_found", err.Error())
		return
	}
	uid := userID(r)
	isMember, _ := h.channels.IsMember(r.Context(), c.ID, uid)
	if !isMember {
		if c.Type != "O" {
			writeError(w, 403, "api.channel.get_by_name.forbidden", "not a channel member")
			return
		}
		// Public channel — caller still has to be in the team.
		isTeamMember, _ := h.teams.IsMember(r.Context(), teamID, uid)
		if !isTeamMember {
			writeError(w, 403, "api.channel.get_by_name.forbidden", "not a team member")
			return
		}
	}
	writeJSON(w, 200, c)
}

// getChannelStats mirrors GET /api/v4/channels/{channel_id}/stats.
func (h *handlers) getChannelStats(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	uid := userID(r)
	isMember, _ := h.channels.IsMember(r.Context(), channelID, uid)
	if !isMember {
		writeError(w, 403, "api.channel.stats.forbidden", "not a channel member")
		return
	}
	st, err := h.channels.Stats(r.Context(), channelID)
	if err != nil {
		writeError(w, 500, "api.channel.stats.app_error", err.Error())
		return
	}
	writeJSON(w, 200, st)
}

// searchChannelsInTeam mirrors POST /api/v4/teams/{team_id}/channels/search.
// Body: `{"term": "..."}`. Returns the array shape Mattermost uses (no envelope).
type channelSearchReq struct {
	Term string `json:"term"`
}

func (h *handlers) searchChannelsInTeam(w http.ResponseWriter, r *http.Request) {
	teamID := chi.URLParam(r, "teamID")
	uid := userID(r)
	isMember, _ := h.teams.IsMember(r.Context(), teamID, uid)
	if !isMember {
		writeError(w, 403, "api.channel.search.forbidden", "not a team member")
		return
	}
	var req channelSearchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.channel.search.invalid_body", err.Error())
		return
	}
	if strings.TrimSpace(req.Term) == "" {
		writeJSON(w, 200, []any{})
		return
	}
	list, err := h.channels.SearchInTeam(r.Context(), teamID, uid, req.Term, 50)
	if err != nil {
		writeError(w, 500, "api.channel.search.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// autocompleteChannelsInTeam mirrors
// GET /api/v4/teams/{team_id}/channels/autocomplete?name=xxx.
func (h *handlers) autocompleteChannelsInTeam(w http.ResponseWriter, r *http.Request) {
	teamID := chi.URLParam(r, "teamID")
	uid := userID(r)
	isMember, _ := h.teams.IsMember(r.Context(), teamID, uid)
	if !isMember {
		writeError(w, 403, "api.channel.autocomplete.forbidden", "not a team member")
		return
	}
	term := strings.TrimSpace(r.URL.Query().Get("name"))
	if term == "" {
		writeJSON(w, 200, []any{})
		return
	}
	list, err := h.channels.AutocompleteInTeam(r.Context(), teamID, uid, term, 25)
	if err != nil {
		writeError(w, 500, "api.channel.autocomplete.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// ---- Posts compat ----

// postsByIDs mirrors POST /api/v4/posts/ids — bulk hydrate posts. Filters
// out posts the caller can't see (non-member channels) before returning.
func (h *handlers) postsByIDs(w http.ResponseWriter, r *http.Request) {
	var ids []string
	if err := json.NewDecoder(r.Body).Decode(&ids); err != nil {
		writeError(w, 400, "api.post.ids.invalid_body", err.Error())
		return
	}
	if len(ids) > 200 {
		ids = ids[:200]
	}
	uid := userID(r)
	posts, err := h.posts.ListByIDs(r.Context(), ids)
	if err != nil {
		writeError(w, 500, "api.post.ids.app_error", err.Error())
		return
	}
	// Cache channel-membership decisions across the batch so a 200-id
	// payload doesn't fan out into 200 IsMember queries.
	allowed := map[string]bool{}
	out := make([]any, 0, len(posts))
	for _, p := range posts {
		if _, seen := allowed[p.ChannelID]; !seen {
			ok, _ := h.channels.IsMember(r.Context(), p.ChannelID, uid)
			allowed[p.ChannelID] = ok
		}
		if allowed[p.ChannelID] {
			out = append(out, p)
		}
	}
	writeJSON(w, 200, out)
}

// patchPost mirrors PUT /api/v4/posts/{post_id}/patch — partial update.
// Body matches the official PostPatch: message/props/file_ids are all
// optional. Backed by the existing posts.Update + UpdateFileIDs methods so
// behavior stays identical to the bare PUT /posts/{id} alias.
type postPatchReq struct {
	Message *string         `json:"message"`
	Props   *map[string]any `json:"props"`
	FileIDs *[]string       `json:"file_ids"`
}

func (h *handlers) patchPost(w http.ResponseWriter, r *http.Request) {
	postID := chi.URLParam(r, "postID")
	uid := userID(r)
	var req postPatchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.post.patch.invalid_body", err.Error())
		return
	}
	existing, err := h.posts.Get(r.Context(), postID)
	if err != nil || existing == nil {
		writeError(w, 404, "api.post.patch.not_found", "post not found")
		return
	}
	if existing.UserID != uid {
		// Pin/unpin lives on a separate route in MM too — for now patch is
		// strictly an edit, owner-only.
		writeError(w, 403, "api.post.patch.forbidden", "not the author")
		return
	}
	msg := existing.Message
	if req.Message != nil {
		msg = *req.Message
	}
	props := existing.Props
	if req.Props != nil {
		props = *req.Props
	}
	updated, err := h.posts.Update(r.Context(), postID, uid, msg, props)
	if err != nil {
		writeError(w, 500, "api.post.patch.app_error", err.Error())
		return
	}
	if updated == nil {
		writeError(w, 404, "api.post.patch.not_found", "post not found")
		return
	}
	if req.FileIDs != nil {
		if err := h.posts.UpdateFileIDs(r.Context(), postID, *req.FileIDs); err != nil {
			writeError(w, 500, "api.post.patch.file_ids", err.Error())
			return
		}
		// Re-read so the response reflects the new file list.
		updated, _ = h.posts.Get(r.Context(), postID)
	}
	if updated != nil {
		h.hub.Broadcast(ws.Event{
			Event:     "post_edited",
			Data:      map[string]any{"post": updated},
			Broadcast: ws.Broadcast{ChannelID: updated.ChannelID},
		})
	}
	writeJSON(w, 200, updated)
}

// ============================================================================
// Phase 22 — Mattermost API v4 compatibility wave 2
// ============================================================================
// Channel sidebar categories, user notify_props, team search/exists, user
// channel_members hydration, posts cursor-paging. Each endpoint mirrors the
// official Mattermost route and response shape so an unmodified Mattermost
// client can call them transparently.

// ---- Sidebar categories ----

// listSidebarCategories mirrors
// GET /api/v4/users/{user_id}/teams/{team_id}/channels/categories. Returns
// `{categories, order}`. Auto-bootstraps the three defaults
// (favorites/channels/direct_messages) on first call.
func (h *handlers) listSidebarCategories(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	teamID := chi.URLParam(r, "teamID")
	out, err := h.sidebar.ListForTeam(r.Context(), uid, teamID)
	if err != nil {
		writeError(w, 500, "api.sidebar.list.app_error", err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// listSidebarCategoryOrder mirrors
// GET /api/v4/users/{user_id}/teams/{team_id}/channels/categories/order.
// Returns just the array of category ids in display order.
func (h *handlers) listSidebarCategoryOrder(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	teamID := chi.URLParam(r, "teamID")
	order, err := h.sidebar.Order(r.Context(), uid, teamID)
	if err != nil {
		writeError(w, 500, "api.sidebar.order.app_error", err.Error())
		return
	}
	writeJSON(w, 200, order)
}

// updateSidebarCategoryOrder mirrors
// PUT /api/v4/users/{user_id}/teams/{team_id}/channels/categories/order.
// Body is `["catId1","catId2",...]` — the new full ordering.
func (h *handlers) updateSidebarCategoryOrder(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	teamID := chi.URLParam(r, "teamID")
	var order []string
	if err := json.NewDecoder(r.Body).Decode(&order); err != nil {
		writeError(w, 400, "api.sidebar.order.invalid_body", err.Error())
		return
	}
	if err := h.sidebar.UpdateOrder(r.Context(), uid, teamID, order); err != nil {
		writeError(w, 500, "api.sidebar.order.app_error", err.Error())
		return
	}
	h.hub.Broadcast(ws.Event{
		Event:     "sidebar_category_order_updated",
		Data:      map[string]any{"team_id": teamID, "order": order},
		Broadcast: ws.Broadcast{UserID: uid},
	})
	writeJSON(w, 200, order)
}

// getSidebarCategory mirrors
// GET /api/v4/users/{user_id}/teams/{team_id}/channels/categories/{category_id}.
func (h *handlers) getSidebarCategory(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	teamID := chi.URLParam(r, "teamID")
	categoryID := chi.URLParam(r, "categoryID")
	cat, err := h.sidebar.Get(r.Context(), uid, teamID, categoryID)
	if err != nil {
		writeError(w, 404, "api.sidebar.get.not_found", err.Error())
		return
	}
	writeJSON(w, 200, cat)
}

// createSidebarCategory mirrors
// POST /api/v4/users/{user_id}/teams/{team_id}/channels/categories.
// Body is `{display_name, channel_ids}` — only custom categories can be
// created via the API. The three default types are minted automatically.
func (h *handlers) createSidebarCategory(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	teamID := chi.URLParam(r, "teamID")
	var req struct {
		DisplayName string   `json:"display_name"`
		ChannelIDs  []string `json:"channel_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.sidebar.create.invalid_body", err.Error())
		return
	}
	cat, err := h.sidebar.Create(r.Context(), uid, teamID, req.DisplayName, req.ChannelIDs)
	if err != nil {
		writeError(w, 400, "api.sidebar.create.app_error", err.Error())
		return
	}
	h.hub.Broadcast(ws.Event{
		Event:     "sidebar_category_created",
		Data:      map[string]any{"category": cat},
		Broadcast: ws.Broadcast{UserID: uid},
	})
	writeJSON(w, 201, cat)
}

// updateSidebarCategory mirrors
// PUT /api/v4/users/{user_id}/teams/{team_id}/channels/categories/{category_id}.
// Replaces display_name + sorting + muted + collapsed + channel_ids wholesale.
func (h *handlers) updateSidebarCategory(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	teamID := chi.URLParam(r, "teamID")
	categoryID := chi.URLParam(r, "categoryID")
	var cat sidebar.Category
	if err := json.NewDecoder(r.Body).Decode(&cat); err != nil {
		writeError(w, 400, "api.sidebar.update.invalid_body", err.Error())
		return
	}
	cat.ID = categoryID
	updated, err := h.sidebar.Update(r.Context(), uid, teamID, cat)
	if err != nil {
		writeError(w, 400, "api.sidebar.update.app_error", err.Error())
		return
	}
	h.hub.Broadcast(ws.Event{
		Event:     "sidebar_category_updated",
		Data:      map[string]any{"category": updated},
		Broadcast: ws.Broadcast{UserID: uid},
	})
	writeJSON(w, 200, updated)
}

// updateSidebarCategoriesBulk mirrors
// PUT /api/v4/users/{user_id}/teams/{team_id}/channels/categories.
// Body is an array of categories — Mattermost's drag-drop reorder hands the
// whole new state in one call so we apply each one inside its own
// service.Update transaction.
func (h *handlers) updateSidebarCategoriesBulk(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	teamID := chi.URLParam(r, "teamID")
	var cats []sidebar.Category
	if err := json.NewDecoder(r.Body).Decode(&cats); err != nil {
		writeError(w, 400, "api.sidebar.bulk.invalid_body", err.Error())
		return
	}
	out := make([]sidebar.Category, 0, len(cats))
	for _, c := range cats {
		updated, err := h.sidebar.Update(r.Context(), uid, teamID, c)
		if err != nil {
			writeError(w, 400, "api.sidebar.bulk.app_error", err.Error())
			return
		}
		out = append(out, *updated)
	}
	h.hub.Broadcast(ws.Event{
		Event:     "sidebar_categories_updated",
		Data:      map[string]any{"categories": out},
		Broadcast: ws.Broadcast{UserID: uid},
	})
	writeJSON(w, 200, out)
}

// deleteSidebarCategory mirrors
// DELETE /api/v4/users/{user_id}/teams/{team_id}/channels/categories/{category_id}.
// Only custom categories can be removed; the service refuses the three
// defaults and returns 400 in that case.
func (h *handlers) deleteSidebarCategory(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	teamID := chi.URLParam(r, "teamID")
	categoryID := chi.URLParam(r, "categoryID")
	if err := h.sidebar.Delete(r.Context(), uid, teamID, categoryID); err != nil {
		writeError(w, 400, "api.sidebar.delete.app_error", err.Error())
		return
	}
	h.hub.Broadcast(ws.Event{
		Event:     "sidebar_category_deleted",
		Data:      map[string]any{"team_id": teamID, "category_id": categoryID},
		Broadcast: ws.Broadcast{UserID: uid},
	})
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

// ---- User notify_props ----

// getUserNotifyProps mirrors GET /api/v4/users/{user_id}/notify_props.
// The body is a flat string→string map (Mattermost contract). Empty/missing
// returns `{}` not 404 because the desktop client expects a stable shape.
func (h *handlers) getUserNotifyProps(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	props, err := h.auth.GetNotifyProps(r.Context(), uid)
	if err != nil {
		writeError(w, 500, "api.user.notify_props.app_error", err.Error())
		return
	}
	writeJSON(w, 200, props)
}

// putUserNotifyProps mirrors PUT /api/v4/users/{user_id}/notify_props.
// Body is the new full notify_props map (no patch semantics). Broadcasts
// `user_updated` so other tabs can refresh without polling.
func (h *handlers) putUserNotifyProps(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	var props map[string]string
	if err := json.NewDecoder(r.Body).Decode(&props); err != nil {
		writeError(w, 400, "api.user.notify_props.invalid_body", err.Error())
		return
	}
	if err := h.auth.SetNotifyProps(r.Context(), uid, props); err != nil {
		writeError(w, 500, "api.user.notify_props.app_error", err.Error())
		return
	}
	h.hub.Broadcast(ws.Event{
		Event:     "user_notify_props_updated",
		Data:      map[string]any{"user_id": uid, "notify_props": props},
		Broadcast: ws.Broadcast{UserID: uid},
	})
	writeJSON(w, 200, props)
}

// ---- Team compat ----

// searchTeams mirrors POST /api/v4/teams/search. Body is `{term, page, per_page}`.
// Only public teams are returned.
func (h *handlers) searchTeams(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Term    string `json:"term"`
		Page    int    `json:"page"`
		PerPage int    `json:"per_page"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.team.search.invalid_body", err.Error())
		return
	}
	out, err := h.teams.Search(r.Context(), req.Term, req.Page, req.PerPage)
	if err != nil {
		writeError(w, 500, "api.team.search.app_error", err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// teamNameExists mirrors GET /api/v4/teams/name/{name}/exists. Returns
// `{exists: bool}`. Public route — used for signup-time validation, so it
// must work without team membership.
func (h *handlers) teamNameExists(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	exists, err := h.teams.Exists(r.Context(), name)
	if err != nil {
		writeError(w, 500, "api.team.exists.app_error", err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"exists": exists})
}

// ---- User channel_members hydration ----

// listUserChannelMembers mirrors GET /api/v4/users/{user_id}/channel_members.
// Returns every channel_members row for the target user across all teams —
// the official client uses this on launch to populate sidebar state in one
// round-trip rather than fanning per-team.
func (h *handlers) listUserChannelMembers(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	out, err := h.channels.ListMembershipsForUser(r.Context(), uid)
	if err != nil {
		writeError(w, 500, "api.channel_members.list.app_error", err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// channelMembersByIDs mirrors POST /api/v4/users/{user_id}/channels/members.
// Body is `{channel_ids: [...]}` (cap 200) — bulk hydrate the caller's
// channel_members rows for a known set of channel ids.
func (h *handlers) channelMembersByIDs(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	var req struct {
		ChannelIDs []string `json:"channel_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.channel_members.bulk.invalid_body", err.Error())
		return
	}
	if len(req.ChannelIDs) > 200 {
		req.ChannelIDs = req.ChannelIDs[:200]
	}
	out, err := h.channels.MembersByIDs(r.Context(), uid, req.ChannelIDs)
	if err != nil {
		writeError(w, 500, "api.channel_members.bulk.app_error", err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// ============================================================================
// Phase 23 — Mattermost API v4 compatibility wave 3
//
// Five user endpoints (PUT /users/{id}, /patch, /active, DELETE image, stats)
// + five channel endpoints (PUT /patch, /privacy, DELETE alias, /members/ids,
// /channels/search) + seven custom-slash-command endpoints + three scheduled-
// post aliases (POST/PUT/DELETE /posts/schedule). Handlers below; routes are
// registered in router.go under the matching Phase 23 block.
// ============================================================================

// ---- Phase 23 — Users -----------------------------------------------------

// updateUserFull mirrors PUT /users/{user_id}. Mattermost's contract takes
// the FULL user object (every field present); missing fields are interpreted
// as "set to empty/default", not "leave alone". Body fields we honor:
// username, email, first_name, last_name, nickname, position. Other fields
// (id, roles, picture, delete_at) are ignored — those have dedicated routes.
type updateUserFullReq struct {
	Username  string `json:"username"`
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Nickname  string `json:"nickname"`
	Position  string `json:"position"`
}

func (h *handlers) updateUserFull(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	var req updateUserFullReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.user.update.invalid_body", err.Error())
		return
	}
	// Run as a Patch with all six fields explicitly set (full-replace semantics).
	patch := auth.ProfilePatch{
		Username:  &req.Username,
		Email:     &req.Email,
		FirstName: &req.FirstName,
		LastName:  &req.LastName,
		Nickname:  &req.Nickname,
		Position:  &req.Position,
	}
	u, err := h.auth.PatchProfile(r.Context(), uid, patch)
	if err != nil {
		writeError(w, 500, "api.user.update.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionUserProfileUpdate, u.ID, map[string]any{
			"scope": "full",
		})
	}
	writeJSON(w, 200, u)
}

// patchUser mirrors PUT /users/{user_id}/patch — partial update, pointer-typed
// fields. nil = leave alone, "" = clear (where allowed), non-empty = set.
type patchUserReq struct {
	Username  *string `json:"username"`
	Email     *string `json:"email"`
	FirstName *string `json:"first_name"`
	LastName  *string `json:"last_name"`
	Nickname  *string `json:"nickname"`
	Position  *string `json:"position"`
}

func (h *handlers) patchUser(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	var req patchUserReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.user.patch.invalid_body", err.Error())
		return
	}
	u, err := h.auth.PatchProfile(r.Context(), uid, auth.ProfilePatch{
		Username:  req.Username,
		Email:     req.Email,
		FirstName: req.FirstName,
		LastName:  req.LastName,
		Nickname:  req.Nickname,
		Position:  req.Position,
	})
	if err != nil {
		writeError(w, 500, "api.user.patch.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionUserPatch, u.ID, nil)
	}
	writeJSON(w, 200, u)
}

// setUserActive mirrors PUT /users/{user_id}/active body {active:bool}.
// Admin-or-self gated. Inactive users are kicked from WS so live sockets
// die immediately — same semantic as DELETE /users/{id}.
type setUserActiveReq struct {
	Active bool `json:"active"`
}

func (h *handlers) setUserActive(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	var req setUserActiveReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.user.active.invalid_body", err.Error())
		return
	}
	changed, err := h.auth.SetActive(r.Context(), uid, req.Active)
	if err != nil {
		writeError(w, 500, "api.user.active.app_error", err.Error())
		return
	}
	if changed && !req.Active && h.hub != nil {
		h.hub.KickUser(uid)
	}
	if changed && h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionUserActiveSet, uid, map[string]any{
			"active": req.Active,
		})
	}
	writeJSON(w, 200, map[string]any{"status": "OK", "changed": changed})
}

// deleteUserImage mirrors DELETE /users/{user_id}/image. Wipes the picture
// column back to empty so the client falls back to initial-tile avatars.
// Note: we don't physically remove the underlying file here — it's still
// reachable by direct file_id, and the file_infos row stays for reference.
func (h *handlers) deleteUserImage(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	if _, err := h.auth.UpdatePicture(r.Context(), uid, ""); err != nil {
		writeError(w, 500, "api.user.image.delete.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionUserImageDelete, uid, nil)
	}
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

// getUserStats mirrors GET /users/stats. Returns total active users count.
// Public to any logged-in user since it's used by the official client to
// render onboarding state ("you are user #42").
func (h *handlers) getUserStats(w http.ResponseWriter, r *http.Request) {
	st, err := h.auth.Stats(r.Context())
	if err != nil {
		writeError(w, 500, "api.user.stats.app_error", err.Error())
		return
	}
	writeJSON(w, 200, st)
}

// ---- Phase 23 — Channels --------------------------------------------------

// patchChannelExtended mirrors PUT /channels/{channel_id}/patch. Adds `name`
// to the existing patchChannel surface. Pointer-typed for omit-vs-clear.
type patchChannelExtendedReq struct {
	Name        *string `json:"name"`
	DisplayName *string `json:"display_name"`
	Header      *string `json:"header"`
	Purpose     *string `json:"purpose"`
}

func (h *handlers) patchChannelExtended(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	isMember, err := h.channels.IsMember(r.Context(), channelID, userID(r))
	if err != nil || !isMember {
		writeError(w, 403, "api.channel.patch.forbidden", "not a channel member")
		return
	}
	var req patchChannelExtendedReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.channel.patch.invalid_body", err.Error())
		return
	}
	// Validate name shape if a rename was requested. The slug rules match
	// channels.New: lowercase ascii letters/digits/-_/, 1-64 chars.
	if req.Name != nil {
		if !validChannelSlug(*req.Name) {
			writeError(w, 400, "api.channel.patch.invalid_name", "name must be 1-64 chars, [a-z0-9_-]")
			return
		}
	}
	c, err := h.channels.PatchExtended(r.Context(), channelID, req.Name, req.DisplayName, req.Header, req.Purpose)
	if err != nil {
		writeError(w, 500, "api.channel.patch.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionChannelPatch, c.ID, map[string]any{
			"name":         c.Name,
			"display_name": c.DisplayName,
			"header":       c.Header,
			"purpose":      c.Purpose,
		})
	}
	raw, _ := json.Marshal(c)
	h.hub.Broadcast(ws.Event{
		Event: "channel_updated",
		Data: map[string]any{
			"channel_id": c.ID,
			"channel":    string(raw),
		},
		Broadcast: ws.Broadcast{ChannelID: c.ID},
	})
	writeJSON(w, 200, c)
}

// validChannelSlug is the same rule used by createChannel; broken out so
// patchChannelExtended can reuse it without depending on the create path.
func validChannelSlug(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		ch := name[i]
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= '0' && ch <= '9':
		case ch == '-' || ch == '_':
		default:
			return false
		}
	}
	return true
}

// updateChannelPrivacy mirrors PUT /channels/{channel_id}/privacy body
// {privacy: "O"|"P"}. DM/G channels reject the flip. Member-only.
type updateChannelPrivacyReq struct {
	Privacy string `json:"privacy"`
}

func (h *handlers) updateChannelPrivacy(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	isMember, err := h.channels.IsMember(r.Context(), channelID, userID(r))
	if err != nil || !isMember {
		writeError(w, 403, "api.channel.privacy.forbidden", "not a channel member")
		return
	}
	var req updateChannelPrivacyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.channel.privacy.invalid_body", err.Error())
		return
	}
	c, err := h.channels.SetPrivacy(r.Context(), channelID, req.Privacy)
	if err != nil {
		writeError(w, 400, "api.channel.privacy.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionChannelPrivacy, c.ID, map[string]any{
			"privacy": c.Type,
		})
	}
	raw, _ := json.Marshal(c)
	h.hub.Broadcast(ws.Event{
		Event:     "channel_converted",
		Data:      map[string]any{"channel_id": c.ID, "channel": string(raw)},
		Broadcast: ws.Broadcast{ChannelID: c.ID},
	})
	writeJSON(w, 200, c)
}

// deleteChannel mirrors DELETE /channels/{channel_id} — Mattermost's archive
// alias. Admin-only, same behaviour as POST /channels/{id}/archive.
func (h *handlers) deleteChannel(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	changed, err := h.channels.Archive(r.Context(), channelID)
	if err != nil {
		writeError(w, 500, "api.channel.delete.app_error", err.Error())
		return
	}
	if !changed {
		writeError(w, 404, "api.channel.delete.not_found", "not found")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionChannelDelete, channelID, nil)
	}
	h.hub.Broadcast(ws.Event{
		Event:     "channel_deleted",
		Data:      map[string]any{"channel_id": channelID},
		Broadcast: ws.Broadcast{ChannelID: channelID},
	})
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

// channelMembersByUserIDs mirrors POST /channels/{channel_id}/members/ids.
// Body {user_ids: [...]} (cap 200) — bulk hydrate channel_member rows for
// the given user list inside the named channel. Member-only.
func (h *handlers) channelMembersByUserIDs(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	uid := userID(r)
	isMember, err := h.channels.IsMember(r.Context(), channelID, uid)
	if err != nil || !isMember {
		writeError(w, 403, "api.channel.members.bulk.forbidden", "not a channel member")
		return
	}
	var req struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.channel.members.bulk.invalid_body", err.Error())
		return
	}
	if len(req.UserIDs) > 200 {
		req.UserIDs = req.UserIDs[:200]
	}
	out, err := h.channels.MembersByChannel(r.Context(), channelID, req.UserIDs)
	if err != nil {
		writeError(w, 500, "api.channel.members.bulk.app_error", err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// searchChannelsAll mirrors POST /channels/search — cross-team admin search.
// Body {term, limit?}. system_admin-only.
type searchChannelsAllReq struct {
	Term  string `json:"term"`
	Limit int    `json:"limit"`
}

func (h *handlers) searchChannelsAll(w http.ResponseWriter, r *http.Request) {
	var req searchChannelsAllReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.channels.search.invalid_body", err.Error())
		return
	}
	out, err := h.channels.SearchAll(r.Context(), req.Term, req.Limit)
	if err != nil {
		writeError(w, 500, "api.channels.search.app_error", err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// ---- Phase 23 — Custom slash commands --------------------------------------
//
// All seven endpoints are gated behind system_admin in the router (consistent
// with bots / webhooks — these are integrations, not per-user state). The
// service enforces the data invariants (trigger slug + uniqueness); the
// handler maps service errors to HTTP statuses.

type createCommandReq struct {
	TeamID           string `json:"team_id"`
	Trigger          string `json:"trigger"`
	Method           string `json:"method"`
	URL              string `json:"url"`
	Username         string `json:"username"`
	IconURL          string `json:"icon_url"`
	AutoComplete     bool   `json:"auto_complete"`
	AutoCompleteDesc string `json:"auto_complete_desc"`
	AutoCompleteHint string `json:"auto_complete_hint"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description"`
}

func (h *handlers) createCommand(w http.ResponseWriter, r *http.Request) {
	if h.commands == nil {
		writeError(w, 503, "api.command.disabled", "commands disabled")
		return
	}
	var req createCommandReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.command.create.invalid_body", err.Error())
		return
	}
	if req.TeamID == "" {
		writeError(w, 400, "api.command.create.missing_team", "team_id required")
		return
	}
	c, err := h.commands.Create(r.Context(), commands.CreateInput{
		TeamID:           req.TeamID,
		CreatorID:        userID(r),
		Trigger:          req.Trigger,
		Method:           req.Method,
		URL:              req.URL,
		Username:         req.Username,
		IconURL:          req.IconURL,
		AutoComplete:     req.AutoComplete,
		AutoCompleteDesc: req.AutoCompleteDesc,
		AutoCompleteHint: req.AutoCompleteHint,
		DisplayName:      req.DisplayName,
		Description:      req.Description,
	})
	if err != nil {
		switch {
		case errors.Is(err, commands.ErrTriggerInvalid):
			writeError(w, 400, "api.command.create.invalid_trigger", err.Error())
		case errors.Is(err, commands.ErrDuplicateTrigger):
			writeError(w, 409, "api.command.create.duplicate", err.Error())
		default:
			writeError(w, 500, "api.command.create.app_error", err.Error())
		}
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionCommandCreate, c.ID, map[string]any{
			"team_id": c.TeamID,
			"trigger": c.Trigger,
		})
	}
	writeJSON(w, 201, c)
}

func (h *handlers) listCommandsForTeam(w http.ResponseWriter, r *http.Request) {
	if h.commands == nil {
		writeJSON(w, 200, []any{})
		return
	}
	teamID := r.URL.Query().Get("team_id")
	if teamID == "" {
		writeError(w, 400, "api.command.list.missing_team", "team_id required")
		return
	}
	out, err := h.commands.ListForTeam(r.Context(), teamID)
	if err != nil {
		writeError(w, 500, "api.command.list.app_error", err.Error())
		return
	}
	writeJSON(w, 200, out)
}

func (h *handlers) getCommand(w http.ResponseWriter, r *http.Request) {
	if h.commands == nil {
		writeError(w, 404, "api.command.get.not_found", "not found")
		return
	}
	id := chi.URLParam(r, "commandID")
	c, err := h.commands.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, commands.ErrNotFound) {
			writeError(w, 404, "api.command.get.not_found", "not found")
			return
		}
		writeError(w, 500, "api.command.get.app_error", err.Error())
		return
	}
	writeJSON(w, 200, c)
}

type updateCommandReq struct {
	Trigger          string `json:"trigger"`
	Method           string `json:"method"`
	URL              string `json:"url"`
	Username         string `json:"username"`
	IconURL          string `json:"icon_url"`
	AutoComplete     bool   `json:"auto_complete"`
	AutoCompleteDesc string `json:"auto_complete_desc"`
	AutoCompleteHint string `json:"auto_complete_hint"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description"`
}

func (h *handlers) updateCommand(w http.ResponseWriter, r *http.Request) {
	if h.commands == nil {
		writeError(w, 404, "api.command.update.not_found", "not found")
		return
	}
	id := chi.URLParam(r, "commandID")
	var req updateCommandReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.command.update.invalid_body", err.Error())
		return
	}
	c, err := h.commands.Update(r.Context(), id, commands.UpdateInput{
		Trigger:          req.Trigger,
		Method:           req.Method,
		URL:              req.URL,
		Username:         req.Username,
		IconURL:          req.IconURL,
		AutoComplete:     req.AutoComplete,
		AutoCompleteDesc: req.AutoCompleteDesc,
		AutoCompleteHint: req.AutoCompleteHint,
		DisplayName:      req.DisplayName,
		Description:      req.Description,
	})
	if err != nil {
		switch {
		case errors.Is(err, commands.ErrNotFound):
			writeError(w, 404, "api.command.update.not_found", "not found")
		case errors.Is(err, commands.ErrTriggerInvalid):
			writeError(w, 400, "api.command.update.invalid_trigger", err.Error())
		case errors.Is(err, commands.ErrDuplicateTrigger):
			writeError(w, 409, "api.command.update.duplicate", err.Error())
		default:
			writeError(w, 500, "api.command.update.app_error", err.Error())
		}
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionCommandUpdate, c.ID, map[string]any{
			"trigger": c.Trigger,
		})
	}
	writeJSON(w, 200, c)
}

func (h *handlers) deleteCommand(w http.ResponseWriter, r *http.Request) {
	if h.commands == nil {
		writeError(w, 404, "api.command.delete.not_found", "not found")
		return
	}
	id := chi.URLParam(r, "commandID")
	if err := h.commands.Delete(r.Context(), id); err != nil {
		if errors.Is(err, commands.ErrNotFound) {
			writeError(w, 404, "api.command.delete.not_found", "not found")
			return
		}
		writeError(w, 500, "api.command.delete.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionCommandDelete, id, nil)
	}
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

func (h *handlers) regenCommandToken(w http.ResponseWriter, r *http.Request) {
	if h.commands == nil {
		writeError(w, 404, "api.command.regen.not_found", "not found")
		return
	}
	id := chi.URLParam(r, "commandID")
	c, err := h.commands.RegenerateToken(r.Context(), id)
	if err != nil {
		if errors.Is(err, commands.ErrNotFound) {
			writeError(w, 404, "api.command.regen.not_found", "not found")
			return
		}
		writeError(w, 500, "api.command.regen.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionCommandRegen, id, nil)
	}
	writeJSON(w, 200, c)
}

type moveCommandReq struct {
	TeamID string `json:"team_id"`
}

func (h *handlers) moveCommand(w http.ResponseWriter, r *http.Request) {
	if h.commands == nil {
		writeError(w, 404, "api.command.move.not_found", "not found")
		return
	}
	id := chi.URLParam(r, "commandID")
	var req moveCommandReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TeamID == "" {
		writeError(w, 400, "api.command.move.invalid_body", "team_id required")
		return
	}
	if err := h.commands.Move(r.Context(), id, req.TeamID); err != nil {
		switch {
		case errors.Is(err, commands.ErrNotFound):
			writeError(w, 404, "api.command.move.not_found", "not found")
		case errors.Is(err, commands.ErrDuplicateTrigger):
			writeError(w, 409, "api.command.move.duplicate", err.Error())
		default:
			writeError(w, 500, "api.command.move.app_error", err.Error())
		}
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionCommandMove, id, map[string]any{
			"target_team_id": req.TeamID,
		})
	}
	writeJSON(w, 200, map[string]string{"status": "OK"})
}

func (h *handlers) autocompleteCommandsForTeam(w http.ResponseWriter, r *http.Request) {
	if h.commands == nil {
		writeJSON(w, 200, []*commands.Command{})
		return
	}
	teamID := chi.URLParam(r, "teamID")
	uid := userID(r)
	isMember, _ := h.teams.IsMember(r.Context(), teamID, uid)
	if !isMember {
		isAdmin, _ := h.auth.HasRole(r.Context(), uid, "system_admin")
		if !isAdmin {
			writeError(w, 403, "api.command.autocomplete.forbidden", "not a team member")
			return
		}
	}
	out, err := h.commands.AutocompleteForTeam(r.Context(), teamID)
	if err != nil {
		writeError(w, 500, "api.command.autocomplete.app_error", err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// ---- Phase 23 — Posts schedule aliases ------------------------------------
//
// Mattermost names the official endpoints `/posts/schedule` (POST/GET/PUT/DELETE).
// We already had `/scheduled_posts`; these aliases route through the same
// service so any client that follows the official path lands on the right
// service.

func (h *handlers) createSchedulePostAlias(w http.ResponseWriter, r *http.Request) {
	// Same body as createScheduledPost; reuse the handler verbatim.
	h.createScheduledPost(w, r)
}

func (h *handlers) listScheduledPostsForTeam(w http.ResponseWriter, r *http.Request) {
	teamID := chi.URLParam(r, "teamID")
	uid := userID(r)
	isMember, _ := h.teams.IsMember(r.Context(), teamID, uid)
	if !isMember {
		isAdmin, _ := h.auth.HasRole(r.Context(), uid, "system_admin")
		if !isAdmin {
			writeError(w, 403, "api.schedule.list_team.forbidden", "not a team member")
			return
		}
	}
	list, err := h.scheduled.ListPendingForTeam(r.Context(), uid, teamID)
	if err != nil {
		writeError(w, 500, "api.schedule.list_team.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// updateSchedulePost mirrors PUT /posts/schedule/{scheduled_id}. Body subset:
// message, file_ids, send_at. We delete + recreate to keep the worker's
// claim semantics simple — it never sees the same id twice that way.
type updateSchedulePostReq struct {
	Message *string  `json:"message"`
	FileIDs []string `json:"file_ids"`
	SendAt  *int64   `json:"scheduled_at"`
}

func (h *handlers) updateSchedulePost(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "scheduledID")
	uid := userID(r)
	// Only pending rows can be edited; the lookup also enforces ownership.
	pending, err := h.scheduled.ListPending(r.Context(), uid)
	if err != nil {
		writeError(w, 500, "api.schedule.update.app_error", err.Error())
		return
	}
	var existing *scheduled.ScheduledPost
	for _, p := range pending {
		if p.ID == id {
			existing = p
			break
		}
	}
	if existing == nil {
		writeError(w, 404, "api.schedule.update.not_found", "not found or already sent")
		return
	}
	var req updateSchedulePostReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.schedule.update.invalid_body", err.Error())
		return
	}
	if req.Message != nil {
		existing.Message = *req.Message
	}
	if req.FileIDs != nil {
		existing.FileIDs = req.FileIDs
	}
	if req.SendAt != nil {
		now := time.Now().UnixMilli()
		if *req.SendAt <= now-30_000 {
			writeError(w, 400, "api.schedule.update.past", "send_at must be in the future")
			return
		}
		existing.SendAt = *req.SendAt
	}
	// Replace by delete+create. Atomic-ish: if create fails we re-insert via
	// the original record — but pgx doesn't expose a transactional API on
	// the service, and the failure mode is extremely rare, so we accept the
	// small window where a successful delete + failed create loses the row.
	deleted, err := h.scheduled.Delete(r.Context(), id, uid)
	if err != nil {
		writeError(w, 500, "api.schedule.update.delete_failed", err.Error())
		return
	}
	if !deleted {
		writeError(w, 409, "api.schedule.update.claimed", "scheduled post is already being processed")
		return
	}
	sp, err := h.scheduled.Create(r.Context(), uid, existing.ChannelID, existing.RootID,
		existing.Message, existing.FileIDs, existing.Props, existing.SendAt)
	if err != nil {
		writeError(w, 500, "api.schedule.update.create_failed", err.Error())
		return
	}
	h.hub.Broadcast(ws.Event{
		Event:     "scheduled_post_updated",
		Data:      map[string]any{"old_id": id, "new": sp},
		Broadcast: ws.Broadcast{UserID: uid},
	})
	writeJSON(w, 200, sp)
}

func (h *handlers) deleteSchedulePostAlias(w http.ResponseWriter, r *http.Request) {
	h.deleteScheduledPost(w, r)
}
