package httpapi

import (
	"context"
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
	"github.com/hkjang/moyro/server/internal/bots"
	"github.com/hkjang/moyro/server/internal/teams"
	"github.com/hkjang/moyro/server/internal/userstatus"
	"github.com/hkjang/moyro/server/internal/webhooks"
	"github.com/hkjang/moyro/server/internal/ws"
)

// ----------------------------------------------------------------------
// Phase 24 — Mattermost API v4 compatibility wave 4
//
// 14 endpoints across six independent pillars, picked from the back of the
// audit's missing-list to avoid file conflicts with Phase 23's user/channel
// work. Pillars: thread membership writes, team patch/privacy, webhook+bot
// updates, user admin (roles/password/device), custom status, set-unread.
// ----------------------------------------------------------------------

// --- Pillar A: Threads compat (3 endpoints) ----------------------------

// PUT /users/{userID}/teams/{teamID}/threads/{rootID}/following
// Body: {"following": bool}. Mattermost's "follow this thread" toggle.
type threadFollowReq struct {
	Following bool `json:"following"`
}

func (h *handlers) putThreadFollowing(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	teamID := chi.URLParam(r, "teamID")
	rootID := chi.URLParam(r, "rootID")
	var req threadFollowReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.thread.follow.invalid_body", err.Error())
		return
	}
	if err := h.threads.SetFollowing(r.Context(), uid, teamID, rootID, req.Following); err != nil {
		writeError(w, 500, "api.thread.follow.app_error", err.Error())
		return
	}
	h.audit.LogAsync(uid, audit.ActionThreadFollow, rootID, map[string]any{
		"team_id": teamID, "following": req.Following,
	})
	h.hub.Broadcast(ws.Event{
		Event:     "thread_follow_changed",
		Data:      map[string]any{"root_id": rootID, "team_id": teamID, "following": req.Following},
		Broadcast: ws.Broadcast{UserID: uid},
	})
	w.WriteHeader(http.StatusNoContent)
}

// PUT /users/{userID}/teams/{teamID}/threads/{rootID}/read/{timestamp}
// Stamps last_viewed_at on a single thread. Timestamp = ms since epoch.
func (h *handlers) putThreadRead(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	teamID := chi.URLParam(r, "teamID")
	rootID := chi.URLParam(r, "rootID")
	tsStr := chi.URLParam(r, "timestamp")
	ts, _ := strconv.ParseInt(tsStr, 10, 64)
	viewed, err := h.threads.MarkRead(r.Context(), uid, teamID, rootID, ts)
	if err != nil {
		writeError(w, 500, "api.thread.read.app_error", err.Error())
		return
	}
	h.audit.LogAsync(uid, audit.ActionThreadRead, rootID, map[string]any{
		"team_id": teamID, "viewed_at": viewed,
	})
	h.hub.Broadcast(ws.Event{
		Event:     "thread_read_changed",
		Data:      map[string]any{"root_id": rootID, "team_id": teamID, "last_viewed_at": viewed},
		Broadcast: ws.Broadcast{UserID: uid},
	})
	writeJSON(w, 200, map[string]any{"last_viewed_at": viewed})
}

// PUT /users/{userID}/teams/{teamID}/threads/read
// Marks every thread membership in (user, team) as read at now().
func (h *handlers) putAllThreadsRead(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	teamID := chi.URLParam(r, "teamID")
	viewed, err := h.threads.MarkAllReadInTeam(r.Context(), uid, teamID)
	if err != nil {
		writeError(w, 500, "api.thread.read_all.app_error", err.Error())
		return
	}
	h.audit.LogAsync(uid, audit.ActionThreadReadAll, teamID, nil)
	h.hub.Broadcast(ws.Event{
		Event:     "thread_read_changed_all",
		Data:      map[string]any{"team_id": teamID, "last_viewed_at": viewed},
		Broadcast: ws.Broadcast{UserID: uid},
	})
	writeJSON(w, 200, map[string]any{"last_viewed_at": viewed})
}

// --- Pillar B: Teams CRUD compat (3 endpoints) -------------------------

// PUT /teams/{teamID}
// Full update of mutable team fields. We accept the same body shape as the
// official client and forward to the Patch path with all fields populated.
type updateTeamReq struct {
	DisplayName     *string `json:"display_name"`
	Name            *string `json:"name"`
	Description     *string `json:"description"`
	CompanyName     *string `json:"company_name"`
	AllowedDomains  *string `json:"allowed_domains"`
	AllowOpenInvite *bool   `json:"allow_open_invite"`
}

func (h *handlers) updateTeamFull(w http.ResponseWriter, r *http.Request) {
	if h.denyGuestMutation(w, r, "api.team.update.guest_forbidden") {
		return
	}
	teamID := chi.URLParam(r, "teamID")
	uid := userID(r)
	// Caller must be team_admin or system_admin to mutate the team row.
	if !h.callerCanAdminTeam(r.Context(), teamID, uid) {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "team admin required")
		return
	}
	var req updateTeamReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.team.update.invalid_body", err.Error())
		return
	}
	t, err := h.teams.Patch(r.Context(), teamID, teams.TeamPatch{
		Name: req.Name, DisplayName: req.DisplayName, Description: req.Description,
		CompanyName: req.CompanyName, AllowedDomains: req.AllowedDomains,
		AllowOpenInvite: req.AllowOpenInvite,
	})
	if err != nil {
		writeError(w, 500, "api.team.update.app_error", err.Error())
		return
	}
	if t == nil {
		writeError(w, 404, "api.team.update.not_found", "team not found")
		return
	}
	h.audit.LogAsync(uid, audit.ActionTeamUpdate, teamID, map[string]any{"team": t})
	h.hub.Broadcast(teamScopedEvent("team_updated", teamID, map[string]any{"team": t}))
	writeJSON(w, 200, t)
}

// PUT /teams/{teamID}/patch — same body as PUT /teams/{teamID} but the official
// contract is partial-update (any nil field is left untouched). Our Patch
// already implements partial-update semantics via CASE WHEN, so the two
// handlers share the same plumbing.
func (h *handlers) patchTeam(w http.ResponseWriter, r *http.Request) {
	if h.denyGuestMutation(w, r, "api.team.update.guest_forbidden") {
		return
	}
	h.updateTeamFull(w, r)
}

// PUT /teams/{teamID}/privacy — body {"privacy": "O" | "I"}.
type teamPrivacyReq struct {
	Privacy string `json:"privacy"`
}

