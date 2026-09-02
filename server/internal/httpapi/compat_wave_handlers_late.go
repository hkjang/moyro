// Mattermost API v4 compatibility waves 7-9.
//
// Split out of compat_wave_handlers.go, which had grown past the point
// where the file could be read or reviewed. The cut follows the existing
// wave banners, so each file still holds whole waves and no handler moved
// between them. Everything remains in package httpapi and is wired by
// router.go exactly as before.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/audit"
	"github.com/hkjang/moyro/server/internal/auth"
	"github.com/hkjang/moyro/server/internal/bookmarks"
	"github.com/hkjang/moyro/server/internal/bots"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/reactions"
	"github.com/hkjang/moyro/server/internal/tos"
	"github.com/hkjang/moyro/server/internal/ws"
)

// =====================================================================
// Phase 27 — Mattermost API v4 compatibility wave 7.
//
// Sweeps endpoints from the back of the audit's missing list, again
// orthogonal to codex's compat_handlers.go / admin_compat_handlers.go.
// Five pillars, 14 endpoints, zero schema changes. All logic added to
// existing services (auth, bots) plus this trailing handler block.
// =====================================================================

// ---- Pillar A — Bot lifecycle (5 endpoints) ----

// GET /bots/{botID} (admin) — single-bot fetch. Mattermost ships both a
// list endpoint and a single-id endpoint; the list path is already wired
// at /bots, this fills in the parity gap.
func (h *handlers) getBot(w http.ResponseWriter, r *http.Request) {
	bid := chi.URLParam(r, "botID")
	if bid == "" {
		writeError(w, 400, "api.bot.get.missing_id", "bot id required")
		return
	}
	b, err := h.bots.Get(r.Context(), bid)
	if err != nil {
		if errors.Is(err, bots.ErrBotNotFound) {
			writeError(w, 404, "api.bot.get.not_found", "bot not found")
			return
		}
		writeError(w, 500, "api.bot.get.app_error", err.Error())
		return
	}
	writeJSON(w, 200, b)
}

// POST /bots/{botID}/disable (admin) — alias of DELETE /bots/{botID}.
// Mattermost ships both shapes; some admin tools POST-with-action,
// others DELETE-with-id. Same underlying call; same audit event.
func (h *handlers) disableBotByPost(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	bid := chi.URLParam(r, "botID")
	if bid == "" {
		writeError(w, 400, "api.bot.disable.missing_id", "bot id required")
		return
	}
	if err := h.bots.Disable(r.Context(), bid); err != nil {
		if errors.Is(err, bots.ErrBotNotFound) {
			writeError(w, 404, "api.bot.disable.not_found", "bot not found")
			return
		}
		writeError(w, 500, "api.bot.disable.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionUserDeactivate, bid, map[string]any{"is_bot": true})
	}
	// After disable, kick any live WS sockets so a stale browser tab
	// running the bot's UI (rare but possible if a human convert-to-bot
	// flow was reversed) loses connectivity immediately.
	if h.hub != nil {
		h.hub.KickUser(bid)
	}
	if b, err := h.bots.Get(r.Context(), bid); err == nil {
		writeJSON(w, 200, b)
		return
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// POST /bots/{botID}/enable (admin) — un-soft-deletes a previously
// disabled bot. Does NOT auto-restore PATs (Disable revokes them; the
// admin must mint a fresh token). Returns the updated bot row.
func (h *handlers) enableBot(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	bid := chi.URLParam(r, "botID")
	if bid == "" {
		writeError(w, 400, "api.bot.enable.missing_id", "bot id required")
		return
	}
	if err := h.bots.Enable(r.Context(), bid); err != nil {
		if errors.Is(err, bots.ErrBotNotFound) {
			writeError(w, 404, "api.bot.enable.not_found", "bot not found or already enabled")
			return
		}
		writeError(w, 500, "api.bot.enable.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionBotEnable, bid, nil)
	}
	b, err := h.bots.Get(r.Context(), bid)
	if err != nil {
		writeJSON(w, 200, map[string]any{"status": "OK"})
		return
	}
	writeJSON(w, 200, b)
}

type convertToUserReq struct {
	Password string `json:"password"`
}

// POST /bots/{botID}/convert_to_user (admin) — inverse of convert_to_bot.
// Body is `{password}` — the bot row keeps its id but flips back to a
// human user with the supplied bcrypt-hashed password. All outstanding
// PATs are revoked in the same transaction.
func (h *handlers) convertBotToUser(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	bid := chi.URLParam(r, "botID")
	if bid == "" {
		writeError(w, 400, "api.bot.convert_to_user.missing_id", "bot id required")
		return
	}
	var req convertToUserReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.bot.convert_to_user.invalid_body", err.Error())
		return
	}
	if strings.TrimSpace(req.Password) == "" {
		writeError(w, 400, "api.bot.convert_to_user.password_required", "password required")
		return
	}
	if err := h.auth.ConvertBotToUser(r.Context(), bid, req.Password); err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			writeError(w, 404, "api.bot.convert_to_user.not_found", "bot not found")
			return
		}
		if errors.Is(err, auth.ErrInvalidPassword) {
			writeError(w, 400, "api.bot.convert_to_user.invalid_password", err.Error())
			return
		}
		writeError(w, 500, "api.bot.convert_to_user.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionBotConvertToUser, bid, nil)
	}
	u, err := h.auth.UserByID(r.Context(), bid)
	if err != nil || u == nil {
		writeJSON(w, 200, map[string]any{"status": "OK"})
		return
	}
	writeJSON(w, 200, u)
}

// POST /bots/{botID}/assign/{userID} (admin) — reassigns bot ownership
// to a different user. The new owner must exist; we reject unknown ids
// up front so the bot isn't left orphaned. Returns the updated bot row.
func (h *handlers) assignBotOwner(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	bid := chi.URLParam(r, "botID")
	newOwner := chi.URLParam(r, "userID")
	if bid == "" || newOwner == "" {
		writeError(w, 400, "api.bot.assign.missing_param", "bot id and user id required")
		return
	}
	if owner, err := h.auth.UserByID(r.Context(), newOwner); err != nil || owner == nil {
		writeError(w, 404, "api.bot.assign.owner_missing", "new owner not found")
		return
	}
	b, err := h.bots.AssignOwner(r.Context(), bid, newOwner)
	if err != nil {
		if errors.Is(err, bots.ErrBotNotFound) {
			writeError(w, 404, "api.bot.assign.not_found", "bot not found")
			return
		}
		writeError(w, 500, "api.bot.assign.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionBotAssign, bid, map[string]any{"new_owner_id": newOwner})
	}
	writeJSON(w, 200, b)
}

// ---- Pillar B — Posts compat (3 endpoints) ----

type ephemeralPostReq struct {
	UserID string         `json:"user_id"`
	Post   map[string]any `json:"post"`
}

// POST /posts/ephemeral (admin) — emits a transient post visible only
// to the target user. We don't persist ephemerals (Mattermost doesn't
// either — they're WS-only payloads with a synthetic id). The fanout
// goes through the WS hub scoped to UserID, so other channel members
// never see it. Useful for slash-command responses with private text.
func (h *handlers) createEphemeralPost(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return
	}
	var req ephemeralPostReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.post.ephemeral.invalid_body", err.Error())
		return
	}
	if req.UserID == "" || req.Post == nil {
		writeError(w, 400, "api.post.ephemeral.invalid_body", "user_id and post required")
		return
	}
	channelID, _ := req.Post["channel_id"].(string)
	if channelID == "" {
		writeError(w, 400, "api.post.ephemeral.missing_channel", "post.channel_id required")
		return
	}
	// Synthesise a post-shaped envelope. Id is a fresh UUID so client
	// caches don't collide with real posts; create_at is now.
	now := time.Now().UnixMilli()
	synth := map[string]any{
		"id":         "ephemeral-" + strconv.FormatInt(now, 36),
		"user_id":    caller,
		"channel_id": channelID,
		"create_at":  now,
		"update_at":  now,
		"type":       "system_ephemeral",
	}
	for k, v := range req.Post {
		// Don't let the body forge id/create_at; preserve the rest.
		if k == "id" || k == "create_at" || k == "update_at" {
			continue
		}
		synth[k] = v
	}
	if h.hub != nil {
		h.hub.Broadcast(ws.Event{
			Event:     "ephemeral_message",
			Broadcast: ws.Broadcast{UserID: req.UserID},
			Data:      map[string]any{"post": synth},
		})
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionPostEphemeral, req.UserID, map[string]any{"channel_id": channelID})
	}
	writeJSON(w, 201, synth)
}