func (h *handlers) updateTeamPrivacy(w http.ResponseWriter, r *http.Request) {
	if h.denyGuestMutation(w, r, "api.team.privacy.guest_forbidden") {
		return
	}
	teamID := chi.URLParam(r, "teamID")
	uid := userID(r)
	if !h.callerCanAdminTeam(r.Context(), teamID, uid) {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "team admin required")
		return
	}
	var req teamPrivacyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.team.privacy.invalid_body", err.Error())
		return
	}
	if req.Privacy != "O" && req.Privacy != "I" {
		writeError(w, 400, "api.team.privacy.invalid", "privacy must be O or I")
		return
	}
	t, err := h.teams.SetPrivacy(r.Context(), teamID, req.Privacy)
	if err != nil {
		writeError(w, 500, "api.team.privacy.app_error", err.Error())
		return
	}
	if t == nil {
		writeError(w, 404, "api.team.privacy.not_found", "team not found")
		return
	}
	h.audit.LogAsync(uid, audit.ActionTeamPrivacy, teamID, map[string]any{"privacy": req.Privacy})
	h.hub.Broadcast(teamScopedEvent("team_updated", teamID, map[string]any{"team": t}))
	writeJSON(w, 200, t)
}

// callerCanAdminTeam is a tiny helper that returns true if the caller is
// system_admin OR team_admin for the given team.
func (h *handlers) callerCanAdminTeam(ctx context.Context, teamID, uid string) bool {
	if uid == "" {
		return false
	}
	if ok, _ := h.auth.HasRole(ctx, uid, "system_admin"); ok {
		return true
	}
	if ok, _ := h.teams.IsTeamAdmin(ctx, teamID, uid); ok {
		return true
	}
	return false
}

// --- Pillar C: Webhooks + Bot updates (3 endpoints) --------------------

// PUT /hooks/incoming/{hookID}
type updateIncomingHookReq struct {
	ChannelID     string `json:"channel_id"`
	DisplayName   string `json:"display_name"`
	Username      string `json:"username"`
	IconURL       string `json:"icon_url"`
	ChannelLocked bool   `json:"channel_locked"`
}

func (h *handlers) updateIncomingWebhook(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "hookID")
	var req updateIncomingHookReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.hook.incoming.update.invalid_body", err.Error())
		return
	}
	hook, err := h.incoming.Update(r.Context(), id, req.ChannelID, req.DisplayName, req.Username, req.IconURL, req.ChannelLocked)
	if err != nil {
		if errors.Is(err, webhooks.ErrHookNotFound) {
			writeError(w, 404, "api.hook.incoming.update.not_found", err.Error())
			return
		}
		writeError(w, 500, "api.hook.incoming.update.app_error", err.Error())
		return
	}
	h.audit.LogAsync(userID(r), audit.ActionHookIncomingUpdate, id, nil)
	writeJSON(w, 200, hook)
}

// PUT /hooks/outgoing/{hookID}
type updateOutgoingHookReq struct {
	TriggerWords []string `json:"trigger_words"`
	CallbackURLs []string `json:"callback_urls"`
	TriggerWhen  int      `json:"trigger_when"`
	DisplayName  string   `json:"display_name"`
	ContentType  string   `json:"content_type"`
}

func (h *handlers) updateOutgoingWebhook(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "hookID")
	var req updateOutgoingHookReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.hook.outgoing.update.invalid_body", err.Error())
		return
	}
	hook, err := h.outgoing.Update(r.Context(), id, req.TriggerWords, req.CallbackURLs, req.TriggerWhen, req.DisplayName, req.ContentType)
	if err != nil {
		if errors.Is(err, webhooks.ErrHookNotFound) {
			writeError(w, 404, "api.hook.outgoing.update.not_found", err.Error())
			return
		}
		writeError(w, 500, "api.hook.outgoing.update.app_error", err.Error())
		return
	}
	h.audit.LogAsync(userID(r), audit.ActionHookOutgoingUpdate, id, nil)
	writeJSON(w, 200, hook)
}

// PUT /bots/{botUserID}
type updateBotReq struct {
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

func (h *handlers) updateBot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "botID")
	var req updateBotReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.bot.update.invalid_body", err.Error())
		return
	}
	b, err := h.bots.Update(r.Context(), id, req.DisplayName, req.Description)
	if err != nil {
		if errors.Is(err, bots.ErrBotNotFound) {
			writeError(w, 404, "api.bot.update.not_found", err.Error())
			return
		}
		writeError(w, 500, "api.bot.update.app_error", err.Error())
		return
	}
	h.audit.LogAsync(userID(r), audit.ActionBotUpdate, id, nil)
	writeJSON(w, 200, b)
}

// --- Pillar D: User admin ops (3 endpoints) ----------------------------

// PUT /users/{userID}/roles — body {"roles": "system_user system_admin"}.
// Admin-only (not self) per Mattermost's contract.
type setRolesReq struct {
	Roles string `json:"roles"`
}

func (h *handlers) setUserRoles(w http.ResponseWriter, r *http.Request) {
	target := chi.URLParam(r, "userID")
	uid := userID(r)
	if uid == target {
		// Mattermost forbids self role assignment to prevent privilege
		// hairpins (an admin demoting themselves into a footgun).
		writeError(w, http.StatusForbidden, "api.user.roles.self", "cannot change own roles")
		return
	}
	var req setRolesReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.user.roles.invalid_body", err.Error())
		return
	}
	if err := h.auth.SetRoles(r.Context(), target, req.Roles); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeError(w, 404, "api.user.roles.not_found", "user not found")
			return
		}
		writeError(w, 500, "api.user.roles.app_error", err.Error())
		return
	}
	h.audit.LogAsync(uid, audit.ActionUserRolesSet, target, map[string]any{"roles": req.Roles})
	h.hub.Broadcast(ws.Event{Event: "user_role_updated", Data: map[string]any{"user_id": target, "roles": req.Roles}, Broadcast: ws.Broadcast{UserID: target}})
	w.WriteHeader(http.StatusNoContent)
}