// POST /posts/{postID}/actions/{actionID} — interactive-message action
// dispatch. The official client POSTs here when a user clicks a button
// inside an attachment's `actions` block. We don't support interactive
// integrations server-side yet (no plugin registers action handlers),
// so this is a stable 200 OK stub returning Mattermost's documented
// success envelope so client UIs don't show an error toast on click.
// When a real integration framework lands, this handler grows the
// dispatch logic without a URL change.
func (h *handlers) doPostAction(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return
	}
	pid := chi.URLParam(r, "postID")
	aid := chi.URLParam(r, "actionID")
	if pid == "" || aid == "" {
		writeError(w, 400, "api.post.action.missing_param", "post id and action id required")
		return
	}
	// Drain body for safety; we don't dispatch on it yet.
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 8*1024))
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionPostAction, pid, map[string]any{"action_id": aid})
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

type postIDsReactionsReq struct {
	IDs []string `json:"ids"`
}

// POST /posts/ids/reactions — bulk hydrate reactions for a list of post
// ids. Returns `{post_id: [reactions...]}`. Mattermost's contract caps
// at 200 ids; we mirror that. Visibility-gated by channel membership:
// the reactions for a post in a channel the caller can't see are
// silently filtered out (rather than 403'ing the whole batch).
func (h *handlers) postsByIDsReactions(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return
	}
	// Accept both `{ids:[...]}` and a bare `[...]` for client variation.
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	var ids []string
	if err := json.Unmarshal(body, &ids); err != nil {
		var req postIDsReactionsReq
		if err := json.Unmarshal(body, &req); err != nil {
			writeError(w, 400, "api.post.ids_reactions.invalid_body", "expected ids array or {ids}")
			return
		}
		ids = req.IDs
	}
	if len(ids) == 0 {
		writeJSON(w, 200, map[string][]any{})
		return
	}
	if len(ids) > 200 {
		ids = ids[:200]
	}
	// Per-channel membership cache to avoid IsMember per post.
	memberOf := map[string]bool{}
	out := map[string][]reactions.Reaction{}
	for _, pid := range ids {
		ch, err := h.reactions.ChannelForPost(r.Context(), pid)
		if err != nil || ch == "" {
			continue
		}
		ok, cached := memberOf[ch]
		if !cached {
			ok, _ = h.channels.IsMember(r.Context(), ch, caller)
			memberOf[ch] = ok
		}
		if !ok {
			continue
		}
		list, err := h.reactions.ListForPost(r.Context(), pid)
		if err != nil {
			continue
		}
		out[pid] = list
	}
	writeJSON(w, 200, out)
}

// ---- Pillar C — Email verify + password reset stubs (4 endpoints) ----
//
// We don't have email verification or password-reset email flows in
// production yet; the digest worker is the only outbound mail path.
// These endpoints exist so the official Mattermost desktop/mobile
// clients don't 404 on the "I forgot my password" / "verify your
// email" links — when SMTP-driven flows land, these grow real logic
// without URL changes.

type emailVerifyReq struct {
	Token string `json:"token"`
}