// PUT /users/{userID}/password — admin path. Distinct from the existing
// PUT /users/me/password (which is the self-rotate path requiring the
// current password). When called as self, falls through to UpdatePassword
// so the contract isn't an admin-only privilege.
type adminSetPasswordReq struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (h *handlers) adminSetPassword(w http.ResponseWriter, r *http.Request) {
	target := chi.URLParam(r, "userID")
	uid := userID(r)
	var req adminSetPasswordReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.user.password.invalid_body", err.Error())
		return
	}
	if req.NewPassword == "" {
		writeError(w, 400, "api.user.password.empty", "new_password required")
		return
	}
	// Self path: must verify current password.
	if target == uid || target == "me" {
		if err := h.auth.UpdatePassword(r.Context(), uid, req.CurrentPassword, req.NewPassword); err != nil {
			if errors.Is(err, auth.ErrInvalidCredentials) {
				writeError(w, 400, "api.user.password.invalid_current", "current password incorrect")
				return
			}
			if errors.Is(err, auth.ErrInvalidPassword) {
				writeError(w, 400, "api.user.password.invalid", err.Error())
				return
			}
			writeError(w, 500, "api.user.password.app_error", err.Error())
			return
		}
		if h.hub != nil {
			h.hub.KickUser(uid)
		}
		h.audit.LogAsync(uid, audit.ActionUserPasswordChg, uid, map[string]any{"sessions_revoked": "all"})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Admin path: caller must hold system_admin.
	if ok, _ := h.auth.HasRole(r.Context(), uid, "system_admin"); !ok {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "admin required")
		return
	}
	if err := h.auth.AdminSetPassword(r.Context(), target, req.NewPassword); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeError(w, 404, "api.user.password.not_found", "user not found")
			return
		}
		if errors.Is(err, auth.ErrInvalidPassword) {
			writeError(w, 400, "api.user.password.invalid", err.Error())
			return
		}
		writeError(w, 500, "api.user.password.app_error", err.Error())
		return
	}
	if h.hub != nil {
		h.hub.KickUser(target)
	}
	h.audit.LogAsync(uid, audit.ActionUserPasswordReset, target, map[string]any{"sessions_revoked": "all"})
	w.WriteHeader(http.StatusNoContent)
}

// PUT /users/{userID}/sessions/device — body {"device_id": "..."}. Stamps
// the device id on the authenticated session row. We don't
// actually do APNS/FCM push fanout yet; this just lets the official mobile
// clients call this on launch without 404'ing.
type setDeviceReq struct {
	DeviceID string `json:"device_id"`
}

func (h *handlers) setDeviceID(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	var req setDeviceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.user.device.invalid_body", err.Error())
		return
	}
	updated, err := h.auth.SetSessionDeviceID(r.Context(), sessionIDFromContext(r.Context()), uid, req.DeviceID)
	if err != nil {
		writeError(w, 500, "api.user.device.app_error", err.Error())
		return
	}
	if !updated {
		// PAT-authenticated requests don't have a sessions row; return 200
		// instead of 404 so the official mobile client doesn't bail.
		writeJSON(w, 200, map[string]any{"status": "OK"})
		return
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// --- Pillar E: Custom status (1 endpoint) ------------------------------

// PUT /users/{userID}/status/custom — body {emoji, text, duration?, expires_at?}.
type setCustomStatusReq struct {
	Emoji     string `json:"emoji"`
	Text      string `json:"text"`
	Duration  string `json:"duration,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

func (h *handlers) setCustomStatus(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	var req setCustomStatusReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.user.custom_status.invalid_body", err.Error())
		return
	}
	cs := userstatus.CustomStatus{
		Emoji: req.Emoji, Text: req.Text,
		Duration: req.Duration, ExpiresAt: req.ExpiresAt,
	}
	if err := h.status.SetCustomStatus(r.Context(), uid, cs); err != nil {
		writeError(w, 500, "api.user.custom_status.app_error", err.Error())
		return
	}
	h.audit.LogAsync(uid, audit.ActionCustomStatusSet, uid, map[string]any{"emoji": cs.Emoji, "text": cs.Text})
	h.hub.Broadcast(subjectUserScopedEvent("custom_status_changed", uid,
		map[string]any{"user_id": uid, "custom_status": cs}))
	writeJSON(w, 200, cs)
}

// DELETE /users/{userID}/status/custom — companion clear endpoint.
func (h *handlers) clearCustomStatus(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	if err := h.status.ClearCustomStatus(r.Context(), uid); err != nil {
		writeError(w, 500, "api.user.custom_status.app_error", err.Error())
		return
	}
	h.audit.LogAsync(uid, audit.ActionCustomStatusClear, uid, nil)
	h.hub.Broadcast(subjectUserScopedEvent("custom_status_changed", uid,
		map[string]any{"user_id": uid, "custom_status": userstatus.CustomStatus{}}))
	w.WriteHeader(http.StatusNoContent)
}

// --- Pillar F: Set unread (1 endpoint) ---------------------------------

// POST /users/{userID}/posts/{postID}/set_unread — rewinds last_viewed_at on
// the channel so the given post becomes the first unread row.
func (h *handlers) setPostUnread(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	postID := chi.URLParam(r, "postID")
	post, err := h.posts.Get(r.Context(), postID)
	if err != nil || post == nil {
		writeError(w, 404, "api.post.set_unread.not_found", "post not found")
		return
	}
	// Caller must be a member of the channel — set_unread is a member-only
	// operation; non-members shouldn't even be able to probe channel posts.
	isMember, _ := h.channels.IsMember(r.Context(), post.ChannelID, uid)
	if !isMember {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "not a channel member")
		return
	}
	viewed, msgCount, mentionCount, err := h.channels.MarkUnreadFromPost(r.Context(), post.ChannelID, uid, post.CreateAt)
	if err != nil {
		writeError(w, 500, "api.post.set_unread.app_error", err.Error())
		return
	}
	h.audit.LogAsync(uid, audit.ActionPostSetUnread, postID, map[string]any{
		"channel_id": post.ChannelID, "boundary": viewed,
	})
	h.hub.Broadcast(ws.Event{
		Event: "channel_unread_updated",
		Data: map[string]any{
			"channel_id":     post.ChannelID,
			"user_id":        uid,
			"last_viewed_at": viewed,
			"msg_count":      msgCount,
			"mention_count":  mentionCount,
		},
		Broadcast: ws.Broadcast{UserID: uid},
	})
	writeJSON(w, 200, map[string]any{
		"channel_id":     post.ChannelID,
		"user_id":        uid,
		"last_viewed_at": viewed,
		"msg_count":      msgCount,
		"mention_count":  mentionCount,
	})
}

// =====================================================================
// Phase 25 — Mattermost API v4 compatibility wave 5.
//
// Picked from the back of the audit's missing list to stay clear of Phase
// 23/24 territory: member-roles + member-notify_props peer-write, session
// revocation HTTP fallbacks, typing HTTP fallback, thread set_unread,
// promote/demote, plus a couple of stub aliases (reminder URL alias,
// recent-custom-status delete) so the official client doesn't 404 on
// boot.
// =====================================================================

// --- Pillar A: Channel + team member roles + notify_props (5 endpoints) ---

// rolesPayload covers BOTH the `roles` body shape (Mattermost classic) and
// the `scheme_user/scheme_admin` body shape (the "schemeRoles" alias). The
// handler maps the latter back into the role-token list before persisting
// so a single service call covers both.
type rolesPayload struct {
	Roles       string `json:"roles"`
	SchemeUser  bool   `json:"scheme_user"`
	SchemeAdmin bool   `json:"scheme_admin"`
}

// derive resolves the canonical role string from whichever shape the
// client used. If `roles` was set explicitly, it wins. Otherwise we
// translate the scheme_* booleans into the conventional channel/team
// role tokens.
func (p rolesPayload) derive(scope string) string {
	if strings.TrimSpace(p.Roles) != "" {
		return p.Roles
	}
	parts := []string{}
	if p.SchemeUser {
		parts = append(parts, scope+"_user")
	}
	if p.SchemeAdmin {
		parts = append(parts, scope+"_admin")
	}
	return strings.Join(parts, " ")
}

// callerCanAdminChannel returns true when the caller holds
// channel_admin on the channel OR system_admin globally. Used by the
// channel-member-roles + per-member-notify_props handlers so a channel
// owner can manage their own room without needing global admin.
func (h *handlers) callerCanAdminChannel(ctx context.Context, channelID, uid string) bool {
	if ok, _ := h.auth.HasRole(ctx, uid, "system_admin"); ok {
		return true
	}
	mem, err := h.channels.GetMember(ctx, channelID, uid)
	if err != nil || mem == nil {
		return false
	}
	for _, r := range strings.Fields(mem.Roles) {
		if r == "channel_admin" {
			return true
		}
	}
	return false
}

// PUT /channels/{channelID}/members/{userID}/roles  (and /schemeRoles alias)
func (h *handlers) setChannelMemberRoles(w http.ResponseWriter, r *http.Request) {
	if h.denyGuestMutation(w, r, "api.channel.member.roles.guest_forbidden") {
		return
	}
	channelID := chi.URLParam(r, "channelID")
	target := chi.URLParam(r, "userID")
	caller := userID(r)
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return
	}
	if !h.callerCanAdminChannel(r.Context(), channelID, caller) {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "channel admin required")
		return
	}
	var req rolesPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.channel.member.roles.invalid_body", err.Error())
		return
	}
	roles := req.derive("channel")
	if roles == "" {
		writeError(w, 400, "api.channel.member.roles.empty", "no roles supplied")
		return
	}
	ok, err := h.channels.SetMemberRoles(r.Context(), channelID, target, roles)
	if err != nil {
		writeError(w, 500, "api.channel.member.roles.app_error", err.Error())
		return
	}
	if !ok {
		writeError(w, 404, "api.channel.member.roles.not_found", "membership not found")
		return
	}
	h.audit.LogAsync(caller, audit.ActionChannelMemberRoles, target,
		map[string]any{"channel_id": channelID, "roles": roles})
	h.hub.Broadcast(channelScopedEvent("channel_member_updated", channelID,
		map[string]any{"channel_id": channelID, "user_id": target, "roles": roles}))
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// PUT /channels/{channelID}/members/{userID}/notify_props
//
// Distinct from /members/me/notify_props (self-write, member-only). This
// peer-write variant lets channel admins push a notify_props mute on
// behalf of a teammate — handy for "force everyone to mention-only" when
// a noisy automation channel gets out of hand.
func (h *handlers) setChannelMemberNotifyProps(w http.ResponseWriter, r *http.Request) {
	if chi.URLParam(r, "userID") != userID(r) && h.denyGuestMutation(w, r, "api.channel.member.notify.guest_forbidden") {
		return
	}
	channelID := chi.URLParam(r, "channelID")
	target := chi.URLParam(r, "userID")
	caller := userID(r)
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return
	}
	// Self-write goes through the existing /members/me path; here we still
	// allow it for symmetry but require admin for peer-writes so a regular
	// user can't tamper with someone else's mute settings.
	if caller != target && !h.callerCanAdminChannel(r.Context(), channelID, caller) {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "channel admin required")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, 400, "api.channel.member.notify_props.invalid_body", err.Error())
		return
	}
	// Membership precheck so a 404 surfaces cleanly when the target isn't
	// in the channel (rather than a silent 0-row UPDATE).
	mem, err := h.channels.GetMember(r.Context(), channelID, target)
	if err != nil || mem == nil {
		writeError(w, 404, "api.channel.member.notify_props.not_found", "membership not found")
		return
	}
	if err := h.channels.SetNotifyProps(r.Context(), channelID, target, body); err != nil {
		writeError(w, 500, "api.channel.member.notify_props.save", err.Error())
		return
	}
	props, err := h.channels.GetNotifyProps(r.Context(), channelID, target)
	if err != nil {
		writeError(w, 500, "api.channel.member.notify_props.app_error", err.Error())
		return
	}
	h.audit.LogAsync(caller, audit.ActionChannelMemberNotify, target,
		map[string]any{"channel_id": channelID})
	h.hub.Broadcast(ws.Event{
		Event:     "channel_member_notify_props_updated",
		Data:      map[string]any{"channel_id": channelID, "user_id": target, "notify_props": props},
		Broadcast: ws.Broadcast{UserID: target},
	})
	writeJSON(w, 200, props)
}

// PUT /teams/{teamID}/members/{userID}/roles  (and /schemeRoles alias)
func (h *handlers) setTeamMemberRoles(w http.ResponseWriter, r *http.Request) {
	if h.denyGuestMutation(w, r, "api.team.member.roles.guest_forbidden") {
		return
	}
	teamID := chi.URLParam(r, "teamID")
	target := chi.URLParam(r, "userID")
	caller := userID(r)
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return
	}
	if !h.callerCanAdminTeam(r.Context(), teamID, caller) {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "team admin required")
		return
	}
	var req rolesPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.team.member.roles.invalid_body", err.Error())
		return
	}
	roles := req.derive("team")
	if roles == "" {
		writeError(w, 400, "api.team.member.roles.empty", "no roles supplied")
		return
	}
	ok, err := h.teams.SetMemberRoles(r.Context(), teamID, target, roles)
	if err != nil {
		writeError(w, 500, "api.team.member.roles.app_error", err.Error())
		return
	}
	if !ok {
		writeError(w, 404, "api.team.member.roles.not_found", "membership not found")
		return
	}
	h.audit.LogAsync(caller, audit.ActionTeamMemberRoles, target,
		map[string]any{"team_id": teamID, "roles": roles})
	h.hub.Broadcast(teamScopedEvent("team_member_updated", teamID,
		map[string]any{"team_id": teamID, "user_id": target, "roles": roles}))
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// --- Pillar B: Session revocation HTTP fallbacks (3 endpoints) ----------

// POST /users/{userID}/sessions/revoke — body {"session_id": "..."}.
// Peer-revoke that an admin (or the session's owner) can use without
// hitting the WS-only flow. Same backing service call as the existing
// `DELETE /users/me/sessions/{id}` route, with a wider auth gate.
type revokeSessionReq struct {
	SessionID string `json:"session_id"`
}

func (h *handlers) revokeUserSession(w http.ResponseWriter, r *http.Request) {
	target, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	var req revokeSessionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.session.revoke.invalid_body", err.Error())
		return
	}
	if strings.TrimSpace(req.SessionID) == "" {
		writeError(w, 400, "api.session.revoke.missing_id", "session_id required")
		return
	}
	removed, err := h.auth.RevokeSession(r.Context(), req.SessionID, target)
	if err != nil {
		writeError(w, 500, "api.session.revoke.app_error", err.Error())
		return
	}
	if !removed {
		writeError(w, 404, "api.session.revoke.not_found", "session not found")
		return
	}
	// Best-effort kick; missing sockets are fine because the session row
	// is gone — the next /users/me call will 401.
	h.hub.KickUser(target)
	h.audit.LogAsync(userID(r), audit.ActionSessionRevoke, target, map[string]any{"session_id": req.SessionID})
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// POST /users/{userID}/sessions/revoke/all — drops every session row for
// one user and closes their open sockets. Self-or-admin gated.
func (h *handlers) revokeAllUserSessions(w http.ResponseWriter, r *http.Request) {
	target, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	count, err := h.auth.RevokeAllForUser(r.Context(), target)
	if err != nil {
		writeError(w, 500, "api.session.revoke_all.app_error", err.Error())
		return
	}
	h.hub.KickUser(target)
	h.audit.LogAsync(userID(r), audit.ActionSessionRevokeAll, target, map[string]any{"count": count})
	writeJSON(w, 200, map[string]any{"status": "OK", "count": count})
}

// POST /users/sessions/revoke/all — admin-only "log everyone out". Used
// by the official admin tool for emergency token rotation. Returns the
// total session count that was nuked.
func (h *handlers) revokeAllSessionsGlobal(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if ok, _ := h.auth.HasRole(r.Context(), caller, "system_admin"); !ok {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "system admin required")
		return
	}
	// Snapshot user ids first so we can fan out kicks AFTER the rows are
	// deleted (otherwise a fast reconnect could re-create the session
	// before we delete it).
	users, err := h.auth.ListAllUserIDsWithSessions(r.Context())
	if err != nil {
		writeError(w, 500, "api.session.revoke_all_global.app_error", err.Error())
		return
	}
	count, err := h.auth.RevokeAllSessionsGlobal(r.Context())
	if err != nil {
		writeError(w, 500, "api.session.revoke_all_global.app_error", err.Error())
		return
	}
	for _, uid := range users {
		h.hub.KickUser(uid)
	}
	h.audit.LogAsync(caller, audit.ActionSessionRevokeGlobal, "", map[string]any{"count": count, "users": len(users)})
	writeJSON(w, 200, map[string]any{"status": "OK", "count": count})
}

// --- Pillar C: Typing HTTP fallback (1 endpoint) ----------------------

// POST /users/{userID}/typing — body {"channel_id": "...", "parent_id"?}.
// Mirrors the existing `user_typing` WS action so headless clients (or
// proxies that strip WS) can still publish typing indicators. We re-check
// channel membership here because the WS path does too — non-members
// shouldn't be able to leak presence into channels they aren't in.
type typingReq struct {
	ChannelID string `json:"channel_id"`
	ParentID  string `json:"parent_id,omitempty"`
}

func (h *handlers) postUserTyping(w http.ResponseWriter, r *http.Request) {
	target, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	var req typingReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.user.typing.invalid_body", err.Error())
		return
	}
	if strings.TrimSpace(req.ChannelID) == "" {
		writeError(w, 400, "api.user.typing.missing_channel", "channel_id required")
		return
	}
	isMember, err := h.channels.IsMember(r.Context(), req.ChannelID, target)
	if err != nil {
		writeError(w, 500, "api.user.typing.member_check", err.Error())
		return
	}
	if !isMember {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "not a channel member")
		return
	}
	h.hub.Broadcast(ws.Event{
		Event: "typing",
		Data: map[string]any{
			"user_id":    target,
			"channel_id": req.ChannelID,
			"parent_id":  req.ParentID,
		},
		Broadcast: ws.Broadcast{ChannelID: req.ChannelID, OmitUsers: []string{target}},
	})
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// --- Pillar D: Thread set_unread (1 endpoint) -------------------------

// POST /users/{userID}/teams/{teamID}/threads/{rootID}/set_unread/{postID}
//
// Rewinds the thread's last_viewed_at to (post.create_at - 1) so the given
// reply becomes the first unread row. Caller must be the target user (or
// admin) AND a member of the channel containing the thread.
func (h *handlers) setThreadUnread(w http.ResponseWriter, r *http.Request) {
	target, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	teamID := chi.URLParam(r, "teamID")
	rootID := chi.URLParam(r, "rootID")
	postID := chi.URLParam(r, "postID")
	post, err := h.posts.Get(r.Context(), postID)
	if err != nil || post == nil {
		writeError(w, 404, "api.thread.set_unread.not_found", "post not found")
		return
	}
	// Anchor post must actually live under the thread the URL claims —
	// otherwise a malicious caller could rewind any thread to any unrelated
	// post's timestamp.
	if post.RootID != rootID && post.ID != rootID {
		writeError(w, 400, "api.thread.set_unread.mismatch", "post is not part of the thread")
		return
	}
	isMember, _ := h.channels.IsMember(r.Context(), post.ChannelID, target)
	if !isMember {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "not a channel member")
		return
	}
	boundary, err := h.threads.MarkUnreadFromPost(r.Context(), target, teamID, rootID, post.CreateAt)
	if err != nil {
		writeError(w, 500, "api.thread.set_unread.app_error", err.Error())
		return
	}
	h.audit.LogAsync(userID(r), audit.ActionThreadSetUnread, rootID,
		map[string]any{"team_id": teamID, "post_id": postID, "boundary": boundary})
	h.hub.Broadcast(ws.Event{
		Event: "thread_read_changed",
		Data: map[string]any{
			"user_id":        target,
			"team_id":        teamID,
			"thread_id":      rootID,
			"last_viewed_at": boundary,
		},
		Broadcast: ws.Broadcast{UserID: target},
	})
	writeJSON(w, 200, map[string]any{
		"user_id":        target,
		"team_id":        teamID,
		"thread_id":      rootID,
		"last_viewed_at": boundary,
	})
}

// --- Pillar E: Promote / Demote (2 endpoints) -------------------------

// POST /users/{userID}/promote — admin promotes a guest to a regular
// system_user. We don't model guests as a first-class concept anywhere
// (no permission check ever inspects system_guest), so this is a contract-
// shape implementation: the role string round-trips correctly so an
// official admin tool that flips back-and-forth gets the same answer it
// sent. Idempotent.
func (h *handlers) promoteUser(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if ok, _ := h.auth.HasRole(r.Context(), caller, "system_admin"); !ok {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "system admin required")
		return
	}
	target := chi.URLParam(r, "userID")
	// Existence check first — PromoteToUser only surfaces a generic db error
	// for a missing user, and we want a 404 for the official-client's error
	// envelope rather than a 500.
	if u, err := h.auth.UserByID(r.Context(), target); err != nil || u == nil {
		writeError(w, 404, "api.user.promote.not_found", "user not found")
		return
	}
	if err := h.auth.PromoteToUser(r.Context(), target); err != nil {
		writeError(w, 500, "api.user.promote.app_error", err.Error())
		return
	}
	h.audit.LogAsync(caller, audit.ActionUserPromote, target, nil)
	h.hub.Broadcast(ws.Event{
		Event:     "user_role_updated",
		Data:      map[string]any{"user_id": target},
		Broadcast: ws.Broadcast{UserID: target},
	})
	w.WriteHeader(http.StatusNoContent)
}

// POST /users/{userID}/demote — admin demotes a regular user to a guest.
func (h *handlers) demoteUser(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if ok, _ := h.auth.HasRole(r.Context(), caller, "system_admin"); !ok {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "system admin required")
		return
	}
	target := chi.URLParam(r, "userID")
	if target == caller {
		// Demoting yourself out of the only admin's regular-user role would
		// leave the system without an actor that can re-promote. Mattermost
		// rejects self-demotion for the same reason.
		writeError(w, 400, "api.user.demote.self", "cannot demote yourself")
		return
	}
	if u, err := h.auth.UserByID(r.Context(), target); err != nil || u == nil {
		writeError(w, 404, "api.user.demote.not_found", "user not found")
		return
	}
	if err := h.auth.DemoteToGuest(r.Context(), target); err != nil {
		writeError(w, 500, "api.user.demote.app_error", err.Error())
		return
	}
	h.audit.LogAsync(caller, audit.ActionUserDemote, target, nil)
	h.hub.Broadcast(ws.Event{
		Event:     "user_role_updated",
		Data:      map[string]any{"user_id": target},
		Broadcast: ws.Broadcast{UserID: target},
	})
	w.WriteHeader(http.StatusNoContent)
}

// --- Pillar F: Reminder alias + custom_status recent stub (2 endpoints) -

// POST /users/{userID}/posts/{postID}/reminder — official-shape alias for
// the existing `POST /posts/{postID}/remind_me`. The body shape and
// semantics are identical; we just remap the URL params and delegate.
func (h *handlers) createUserPostReminder(w http.ResponseWriter, r *http.Request) {
	target, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
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
	post, err := h.posts.Get(r.Context(), postID)
	if err != nil || post == nil {
		writeError(w, 404, "api.reminder.create.not_found", "post not found")
		return
	}
	isMember, _ := h.channels.IsMember(r.Context(), post.ChannelID, target)
	if !isMember {
		writeError(w, http.StatusForbidden, "api.reminder.create.forbidden", "not a channel member")
		return
	}
	rem, err := h.reminders.Create(r.Context(), target, postID, req.RemindAt)
	if err != nil {
		writeError(w, 500, "api.reminder.create.app_error", err.Error())
		return
	}
	h.hub.Broadcast(ws.Event{
		Event:     "reminder_created",
		Data:      map[string]any{"id": rem.ID, "post_id": postID, "remind_at": rem.RemindAt},
		Broadcast: ws.Broadcast{UserID: target},
	})
	writeJSON(w, 201, rem)
}

// POST /users/{userID}/status/custom/recent/delete — body {emoji, text}.
//
// Mattermost tracks a per-user "recent custom statuses" list (the picker's
// dropdown). We don't persist that list yet, so this endpoint accepts the
// payload, stamps an audit row, and returns 200. It exists purely so the
// official client doesn't 404 when the user removes a recent — when we
// eventually persist the list it'll grow a real backing store; the URL
// shape stays unchanged.
type recentCustomStatusReq struct {
	Emoji string `json:"emoji"`
	Text  string `json:"text"`
}

func (h *handlers) deleteRecentCustomStatus(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	var req recentCustomStatusReq
	// Body is optional — an empty body is a valid "clear nothing" no-op.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "api.user.custom_status.recent.invalid_body", err.Error())
			return
		}
	}
	h.audit.LogAsync(uid, audit.ActionCustomStatusClear, uid,
		map[string]any{"recent_delete_emoji": req.Emoji, "recent_delete_text": req.Text})
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// ============================================================================
// Phase 26 — Mattermost API v4 compatibility wave 6
// 14 endpoints across 6 pillars (PAT operator surface, team admin restore +
// invite shape, posts move/restore, channel/group bulk hydrate, user admin
// convert/reset, outgoing webhook regen). Zero schema changes; everything
// here builds on tables we already have.
// ============================================================================

// ---- Pillar A — PAT operator surface (4 endpoints) ----

type tokenIDReq struct {
	TokenID string `json:"token_id"`
}

// POST /users/tokens/disable  (admin) — alias for revoke. Mattermost ships
// both action names because some admin tools send disable, others send
// revoke. We treat them as one operation since our PAT row only has
// revoked_at. Body: {token_id}.
func (h *handlers) disableUserToken(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return
	}
	var req tokenIDReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TokenID == "" {
		writeError(w, 400, "api.user.token.disable.invalid_body", "token_id required")
		return
	}
	if err := h.bots.RevokeToken(r.Context(), req.TokenID); err != nil {
		writeError(w, 500, "api.user.token.disable.save", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionTokenDisable, req.TokenID, nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// POST /users/tokens/enable (admin) — un-revokes a token by zeroing
// revoked_at. Body: {token_id}.
func (h *handlers) enableUserToken(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return
	}
	var req tokenIDReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TokenID == "" {
		writeError(w, 400, "api.user.token.enable.invalid_body", "token_id required")
		return
	}
	if err := h.bots.EnableToken(r.Context(), req.TokenID); err != nil {
		if errors.Is(err, bots.ErrTokenInvalid) {
			writeError(w, 404, "api.user.token.not_found", "token not found")
			return
		}
		writeError(w, 500, "api.user.token.enable.save", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionTokenEnable, req.TokenID, nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// POST /users/tokens/revoke (admin) — bulk-form alias of the
// /tokens/{tokenID}/revoke route. Body: {token_id}.
func (h *handlers) revokeUserTokenByBody(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return
	}
	var req tokenIDReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TokenID == "" {
		writeError(w, 400, "api.user.token.revoke.invalid_body", "token_id required")
		return
	}
	if err := h.bots.RevokeToken(r.Context(), req.TokenID); err != nil {
		writeError(w, 500, "api.user.token.revoke.save", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionTokenRevoke, req.TokenID, nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

type searchTokensReq struct {
	Term string `json:"term"`
}

// POST /users/tokens/search (admin) — list tokens whose description, owner,
// or id matches the term. Empty term returns the most recent 100 rows.
func (h *handlers) searchUserTokens(w http.ResponseWriter, r *http.Request) {
	var req searchTokensReq
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "api.user.token.search.invalid_body", err.Error())
			return
		}
	}
	out, err := h.bots.SearchTokens(r.Context(), req.Term, 100)
	if err != nil {
		writeError(w, 500, "api.user.token.search.query", err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// ---- Pillar B — Team admin restore + invite shape (3 endpoints) ----

// POST /teams/{teamID}/restore (admin) — un-archive a soft-deleted team.
// Mirrors the channel restore path from Phase 16. Returns the team row
// after restore. Idempotent: restoring an active team is a 200 no-op.
func (h *handlers) restoreTeam(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return
	}
	teamID := chi.URLParam(r, "teamID")
	if teamID == "" {
		writeError(w, 400, "api.team.restore.missing_id", "team id required")
		return
	}
	if _, err := h.teams.Restore(r.Context(), teamID); err != nil {
		writeError(w, 500, "api.team.restore.save", err.Error())
		return
	}
	t, err := h.teams.Get(r.Context(), teamID)
	if err != nil || t == nil {
		writeError(w, 404, "api.team.not_found", "team not found")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionTeamRestore, teamID, nil)
	}
	if h.hub != nil {
		h.hub.Broadcast(teamScopedEvent("team_restored", teamID, map[string]any{"team_id": teamID}))
	}
	writeJSON(w, 200, t)
}

// POST /teams/{teamID}/regenerate_invite_id — rotates the team's primary
// invite link by creating a fresh long-lived invite_tokens row with no
// max_uses and a 90-day TTL. Returns the new invite id in
// {invite_id} so the official client can drop it into URL share buttons.
// Caller must be team_admin or system_admin.
func (h *handlers) regenerateTeamInviteID(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return
	}
	teamID := chi.URLParam(r, "teamID")
	if teamID == "" {
		writeError(w, 400, "api.team.invite.regen.missing_id", "team id required")
		return
	}
	if !h.callerCanAdminTeam(r.Context(), teamID, caller) {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "team admin required")
		return
	}
	// 90-day TTL, unlimited uses — matches the contract of Mattermost's
	// invite_id which is a long-lived per-team URL token.
	inv, err := h.invites.Create(r.Context(), teamID, caller, 0, secondsDuration(90*24*3600))
	if err != nil {
		writeError(w, 500, "api.team.invite.regen.save", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionTeamInviteRegen, teamID, map[string]any{
			"invite_id":  inv.ID,
			"expires_at": inv.ExpiresAt,
		})
	}
	writeJSON(w, 201, map[string]any{
		"invite_id":  inv.ID,
		"team_id":    teamID,
		"expires_at": inv.ExpiresAt,
	})
}

type inviteEmailReq struct {
	Emails []string `json:"emails"`
}

// POST /teams/{teamID}/invite/email (admin) — accepts an email list for
// invitation. We don't ship server-side email yet, so the endpoint
// records an audit row and returns 200 with an empty result list. The
// shape exists so an admin tool's "invite by email" form doesn't 404;
// when SMTP-based invites land later, this handler grows the actual
// dispatch logic without a URL change.
func (h *handlers) inviteTeamMembersByEmail(w http.ResponseWriter, r *http.Request) {
	if h.denyGuestMutation(w, r, "api.team.invite.guest_forbidden") {
		return
	}
	caller := userID(r)
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return
	}
	teamID := chi.URLParam(r, "teamID")
	if !h.callerCanAdminTeam(r.Context(), teamID, caller) {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "team admin required")
		return
	}
	var req inviteEmailReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.team.invite.email.invalid_body", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionTeamInviteEmail, teamID, map[string]any{
			"emails": req.Emails,
		})
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// ---- Pillar C — Posts move + restore (2 endpoints) ----

type movePostReq struct {
	ChannelID string `json:"channel_id"`
}

// POST /posts/{postID}/move — relocates a thread (root post + all replies)
// to a different channel. Caller must be a channel_admin or system_admin
// for BOTH the source and destination channels — otherwise a leak vector
// where a privileged user in the destination but not the source could
// harvest a thread they shouldn't see.
func (h *handlers) movePost(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return
	}
	postID := chi.URLParam(r, "postID")
	var req movePostReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ChannelID == "" {
		writeError(w, 400, "api.post.move.invalid_body", "channel_id required")
		return
	}
	post, err := h.posts.Get(r.Context(), postID)
	if err != nil || post == nil {
		writeError(w, 404, "api.post.not_found", "post not found")
		return
	}
	// We only move thread roots — moving an arbitrary reply leaves the
	// thread split across channels, which Mattermost forbids.
	if post.RootID != "" {
		writeError(w, 400, "api.post.move.not_root", "only root posts can be moved")
		return
	}
	if !h.callerCanAdminChannel(r.Context(), post.ChannelID, caller) ||
		!h.callerCanAdminChannel(r.Context(), req.ChannelID, caller) {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error",
			"channel admin required on both source and destination")
		return
	}
	moved, err := h.posts.MoveThread(r.Context(), postID, req.ChannelID)
	if err != nil {
		writeError(w, 500, "api.post.move.save", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionPostMove, postID, map[string]any{
			"from_channel_id": post.ChannelID,
			"to_channel_id":   req.ChannelID,
			"row_count":       moved,
		})
	}
	if h.hub != nil {
		h.hub.Broadcast(ws.Event{
			Event: "post_moved",
			Data: map[string]any{
				"post_id":         postID,
				"from_channel_id": post.ChannelID,
				"to_channel_id":   req.ChannelID,
				"row_count":       moved,
			},
			Broadcast: ws.Broadcast{ChannelID: post.ChannelID},
		})
		h.hub.Broadcast(ws.Event{
			Event: "post_moved",
			Data: map[string]any{
				"post_id":         postID,
				"from_channel_id": post.ChannelID,
				"to_channel_id":   req.ChannelID,
				"row_count":       moved,
			},
			Broadcast: ws.Broadcast{ChannelID: req.ChannelID},
		})
	}
	writeJSON(w, 200, map[string]any{
		"post_id":         postID,
		"to_channel_id":   req.ChannelID,
		"from_channel_id": post.ChannelID,
		"moved":           moved,
	})
}

// POST /posts/{postID}/restore/{revisionID} — un-soft-deletes a post.
// We don't version posts, so the {revisionID} URL segment is captured
// but ignored. Admin-only.
func (h *handlers) restorePost(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return
	}
	postID := chi.URLParam(r, "postID")
	revID := chi.URLParam(r, "revID")
	post, err := h.posts.Get(r.Context(), postID)
	if err != nil || post == nil {
		// A still-deleted post returns from Get with delete_at != 0
		// (or, depending on the query, returns an error). Try a raw
		// pgx fallback — but for now treat any miss as 404.
		writeError(w, 404, "api.post.not_found", "post not found")
		return
	}
	if !h.callerCanAdminChannel(r.Context(), post.ChannelID, caller) {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "channel admin required")
		return
	}
	if _, err := h.posts.Restore(r.Context(), postID); err != nil {
		writeError(w, 500, "api.post.restore.save", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionPostRestore, postID, map[string]any{
			"channel_id":  post.ChannelID,
			"revision_id": revID,
		})
	}
	if h.hub != nil {
		h.hub.Broadcast(ws.Event{
			Event: "post_restored",
			Data: map[string]any{
				"post_id":    postID,
				"channel_id": post.ChannelID,
			},
			Broadcast: ws.Broadcast{ChannelID: post.ChannelID},
		})
	}
	writeJSON(w, 200, map[string]any{"status": "OK", "post_id": postID})
}

// ---- Pillar D — Channel + group bulk hydrate (2 endpoints) ----

type channelIDsReq struct {
	IDs []string `json:"ids"`
}

// POST /teams/{teamID}/channels/ids — bulk-hydrates a list of channels by
// id within a single team, gated by visibility (public OR member).
// Body shape varies between Mattermost versions: some send `{ids:[...]}`,
// some send a bare array. We accept both.
func (h *handlers) channelsByIDsInTeam(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return
	}
	teamID := chi.URLParam(r, "teamID")
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeError(w, 400, "api.channel.ids.invalid_body", err.Error())
		return
	}
	var ids []string
	// Try bare array first; on failure, fall back to {ids:[]}.
	if jerr := json.Unmarshal(body, &ids); jerr != nil {
		var wrapped channelIDsReq
		if err := json.Unmarshal(body, &wrapped); err != nil {
			writeError(w, 400, "api.channel.ids.invalid_body", "expected array or {ids:[]}")
			return
		}
		ids = wrapped.IDs
	}
	out, err := h.channels.ChannelsByIDsInTeam(r.Context(), teamID, caller, ids)
	if err != nil {
		writeError(w, 500, "api.channel.ids.query", err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// POST /users/group_channels — returns the user's group ('G') channel
// memberships. Mattermost's official API exposes this as a body-less POST
// (legacy quirk); the body is read-and-discarded for compatibility.
func (h *handlers) listMyGroupChannels(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return
	}
	// Discard any body the official client may send.
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 4*1024))
	}
	out, err := h.channels.ListGroupChannelsForUser(r.Context(), caller)
	if err != nil {
		writeError(w, 500, "api.user.group_channels.query", err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// ---- Pillar E — User admin convert/reset (2 endpoints) ----

type convertToBotReq struct {
	OwnerID     string `json:"owner_id"`
	Description string `json:"description"`
}

// POST /users/{userID}/convert_to_bot (admin) — flips an existing human
// user account into a bot. The user keeps their id and posts; their
// password_hash is blanked and every active session is wiped. Caller
// supplies an owner_id (the human responsible for the bot). Returns the
// new bot row so the admin tool can immediately follow up with token
// creation.
func (h *handlers) convertUserToBot(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return
	}
	target := chi.URLParam(r, "userID")
	if target == "" || target == "me" {
		writeError(w, 400, "api.user.convert_to_bot.invalid_target", "explicit user id required")
		return
	}
	if target == caller {
		writeError(w, 400, "api.user.convert_to_bot.self", "cannot convert self")
		return
	}
	var req convertToBotReq
	// Body is optional; defaults are caller-as-owner, no description.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "api.user.convert_to_bot.invalid_body", err.Error())
			return
		}
	}
	owner := req.OwnerID
	if owner == "" {
		owner = caller
	}
	if err := h.auth.ConvertToBot(r.Context(), target, owner, req.Description); err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			writeError(w, 404, "api.user.not_found", "user not found or already a bot")
			return
		}
		writeError(w, 500, "api.user.convert_to_bot.save", err.Error())
		return
	}
	if h.hub != nil {
		h.hub.KickUser(target)
	}
	bot, err := h.bots.Get(r.Context(), target)
	if err != nil {
		// Conversion succeeded but read-back failed; return a minimal
		// success envelope rather than 500 — the row IS now a bot.
		bot = &bots.Bot{UserID: target, OwnerID: owner, Description: req.Description}
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionUserConvertToBot, target, map[string]any{
			"owner_id":    owner,
			"description": req.Description,
		})
	}
	writeJSON(w, 201, bot)
}

// POST /users/{userID}/reset_failed_attempts (admin) — Mattermost zeroes
// the failed-login counter on the user row. We don't track failed
// attempts (the rate-limiter is per-IP, not per-user), so this is a
// 200 OK stub that records an audit row and returns the success envelope.
// The endpoint exists so an admin tool's "unlock account" button has
// somewhere to POST.
func (h *handlers) resetUserFailedAttempts(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return
	}
	target := chi.URLParam(r, "userID")
	if target == "" {
		writeError(w, 400, "api.user.reset_failed_attempts.invalid_target", "user id required")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionUserResetAttempts, target, nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// ---- Pillar F — Outgoing webhook regen_token (1 endpoint) ----

// POST /hooks/outgoing/{hookID}/regen_token (admin) — rotates the token
// on an outgoing webhook to a fresh UUID and returns the updated row.
// Used to invalidate a leaked secret without rebuilding the hook. The
// new token comes back in the response body once — receivers using the
// old token will be ignored on the next dispatch.
func (h *handlers) regenerateOutgoingWebhookToken(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "missing token")
		return
	}
	hookID := chi.URLParam(r, "hookID")
	if hookID == "" {
		writeError(w, 400, "api.hook.outgoing.regen.missing_id", "hook id required")
		return
	}
	hook, err := h.outgoing.RegenerateToken(r.Context(), hookID)
	if err != nil {
		if errors.Is(err, webhooks.ErrHookNotFound) {
			writeError(w, 404, "api.hook.outgoing.not_found", "hook not found")
			return
		}
		writeError(w, 500, "api.hook.outgoing.regen.save", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionHookOutgoingRegen, hookID, nil)
	}
	writeJSON(w, 200, hook)
}