// POST /users/email/verify — public; consumes a verification token.
// Stub: returns 200 OK without touching state.
func (h *handlers) verifyUserEmail(w http.ResponseWriter, r *http.Request) {
	var req emailVerifyReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	if h.audit != nil {
		// caller is unauthenticated here, so actor is empty.
		h.audit.LogAsync("", audit.ActionEmailVerify, "", nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

type emailVerifySendReq struct {
	Email string `json:"email"`
}

// POST /users/email/verify/send — public; requests a verification mail.
// Stub: returns 200 OK regardless of whether the email exists, so the
// endpoint can't be used as an email-existence oracle.
func (h *handlers) sendUserEmailVerification(w http.ResponseWriter, r *http.Request) {
	var req emailVerifySendReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	if h.audit != nil {
		h.audit.LogAsync("", audit.ActionEmailVerifySend, req.Email, nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

type passwordResetReq struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// POST /users/password/reset — public; consumes a reset token.
// Stub: returns 200 OK. When SMTP reset lands, this validates the
// token and calls AdminSetPassword (or a dedicated method).
func (h *handlers) consumePasswordReset(w http.ResponseWriter, r *http.Request) {
	var req passwordResetReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	if h.audit != nil {
		h.audit.LogAsync("", audit.ActionPasswordResetDo, "", nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

type passwordResetSendReq struct {
	Email string `json:"email"`
}

// POST /users/password/reset/send — public; requests a reset mail.
// Stub: returns 200 OK regardless of email existence (same anti-oracle
// posture as verify/send).
func (h *handlers) sendPasswordResetEmail(w http.ResponseWriter, r *http.Request) {
	var req passwordResetSendReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	if h.audit != nil {
		h.audit.LogAsync("", audit.ActionPasswordResetReq, req.Email, nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// ---- Pillar D — User tokens GET (2 endpoints) ----

// GET /users/tokens (admin) — paginated list of every PAT in the system.
// Reuses bots.SearchTokens with an empty term so the same code path
// drives both list and search. Caller has system_admin via the route
// group middleware.
func (h *handlers) listAllUserTokens(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("per_page"))
	if limit <= 0 {
		limit = 100
	}
	tokens, err := h.bots.SearchTokens(r.Context(), "", limit)
	if err != nil {
		writeError(w, 500, "api.user.tokens.list", err.Error())
		return
	}
	writeJSON(w, 200, tokens)
}

// GET /users/tokens/{tokenID} (admin) — single PAT lookup.
func (h *handlers) getUserToken(w http.ResponseWriter, r *http.Request) {
	tid := chi.URLParam(r, "tokenID")
	if tid == "" {
		writeError(w, 400, "api.user.tokens.get.missing_id", "token id required")
		return
	}
	tok, err := h.bots.GetToken(r.Context(), tid)
	if err != nil {
		if errors.Is(err, bots.ErrTokenInvalid) {
			writeError(w, 404, "api.user.tokens.get.not_found", "token not found")
			return
		}
		writeError(w, 500, "api.user.tokens.get.app_error", err.Error())
		return
	}
	writeJSON(w, 200, tok)
}

// ---- Pillar E — Misc compat (notifications/ack + flagged posts) ----

type notificationAckReq struct {
	UserID         string `json:"user_id"`
	PostID         string `json:"post_id"`
	NotificationID string `json:"notification_id"`
	ReceivedAt     int64  `json:"received_at"`
	PlatformType   string `json:"platform"`
}

// POST /notifications/ack — official mobile clients POST here when a
// push notification is delivered. We don't run a push backend yet, so
// the endpoint exists purely so the mobile client's notification
// receipt path doesn't 404. Audit-logged async because volume can be
// high on a live deployment with many mobile users.
func (h *handlers) ackNotification(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return
	}
	var req notificationAckReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionNotificationsAck, req.PostID, map[string]any{
			"platform": req.PlatformType,
		})
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// GET /users/{userID}/posts/flagged — Mattermost's "flagged" concept
// is identical to our "saved posts" (per-user bookmarks). This is a
// thin envelope shape over savedposts.ListIDs that returns the same
// `{order, posts}` shape MeattermostMM uses for GET /channels/{id}/posts.
func (h *handlers) listFlaggedPosts(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("per_page"))
	if limit <= 0 {
		limit = 60
	}
	offset, _ := strconv.Atoi(q.Get("page"))
	if offset > 0 {
		offset = offset * limit
	}
	ids, err := h.saved.ListIDs(r.Context(), uid, limit, offset)
	if err != nil {
		writeError(w, 500, "api.user.flagged.list", err.Error())
		return
	}
	if len(ids) == 0 {
		writeJSON(w, 200, map[string]any{"order": []string{}, "posts": map[string]any{}})
		return
	}
	postsList, err := h.posts.ListByIDsForUser(r.Context(), uid, ids)
	if err != nil {
		writeError(w, 500, "api.user.flagged.hydrate", err.Error())
		return
	}
	postMap := map[string]any{}
	order := []string{}
	for _, p := range postsList {
		postMap[p.ID] = p
		order = append(order, p.ID)
	}
	writeJSON(w, 200, map[string]any{"order": order, "posts": postMap})
}

// =============================================================================
// Phase 28 — Mattermost API v4 compatibility wave 8.
//
// Sweeps the back of the audit's missing list, again orthogonal to codex's
// `compat_handlers.go` and `admin_compat_handlers.go`. Five pillars covering
// post acknowledgments, terms-of-service, MFA, bulk channel members, and a
// few admin-edge stubs. Zero schema changes — every endpoint is either a
// stable 200-OK stub or a thin wrapper over an existing service. The stubs
// exist so the official Mattermost desktop/mobile/web clients don't 404
// against this server when the user lands on a page that probes them.
// When real implementations land later, the URL shapes are stable; the
// handlers grow logic without any client-visible change.
// =============================================================================

// callerIsSystemAdmin is a small helper used by the Phase 28 stubs to gate
// admin-only paths without hard-failing on missing role data.
func (h *handlers) callerIsSystemAdmin(r *http.Request) bool {
	caller := userID(r)
	if caller == "" {
		return false
	}
	ok, _ := h.auth.HasRole(r.Context(), caller, "system_admin")
	return ok
}

// ------ Pillar A — Post acknowledgments (3 endpoints) ------
//
// Phase 33 upgraded these from in-memory stubs to durable rows in the
// post_acknowledgements table. Each ack writes one row per (post_id, user_id)
// idempotently — re-acking touches ack_at. Removing an ack deletes the row.
// The list endpoint is channel-membership-gated so a non-member can't probe
// who has acked a post they shouldn't see.
//
// WS broadcast posture: on ack/unack we emit `post_acknowledgement_changed`
// scoped to the post's channel so every viewer's "Acknowledged by N people"
// badge updates live.

// POST /users/{userID}/posts/{postID}/ack
func (h *handlers) ackPost(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	uid := chi.URLParam(r, "userID")
	if uid == "me" {
		uid = caller
	}
	if uid != caller {
		writeError(w, 403, "api.context.permissions.app_error", "cannot ack as another user")
		return
	}
	pid := chi.URLParam(r, "postID")
	if pid == "" {
		writeError(w, 400, "api.post.id.app_error", "missing post id")
		return
	}
	post, err := h.posts.Get(r.Context(), pid)
	if err != nil || post == nil {
		writeError(w, 404, "api.post.get.app_error", "post not found")
		return
	}
	ok, _ := h.channels.IsMember(r.Context(), post.ChannelID, caller)
	if !ok {
		writeError(w, 403, "api.context.permissions.app_error", "not a channel member")
		return
	}
	ack, err := h.postacks.Save(r.Context(), pid, uid)
	if err != nil {
		writeError(w, 500, "api.post.ack.save.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionPostAck, pid, nil)
	}
	if h.hub != nil {
		h.hub.Broadcast(ws.Event{
			Event: "post_acknowledgement_changed",
			Data: map[string]any{
				"post_id":         pid,
				"user_id":         uid,
				"acknowledged_at": ack.AcknowledgedAt,
				"removed":         false,
			},
			Broadcast: ws.Broadcast{ChannelID: post.ChannelID},
		})
	}
	writeJSON(w, 200, ack)
}

// DELETE /users/{userID}/posts/{postID}/ack
func (h *handlers) unackPost(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	uid := chi.URLParam(r, "userID")
	if uid == "me" {
		uid = caller
	}
	if uid != caller {
		writeError(w, 403, "api.context.permissions.app_error", "cannot unack as another user")
		return
	}
	pid := chi.URLParam(r, "postID")
	post, err := h.posts.Get(r.Context(), pid)
	if err != nil || post == nil {
		writeError(w, 404, "api.post.get.app_error", "post not found")
		return
	}
	if _, err := h.postacks.Delete(r.Context(), pid, uid); err != nil {
		writeError(w, 500, "api.post.unack.delete.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionPostUnack, pid, nil)
	}
	if h.hub != nil {
		h.hub.Broadcast(ws.Event{
			Event: "post_acknowledgement_changed",
			Data: map[string]any{
				"post_id": pid,
				"user_id": uid,
				"removed": true,
			},
			Broadcast: ws.Broadcast{ChannelID: post.ChannelID},
		})
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// GET /posts/{postID}/acknowledgements — returns the actual list of acks
// from the post_acknowledgements table. Channel-membership-gated to avoid
// leaking ack state to non-members.
func (h *handlers) listPostAcknowledgements(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	pid := chi.URLParam(r, "postID")
	post, err := h.posts.Get(r.Context(), pid)
	if err != nil || post == nil {
		writeError(w, 404, "api.post.get.app_error", "post not found")
		return
	}
	ok, _ := h.channels.IsMember(r.Context(), post.ChannelID, caller)
	if !ok && !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "not a channel member")
		return
	}
	acks, err := h.postacks.ListForPost(r.Context(), pid)
	if err != nil {
		writeError(w, 500, "api.post.ack.list.app_error", err.Error())
		return
	}
	writeJSON(w, 200, acks)
}

// ------ Pillar B — Terms of Service (3 endpoints) ------
//
// Phase 33 upgraded these from in-memory stubs to durable rows in the
// terms_of_service + user_terms_of_service tables. POST creates a new
// revision (and soft-deletes the previous active one); GET returns the
// active revision; per-user accept persists a pointer keyed on user_id so
// the user only carries one accepted-TOS pointer at a time.

// GET /terms_of_service — returns the active revision. Returns the
// canonical zero-shape when no TOS is configured (rather than 404) so the
// client renders an empty consent screen instead of an error toast.
func (h *handlers) getTermsOfService(w http.ResponseWriter, r *http.Request) {
	cur, err := h.tos.Current(r.Context())
	if err != nil {
		if errors.Is(err, tos.ErrNoCurrentTOS) {
			writeJSON(w, 200, map[string]any{
				"id":        "",
				"text":      "",
				"create_at": int64(0),
				"user_id":   "",
			})
			return
		}
		writeError(w, 500, "api.tos.get.app_error", err.Error())
		return
	}
	writeJSON(w, 200, cur)
}

// POST /terms_of_service — admin sets a new revision. Soft-deletes the
// prior active row inside the same tx so Current() always resolves the
// new id.
func (h *handlers) updateTermsOfService(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" || !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.tos.body.app_error", err.Error())
		return
	}
	cur, err := h.tos.Create(r.Context(), caller, req.Text)
	if err != nil {
		writeError(w, 500, "api.tos.create.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionTosUpdate, cur.ID, map[string]any{"len": len(req.Text)})
	}
	writeJSON(w, 200, cur)
}

// POST /users/{userID}/terms_of_service — user records that they've
// accepted a TOS revision. Persists the pointer in user_terms_of_service.
func (h *handlers) acceptTermsOfService(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	uid := chi.URLParam(r, "userID")
	if uid == "me" {
		uid = caller
	}
	if uid != caller && !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "cannot accept TOS as another user")
		return
	}
	var req struct {
		ServiceTermsID   string `json:"serviceTermsId"`
		TermsOfServiceID string `json:"termsOfServiceId"`
		Accepted         bool   `json:"accepted"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	tosID := req.ServiceTermsID
	if tosID == "" {
		tosID = req.TermsOfServiceID
	}
	if tosID == "" {
		// Fall back to the current revision so a missing-id payload still
		// records an acceptance against whatever's live now — matches the
		// official client's behaviour when it omits the id.
		if cur, err := h.tos.Current(r.Context()); err == nil {
			tosID = cur.ID
		}
	}
	if tosID == "" {
		writeError(w, 400, "api.tos.accept.no_id.app_error", "no terms_of_service to accept")
		return
	}
	if err := h.tos.Accept(r.Context(), uid, tosID); err != nil {
		writeError(w, 500, "api.tos.accept.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionTosAccept, tosID, map[string]any{"accepted": req.Accepted})
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// ------ Pillar C — MFA stubs (3 endpoints) ------
//
// We don't ship MFA yet. The endpoints exist so the official client's
// "Two-factor authentication" settings page doesn't 404 when the user
// opens it. `mfa_required` always reports false; `generate` returns a
// stable empty-secret payload; `enable/disable` audits and 200s.

// PUT /users/{userID}/mfa
func (h *handlers) setUserMFA(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	uid := chi.URLParam(r, "userID")
	if uid == "me" {
		uid = caller
	}
	if uid != caller && !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "cannot toggle MFA for another user")
		return
	}
	var req struct {
		Activate bool   `json:"activate"`
		Code     string `json:"code"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	action := audit.ActionMFAEnable
	if !req.Activate {
		action = audit.ActionMFADisable
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, action, uid, nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// POST /users/{userID}/mfa/generate — returns a stub QR/secret payload.
// Real impl would generate a TOTP secret and store the pending state.
func (h *handlers) generateUserMFA(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	uid := chi.URLParam(r, "userID")
	if uid == "me" {
		uid = caller
	}
	if uid != caller && !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "cannot generate MFA for another user")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionMFAGenerate, uid, nil)
	}
	writeJSON(w, 200, map[string]any{
		"secret":  "",
		"qr_code": "",
	})
}

// POST /users/mfa — body `{login_id}` returns whether MFA is required for
// the given login id. We always say false because no user has MFA enabled.
// Anti-oracle: returns the same shape regardless of whether the login_id
// resolves to a real user, so this endpoint can't be used to enumerate.
func (h *handlers) checkUserMFA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LoginID string `json:"login_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	writeJSON(w, 200, map[string]any{"mfa_required": false})
}

// ------ Pillar D — Bulk channel members (1 endpoint) ------
//
// `PUT /channels/{id}/members` body `{user_ids:[]}` — bulk-add members.
// Wraps the existing single-user `Join` in a loop with per-member error
// reporting so a partial failure doesn't lose the rest of the batch.
// Caller must hold channel-admin or system-admin.

func (h *handlers) bulkAddChannelMembers(w http.ResponseWriter, r *http.Request) {
	if h.denyGuestMutation(w, r, "api.channel.members.bulk_add.guest_forbidden") {
		return
	}
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	cid := chi.URLParam(r, "channelID")
	if !h.callerCanAdminChannel(r.Context(), cid, caller) {
		writeError(w, 403, "api.context.permissions.app_error", "channel_admin required")
		return
	}
	var req struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.channel.members.body.app_error", err.Error())
		return
	}
	if len(req.UserIDs) == 0 {
		writeError(w, 400, "api.channel.members.empty.app_error", "user_ids empty")
		return
	}
	if len(req.UserIDs) > 200 {
		req.UserIDs = req.UserIDs[:200]
	}
	added := []map[string]any{}
	failed := []map[string]any{}
	for _, uid := range req.UserIDs {
		if uid == "" {
			continue
		}
		if err := h.channels.Join(r.Context(), cid, uid); err != nil {
			failed = append(failed, map[string]any{"user_id": uid, "error": err.Error()})
			continue
		}
		added = append(added, map[string]any{"user_id": uid, "channel_id": cid})
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionChannelMembersBulk, cid, map[string]any{
			"added_count":  len(added),
			"failed_count": len(failed),
		})
	}
	writeJSON(w, 201, map[string]any{
		"members": added,
		"failed":  failed,
	})
}

// ------ Pillar E — Admin-edge stubs (3+ endpoints) ------
//
// Misc back-of-list admin endpoints. `auth/method` flip is a stub because
// we don't switch users between password and OAuth-only post-creation.
// `users/search` reuses the existing autocomplete service for the bulk-
// search wire shape used by admin tooling. `properties/groups` is a 404-
// avoidance stub for an enterprise-only feature surface.

// PUT /users/{userID}/auth — admin force-rotates a user's auth provider
// (e.g. flip a local-password user to LDAP-only). We don't persist a
// per-user auth-method column; the request is audited and 200-OK'd so
// admin tools don't break.
func (h *handlers) setUserAuthMethod(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" || !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	uid := chi.URLParam(r, "userID")
	var req struct {
		AuthData    string `json:"auth_data"`
		AuthService string `json:"auth_service"`
		Password    string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionUserAuthSet, uid, map[string]any{
			"auth_service": req.AuthService,
		})
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// GET /users/{userID}/terms_of_service — per-user TOS acceptance status.
// Until we durably store acceptance, this returns the empty-zero record so
// the official client knows "no acceptance yet" and renders its TOS page
// rather than 500'ing.
func (h *handlers) getUserTermsOfServiceStatus(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	uid := chi.URLParam(r, "userID")
	if uid == "me" {
		uid = caller
	}
	if uid != caller && !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "cannot read another user's TOS state")
		return
	}
	writeJSON(w, 200, map[string]any{
		"user_id":             uid,
		"terms_of_service_id": "",
		"create_at":           int64(0),
	})
}

// GET /users/known — returns the caller's "known users" set. In Mattermost
// this is the union of users sharing any channel with the caller. Cheap
// approximation: route through `channels.ListMembershipsForUser` and
// flatten unique user_ids per shared channel via `channels.MembersByIDs`.
// Caller themselves is excluded from the result.
func (h *handlers) listKnownUsers(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	memberships, err := h.channels.ListMembershipsForUser(r.Context(), caller)
	if err != nil {
		writeError(w, 500, "api.user.known.memberships.app_error", err.Error())
		return
	}
	cids := make([]string, 0, len(memberships))
	for _, m := range memberships {
		cids = append(cids, m.ChannelID)
	}
	known := map[string]struct{}{}
	for _, cid := range cids {
		mems, err := h.channels.ListMembers(r.Context(), cid)
		if err != nil {
			continue
		}
		for _, mm := range mems {
			if mm.UserID != caller {
				known[mm.UserID] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(known))
	for uid := range known {
		out = append(out, uid)
	}
	writeJSON(w, 200, out)
}

// (server_busy handlers live in admin_compat_handlers.go — codex shipped
// them in commit fa3baaf, so we don't redeclare here.)

// =============================================================================
// Phase 29 — Mattermost API v4 compatibility wave 9.
//
// Sweeps the back of the audit's missing list. Five pillars: channel
// bookmarks (real feature, new table), admin diagnostics stubs, hooks GET,
// team-scoped channel listings, and miscellaneous usage/redirect stubs.
// One new schema table (`channel_bookmarks`) + one new package
// (`server/internal/bookmarks`); everything else is additive on existing
// services. Stubs return Mattermost's documented 200-OK envelopes so the
// official client/admin console doesn't 404 on probe.
// =============================================================================

// ------ Pillar A — Channel bookmarks (5 endpoints) ------

// listChannelBookmarks — every channel member can read.
func (h *handlers) listChannelBookmarks(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	cid := chi.URLParam(r, "channelID")
	ok, _ := h.channels.IsMember(r.Context(), cid, caller)
	if !ok && !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "not a channel member")
		return
	}
	list, err := h.bookmarks.List(r.Context(), cid)
	if err != nil {
		writeError(w, 500, "api.bookmark.list.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// createChannelBookmark — any channel member can add bookmarks (matches
// the Mattermost default; admins can pin them via separate channel-admin
// flow when that lands).
type bookmarkCreateReq struct {
	DisplayName string `json:"display_name"`
	LinkURL     string `json:"link_url"`
	ImageURL    string `json:"image_url"`
	Emoji       string `json:"emoji"`
	FileID      string `json:"file_id"`
	Type        string `json:"type"`
}

func (h *handlers) createChannelBookmark(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	cid := chi.URLParam(r, "channelID")
	ok, _ := h.channels.IsMember(r.Context(), cid, caller)
	if !ok && !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "not a channel member")
		return
	}
	var req bookmarkCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.bookmark.body.app_error", err.Error())
		return
	}
	if req.DisplayName == "" && req.LinkURL == "" && req.FileID == "" {
		writeError(w, 400, "api.bookmark.empty.app_error", "display_name, link_url or file_id required")
		return
	}
	b := &bookmarks.Bookmark{
		ChannelID:   cid,
		OwnerID:     caller,
		DisplayName: req.DisplayName,
		LinkURL:     req.LinkURL,
		ImageURL:    req.ImageURL,
		Emoji:       req.Emoji,
		FileID:      req.FileID,
		Type:        req.Type,
	}
	out, err := h.bookmarks.Create(r.Context(), b)
	if err != nil {
		writeError(w, 500, "api.bookmark.create.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionBookmarkCreate, out.ID, map[string]any{"channel_id": cid})
	}
	if h.hub != nil {
		h.hub.Broadcast(ws.Event{
			Event:     "channel_bookmark_created",
			Broadcast: ws.Broadcast{ChannelID: cid},
			Data:      map[string]any{"bookmark": out},
		})
	}
	writeJSON(w, 201, out)
}

// patchChannelBookmark — any channel member can edit.
type bookmarkPatchReq struct {
	DisplayName *string `json:"display_name,omitempty"`
	LinkURL     *string `json:"link_url,omitempty"`
	ImageURL    *string `json:"image_url,omitempty"`
	Emoji       *string `json:"emoji,omitempty"`
	FileID      *string `json:"file_id,omitempty"`
	SortOrder   *int    `json:"sort_order,omitempty"`
}

func (h *handlers) patchChannelBookmark(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	cid := chi.URLParam(r, "channelID")
	bid := chi.URLParam(r, "bookmarkID")
	ok, _ := h.channels.IsMember(r.Context(), cid, caller)
	if !ok && !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "not a channel member")
		return
	}
	var req bookmarkPatchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.bookmark.body.app_error", err.Error())
		return
	}
	out, err := h.bookmarks.Patch(r.Context(), bid, bookmarks.Patch{
		DisplayName: req.DisplayName,
		LinkURL:     req.LinkURL,
		ImageURL:    req.ImageURL,
		Emoji:       req.Emoji,
		FileID:      req.FileID,
		SortOrder:   req.SortOrder,
	})
	if errors.Is(err, bookmarks.ErrNotFound) {
		writeError(w, 404, "api.bookmark.not_found", "no such bookmark")
		return
	}
	if err != nil {
		writeError(w, 500, "api.bookmark.patch.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionBookmarkUpdate, bid, map[string]any{"channel_id": cid})
	}
	if h.hub != nil {
		h.hub.Broadcast(ws.Event{
			Event:     "channel_bookmark_updated",
			Broadcast: ws.Broadcast{ChannelID: cid},
			Data:      map[string]any{"bookmark": out},
		})
	}
	writeJSON(w, 200, out)
}

// deleteChannelBookmark — owner or admin only. Soft-delete via service.
func (h *handlers) deleteChannelBookmark(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	cid := chi.URLParam(r, "channelID")
	bid := chi.URLParam(r, "bookmarkID")
	b, err := h.bookmarks.Get(r.Context(), bid)
	if errors.Is(err, bookmarks.ErrNotFound) {
		writeError(w, 404, "api.bookmark.not_found", "no such bookmark")
		return
	}
	if err != nil {
		writeError(w, 500, "api.bookmark.get.app_error", err.Error())
		return
	}
	if b.OwnerID != caller && !h.callerCanAdminChannel(r.Context(), cid, caller) {
		writeError(w, 403, "api.context.permissions.app_error", "owner or channel_admin required")
		return
	}
	if err := h.bookmarks.Delete(r.Context(), bid); err != nil {
		writeError(w, 500, "api.bookmark.delete.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionBookmarkDelete, bid, map[string]any{"channel_id": cid})
	}
	if h.hub != nil {
		h.hub.Broadcast(ws.Event{
			Event:     "channel_bookmark_deleted",
			Broadcast: ws.Broadcast{ChannelID: cid},
			Data:      map[string]any{"bookmark_id": bid},
		})
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// reorderChannelBookmark — sort_order shuffle.
type bookmarkReorderReq struct {
	SortOrder int `json:"sort_order"`
}

func (h *handlers) reorderChannelBookmark(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	cid := chi.URLParam(r, "channelID")
	bid := chi.URLParam(r, "bookmarkID")
	ok, _ := h.channels.IsMember(r.Context(), cid, caller)
	if !ok && !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "not a channel member")
		return
	}
	var req bookmarkReorderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.bookmark.body.app_error", err.Error())
		return
	}
	if err := h.bookmarks.Reorder(r.Context(), cid, bid, req.SortOrder); err != nil {
		if errors.Is(err, bookmarks.ErrNotFound) {
			writeError(w, 404, "api.bookmark.not_found", "no such bookmark")
			return
		}
		writeError(w, 500, "api.bookmark.reorder.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionBookmarkReorder, bid, map[string]any{"channel_id": cid, "sort_order": req.SortOrder})
	}
	if h.hub != nil {
		h.hub.Broadcast(ws.Event{
			Event:     "channel_bookmark_sort_changed",
			Broadcast: ws.Broadcast{ChannelID: cid},
			Data:      map[string]any{"bookmark_id": bid, "sort_order": req.SortOrder},
		})
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// ------ Pillar B — Admin diagnostics compatibility (10 endpoints) ------
//
// Keep the Mattermost route shapes, but never return a green-check success
// for a diagnostic that moyro did not actually perform. Unsupported runtime
// services return a Mattermost AppError with HTTP 501. Every attempt is still
// audited so operators can distinguish a rejected probe from no activity.

// adminTestEmail — POST /email/test
func (h *handlers) adminTestEmail(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionDiagnosticsTest, "email", map[string]any{"result": "not_supported"})
	}
	writeAdminCompatNotSupported(w, "api.email.test.not_supported.app_error", "SMTP email testing")
}

// adminTestNotifications — POST /notifications/test
func (h *handlers) adminTestNotifications(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionDiagnosticsTest, "notifications", map[string]any{"result": "not_supported"})
	}
	writeAdminCompatNotSupported(w, "api.notifications.test.not_supported.app_error", "notification delivery testing")
}

// adminTestS3 — POST /file/s3_test
func (h *handlers) adminTestS3(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionDiagnosticsTest, "s3", map[string]any{"result": "not_supported"})
	}
	writeAdminCompatNotSupported(w, "api.file.s3_test.not_supported.app_error", "S3 connection testing")
}

// adminTestSiteURL — POST /site_url/test
func (h *handlers) adminTestSiteURL(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionDiagnosticsTest, "site_url", map[string]any{"result": "not_supported"})
	}
	writeAdminCompatNotSupported(w, "api.site_url.test.not_supported.app_error", "legacy SiteURL testing")
}

// adminTestElasticsearch — POST /elasticsearch/test
func (h *handlers) adminTestElasticsearch(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionDiagnosticsTest, "elasticsearch", map[string]any{"result": "not_supported"})
	}
	writeAdminCompatNotSupported(w, "api.elasticsearch.test.not_supported.app_error", "Elasticsearch connection testing")
}

// adminPurgeElasticsearch — POST /elasticsearch/purge_indexes
func (h *handlers) adminPurgeElasticsearch(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionDiagnosticsTest, "elasticsearch.purge", map[string]any{"result": "not_supported"})
	}
	writeAdminCompatNotSupported(w, "api.elasticsearch.purge.not_supported.app_error", "Elasticsearch index purge")
}

// adminPurgeBleve — POST /bleve/purge_indexes
func (h *handlers) adminPurgeBleve(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionDiagnosticsTest, "bleve.purge", map[string]any{"result": "not_supported"})
	}
	writeAdminCompatNotSupported(w, "api.bleve.purge.not_supported.app_error", "Bleve index purge")
}

// adminInvalidateCaches — POST /caches/invalidate
func (h *handlers) adminInvalidateCaches(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionDiagnosticsTest, "caches.invalidate", map[string]any{"result": "not_supported"})
	}
	writeAdminCompatNotSupported(w, "api.caches.invalidate.not_supported.app_error", "cache invalidation")
}

// adminRecycleDB — POST /database/recycle
func (h *handlers) adminRecycleDB(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionDiagnosticsTest, "database.recycle", map[string]any{"result": "not_supported"})
	}
	writeAdminCompatNotSupported(w, "api.database.recycle.not_supported.app_error", "database connection recycling")
}

// adminCheckIntegrity — POST /integrity. A real implementation must run
// cross-table orphan scans; an empty list without a scan is false success.
func (h *handlers) adminCheckIntegrity(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionDiagnosticsTest, "integrity", map[string]any{"result": "not_supported"})
	}
	writeAdminCompatNotSupported(w, "api.integrity.check.not_supported.app_error", "database integrity checking")
}

// ------ Pillar C — Hooks GET (2 endpoints) ------
//
// The webhook update endpoints from Phase 24 take a hook id but don't
// expose a way to read the current row's settings before patching.
// Mattermost's admin console reads the hook via these GETs, then PUTs
// the patch. Both admin-only.

// getIncomingHook — GET /hooks/incoming/{hookID}
func (h *handlers) getIncomingHook(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	hid := chi.URLParam(r, "hookID")
	hook, err := h.incoming.Get(r.Context(), hid)
	if err != nil || hook == nil {
		writeError(w, 404, "api.hook.not_found", "incoming hook not found")
		return
	}
	writeJSON(w, 200, hook)
}

// getOutgoingHook — GET /hooks/outgoing/{hookID}
func (h *handlers) getOutgoingHook(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	hid := chi.URLParam(r, "hookID")
	hook, err := h.outgoing.Get(r.Context(), hid)
	if err != nil || hook == nil {
		writeError(w, 404, "api.hook.not_found", "outgoing hook not found")
		return
	}
	writeJSON(w, 200, hook)
}

// ------ Pillar D — Team channel scopes (3 endpoints) ------
//
// Three team-scoped channel listings the official client uses to populate
// admin views and channel-pickers. All three reuse existing channels.Service
// methods with thin filtering — no new SQL.

// listTeamPrivateChannels — GET /teams/{teamID}/channels/private
// Admin-only system-wide listing of every private channel in a team.
// Used by the admin console "Channels" tab to surface private rooms.
func (h *handlers) listTeamPrivateChannels(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	tid := chi.URLParam(r, "teamID")
	rows, err := h.channels.SearchAll(r.Context(), "", 200)
	if err != nil {
		writeError(w, 500, "api.channels.private.app_error", err.Error())
		return
	}
	out := []channels.Channel{}
	for _, c := range rows {
		if c.TeamID == tid && c.Type == "P" {
			out = append(out, c)
		}
	}
	writeJSON(w, 200, out)
}

// listTeamDeletedChannels — GET /teams/{teamID}/channels/deleted
func (h *handlers) listTeamDeletedChannels(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	tid := chi.URLParam(r, "teamID")
	all, err := h.channels.ListForUserIncludingDeleted(r.Context(), caller, tid, true)
	if err != nil {
		writeError(w, 500, "api.channels.deleted.app_error", err.Error())
		return
	}
	out := []channels.Channel{}
	for _, c := range all {
		if c.DeleteAt > 0 {
			out = append(out, c)
		}
	}
	writeJSON(w, 200, out)
}

// searchAutocompleteTeamChannels — GET /teams/{teamID}/channels/search_autocomplete
// URL-shape alias of POST /teams/{teamID}/channels/search; reads `?term=` and
// reuses the Phase 21 autocomplete service. Mattermost ships both shapes.
func (h *handlers) searchAutocompleteTeamChannels(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	if h.denyGuestEnumeration(w, r, "api.channels.search_autocomplete.guest_forbidden") {
		return
	}
	tid := chi.URLParam(r, "teamID")
	isMember, err := h.teams.IsMember(r.Context(), tid, caller)
	if err != nil {
		writeError(w, 500, "api.channels.search_autocomplete.member_check", err.Error())
		return
	}
	if !isMember {
		writeError(w, 403, "api.channels.search_autocomplete.forbidden", "not a team member")
		return
	}
	term := strings.TrimSpace(r.URL.Query().Get("term"))
	if term == "" {
		term = strings.TrimSpace(r.URL.Query().Get("name"))
	}
	if term == "" {
		writeJSON(w, 200, []any{})
		return
	}
	rows, err := h.channels.AutocompleteInTeam(r.Context(), tid, caller, term, 25)
	if err != nil {
		writeError(w, 500, "api.channels.search_autocomplete.app_error", err.Error())
		return
	}
	writeJSON(w, 200, rows)
}

// ------ Pillar E — Usage + redirect stubs (5 endpoints) ------

// getUsagePosts — GET /usage/posts
func (h *handlers) getUsagePosts(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	var count int64
	_ = h.auth.DB().Pool.QueryRow(r.Context(), `SELECT COUNT(*) FROM posts WHERE delete_at=0`).Scan(&count)
	writeJSON(w, 200, map[string]any{"count": count})
}

// getUsageStorage — GET /usage/storage
func (h *handlers) getUsageStorage(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	var bytes int64
	_ = h.auth.DB().Pool.QueryRow(r.Context(), `SELECT COALESCE(SUM(size),0) FROM file_infos WHERE delete_at=0`).Scan(&bytes)
	writeJSON(w, 200, map[string]any{"bytes": bytes})
}

// getServerLimits — GET /limits/server
func (h *handlers) getServerLimits(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	writeJSON(w, 200, map[string]any{
		"max_users":             0, // unlimited
		"max_active_users":      0,
		"max_message_retention": 0,
	})
}

// getRedirectLocation — GET /redirect_location?url=<u>
// Mattermost surfaces this so the official client can preview where a
// shortlink resolves before navigating. We just echo the input — running
// a real HEAD is expensive and we don't want to be a free SSRF gateway.
func (h *handlers) getRedirectLocation(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	u := r.URL.Query().Get("url")
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionRedirectLookup, u, nil)
	}
	writeJSON(w, 200, map[string]any{"location": u})
}

// getProxiedImage — GET /image?url=<u>
// Mattermost's image-proxy endpoint. We reuse the Phase 18 link-preview
// SSRF-safe fetcher when available, fall back to a 302 redirect to the
// raw URL when the link-preview service is disabled. The 302 path is
// safe because the official client only renders into <img> tags which
// the browser fetches directly without our involvement.
func (h *handlers) getProxiedImage(w http.ResponseWriter, r *http.Request) {
	u := r.URL.Query().Get("url")
	if u == "" {
		writeError(w, 400, "api.image.url.app_error", "missing url")
		return
	}
	// Reuse the link-preview image-proxy when configured; otherwise 302.
	if h.links != nil {
		// Delegate to existing linkPreviewImage shape by re-routing
		// internally. linkPreviewImage reads ?url= the same way.
		h.linkPreviewImage(w, r)
		return
	}
	http.Redirect(w, r, u, http.StatusFound)
}
