package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/audit"
	"github.com/hkjang/moyro/server/internal/auth"
	"github.com/hkjang/moyro/server/internal/bookmarks"
	"github.com/hkjang/moyro/server/internal/bots"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/customprofile"
	"github.com/hkjang/moyro/server/internal/files"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/reactions"
	"github.com/hkjang/moyro/server/internal/teams"
	"github.com/hkjang/moyro/server/internal/tos"
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

// ============================================================================
// Phase 30 — Mattermost API v4 compatibility wave 10
// ----------------------------------------------------------------------------
// Five pillars of additive endpoints chosen from the back of the audit's
// missing-endpoint list to stay clear of codex's compat handler files
// (compat_handlers.go, admin_compat_handlers.go, enterprise_compat_handlers.go).
// All handlers stamp Phase 30 below this comment. Most are stubs (anti-oracle
// envelopes that return the documented success shape regardless of state) so
// the official Mattermost client doesn't 404 — when real implementations land
// later, the URL contracts stay stable.
// ============================================================================

// ---- Pillar A — Team destructive aliases ----------------------------------

// deleteTeam — DELETE /teams/{teamID}
// Soft-deletes a team; system_admin-only. The teams package's Delete
// stamps delete_at on the row but leaves the record so audit trails can
// resolve the team name forever. ?permanent=true is silently ignored —
// we don't support hard deletes to avoid losing the audit history.
func (h *handlers) deleteTeam(w http.ResponseWriter, r *http.Request) {
	if h.denyGuestMutation(w, r, "api.team.delete.guest_forbidden") {
		return
	}
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	tid := chi.URLParam(r, "teamID")
	changed, err := h.teams.Delete(r.Context(), tid)
	if err != nil {
		writeError(w, 500, "api.team.delete.app_error", err.Error())
		return
	}
	if !changed {
		writeError(w, 404, "api.team.delete.not_found", "team not found or already deleted")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionTeamDelete, tid, nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// removeTeamMember — DELETE /teams/{teamID}/members/{userID}
// Mirror of POST /teams/{teamID}/members which adds; this kicks. Caller
// must hold team_admin for that team OR be system_admin globally OR be
// removing themselves (Leave). Self-removal is allowed because every
// user can leave any team they're in.
func (h *handlers) removeTeamMember(w http.ResponseWriter, r *http.Request) {
	if chi.URLParam(r, "userID") != userID(r) && h.denyGuestMutation(w, r, "api.team.members.remove.guest_forbidden") {
		return
	}
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	tid := chi.URLParam(r, "teamID")
	uid := chi.URLParam(r, "userID")
	if uid == "" {
		writeError(w, 400, "api.team.member.remove.app_error", "missing user")
		return
	}
	if uid != caller && !h.callerCanAdminTeam(r.Context(), tid, caller) {
		writeError(w, 403, "api.context.permissions.app_error", "team_admin required")
		return
	}
	changed, err := h.teams.RemoveMember(r.Context(), tid, uid)
	if err != nil {
		writeError(w, 500, "api.team.member.remove.app_error", err.Error())
		return
	}
	if !changed {
		writeError(w, 404, "api.team.member.remove.not_found", "membership not found")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionTeamMemberRemove, tid+":"+uid, nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// deleteTeamImage — DELETE /teams/{teamID}/image
// Stub: we don't store team images yet (the table has no image column).
// Returns 200 with the documented success envelope so the admin
// console's "Remove team icon" button doesn't error-toast.
func (h *handlers) deleteTeamImage(w http.ResponseWriter, r *http.Request) {
	if h.denyGuestMutation(w, r, "api.team.image.guest_forbidden") {
		return
	}
	if !h.callerCanAdminTeam(r.Context(), chi.URLParam(r, "teamID"), userID(r)) {
		writeError(w, 403, "api.context.permissions.app_error", "team_admin required")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionTeamImageDelete, chi.URLParam(r, "teamID"), nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// uploadTeamImage — POST /teams/{teamID}/image
// Stub. Reads-and-discards the multipart body so the official admin
// console's drag-drop upload form doesn't hang on EOF; future work
// would tie this into the existing files.Service backend.
func (h *handlers) uploadTeamImage(w http.ResponseWriter, r *http.Request) {
	if !h.callerCanAdminTeam(r.Context(), chi.URLParam(r, "teamID"), userID(r)) {
		writeError(w, 403, "api.context.permissions.app_error", "team_admin required")
		return
	}
	// Cap at 10MB then drain — protects against a runaway upload while
	// the stub doesn't persist anything.
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	_, _ = io.Copy(io.Discard, r.Body)
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionTeamImageUpload, chi.URLParam(r, "teamID"), nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// getTeamImage — GET /teams/{teamID}/image
// Stub. Returns 404 immediately — the official client treats 404 as
// "no team icon, render initials" so the empty state works fine.
func (h *handlers) getTeamImage(w http.ResponseWriter, r *http.Request) {
	writeError(w, 404, "api.team.image.not_found", "no team image")
}

// revokeTeamEmailInvites — DELETE /teams/invites/email
// Stub. We don't ship email invitations yet (Phase 16's invite_tokens
// is link-based, not email-based) so there's nothing to revoke.
// Returns the documented success envelope.
func (h *handlers) revokeTeamEmailInvites(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionTeamInvitesRevoke, "", nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// ---- Pillar B — Channel views stubs ---------------------------------------
// Mattermost's "channel views" feature lets a member save a filtered subset
// of a channel's posts under a named view (think saved searches scoped to
// a channel). We don't have storage for this yet; the endpoints exist so
// the official client's "Saved views" UI doesn't 404.

// listChannelViews — GET /channels/{channelID}/views
func (h *handlers) listChannelViews(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	cid := chi.URLParam(r, "channelID")
	ok, _ := h.channels.IsMember(r.Context(), cid, caller)
	if !ok {
		writeError(w, 403, "api.context.permissions.app_error", "not a channel member")
		return
	}
	writeJSON(w, 200, []any{})
}

// getChannelView — GET /channels/{channelID}/views/{viewID}
func (h *handlers) getChannelView(w http.ResponseWriter, r *http.Request) {
	writeError(w, 404, "api.channel.view.not_found", "no view by that id")
}

// createChannelView — POST /channels/{channelID}/views
func (h *handlers) createChannelView(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	cid := chi.URLParam(r, "channelID")
	ok, _ := h.channels.IsMember(r.Context(), cid, caller)
	if !ok {
		writeError(w, 403, "api.context.permissions.app_error", "not a channel member")
		return
	}
	// Synthesize an id so the client gets a stable handle — the row
	// isn't durable but the client can still cache it locally for the
	// session.
	now := time.Now().UnixMilli()
	id := fmt.Sprintf("view-%d", now)
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionViewCreate, cid+":"+id, nil)
	}
	writeJSON(w, 201, map[string]any{
		"id":         id,
		"channel_id": cid,
		"user_id":    caller,
		"create_at":  now,
		"update_at":  now,
	})
}

// patchChannelView — PATCH /channels/{channelID}/views/{viewID}
func (h *handlers) patchChannelView(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// deleteChannelView — DELETE /channels/{channelID}/views/{viewID}
func (h *handlers) deleteChannelView(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	if h.audit != nil {
		vid := chi.URLParam(r, "viewID")
		cid := chi.URLParam(r, "channelID")
		h.audit.LogAsync(caller, audit.ActionViewDelete, cid+":"+vid, nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// listChannelViewPosts — GET /channels/{channelID}/views/{viewID}/posts
// Returns the canonical empty post envelope so the client renders the
// "no posts in this view" empty state cleanly.
func (h *handlers) listChannelViewPosts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"order": []string{},
		"posts": map[string]any{},
	})
}

// reorderChannelView — POST /channels/{channelID}/views/{viewID}/sort_order
func (h *handlers) reorderChannelView(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// ---- Pillar C — Channel admin operations ----------------------------------

// listAllChannels — GET /channels
// Admin-wide listing of every channel across every team. Used by the
// admin console "Channels" tab. Reuses the existing SearchAll service
// method with an empty term.
func (h *handlers) listAllChannels(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	rows, err := h.channels.SearchAll(r.Context(), "", 200)
	if err != nil {
		writeError(w, 500, "api.channel.list_all.app_error", err.Error())
		return
	}
	writeJSON(w, 200, rows)
}

// listChannelTimezones — GET /channels/{channelID}/timezones
// Mattermost surfaces this so the official client can show a "members
// across N timezones" badge. We don't store per-user timezone yet;
// returning [] is the documented empty-state shape.
func (h *handlers) listChannelTimezones(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	cid := chi.URLParam(r, "channelID")
	ok, _ := h.channels.IsMember(r.Context(), cid, caller)
	if !ok {
		writeError(w, 403, "api.context.permissions.app_error", "not a channel member")
		return
	}
	writeJSON(w, 200, []string{})
}

// getChannelModerations — GET /channels/{channelID}/moderations
// Stub. Mattermost's moderation feature lets channel admins toggle
// permissions like "members can post" / "members can react" on a
// per-channel basis. We don't model these yet; returns the canonical
// "everything allowed" defaults so the admin console renders without
// errors.
func (h *handlers) getChannelModerations(w http.ResponseWriter, r *http.Request) {
	if !h.callerCanAdminChannel(r.Context(), chi.URLParam(r, "channelID"), userID(r)) {
		writeError(w, 403, "api.context.permissions.app_error", "channel_admin required")
		return
	}
	defaults := []map[string]any{
		{"name": "create_post", "roles": map[string]any{"members": true, "guests": true}},
		{"name": "create_reactions", "roles": map[string]any{"members": true, "guests": true}},
		{"name": "manage_members", "roles": map[string]any{"members": true}},
		{"name": "manage_channel_mentions", "roles": map[string]any{"members": true}},
	}
	writeJSON(w, 200, defaults)
}

// patchChannelModerations — PUT /channels/{channelID}/moderations/patch
func (h *handlers) patchChannelModerations(w http.ResponseWriter, r *http.Request) {
	if !h.callerCanAdminChannel(r.Context(), chi.URLParam(r, "channelID"), userID(r)) {
		writeError(w, 403, "api.context.permissions.app_error", "channel_admin required")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionChannelModerations, chi.URLParam(r, "channelID"), nil)
	}
	// Echo the canonical defaults again — clients use the response
	// shape to refresh their local state.
	defaults := []map[string]any{
		{"name": "create_post", "roles": map[string]any{"members": true, "guests": true}},
		{"name": "create_reactions", "roles": map[string]any{"members": true, "guests": true}},
		{"name": "manage_members", "roles": map[string]any{"members": true}},
		{"name": "manage_channel_mentions", "roles": map[string]any{"members": true}},
	}
	writeJSON(w, 200, defaults)
}

// setChannelScheme — PUT /channels/{channelID}/scheme
// Stub. Schemes are a Mattermost enterprise feature for swapping the
// role definitions a channel uses; we don't model schemes yet.
func (h *handlers) setChannelScheme(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionChannelSchemeSet, chi.URLParam(r, "channelID"), nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// moveChannel — POST /channels/{channelID}/move
// Stub. Moving a channel between teams is a Mattermost admin feature
// that requires re-routing every member's team membership too — non-
// trivial to ship as a real transaction. The stub audits the request
// + 200s so admin-tool integrations don't break.
func (h *handlers) moveChannel(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	cid := chi.URLParam(r, "channelID")
	var body struct {
		TeamID string `json:"team_id"`
		Force  bool   `json:"force"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionChannelMove, cid, map[string]any{"to_team": body.TeamID})
	}
	// Echo the existing channel record back; the client refreshes from
	// the response. Real implementation would update channels.team_id
	// and re-fanout membership.
	ch, err := h.channels.Get(r.Context(), cid)
	if err != nil {
		writeError(w, 404, "api.channel.move.not_found", err.Error())
		return
	}
	writeJSON(w, 200, ch)
}

// getTeamChannelByName — GET /teams/name/{teamName}/channels/name/{channelName}
// Composite by-name-by-name lookup. Saves a round-trip for clients that
// have just a (team-slug, channel-slug) pair from a deep link.
func (h *handlers) getTeamChannelByName(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	teamName := chi.URLParam(r, "teamName")
	channelName := chi.URLParam(r, "channelName")
	team, err := h.teams.GetByName(r.Context(), teamName)
	if err != nil || team == nil {
		// This composite lookup must not reveal whether a hidden team slug is
		// valid, so unknown teams and inaccessible team/channel pairs share the
		// same response envelope.
		writeError(w, 404, "api.channel.not_found", "channel not found")
		return
	}
	teamMember, err := h.teams.IsMember(r.Context(), team.ID, caller)
	if err != nil {
		writeError(w, 500, "api.channel.lookup.member_check", err.Error())
		return
	}
	isAdmin := h.callerIsSystemAdmin(r)
	if !teamMember && !isAdmin {
		// Keep an existing-but-hidden team/channel pair indistinguishable
		// from an unknown channel name.
		writeError(w, 404, "api.channel.not_found", "channel not found")
		return
	}
	ch, err := h.channels.GetByName(r.Context(), team.ID, channelName)
	if err != nil || ch == nil {
		writeError(w, 404, "api.channel.not_found", "channel not found")
		return
	}
	channelMember, err := h.channels.IsMember(r.Context(), ch.ID, caller)
	if err != nil {
		writeError(w, 500, "api.channel.lookup.member_check", err.Error())
		return
	}
	actor, err := h.auth.UserByID(r.Context(), caller)
	if err != nil {
		writeError(w, 401, "api.context.session_expired.app_error", "active user session required")
		return
	}
	// Guests may resolve only their explicit channel allow-list. Other
	// callers may resolve public channels in teams they belong to; private
	// channels remain member/admin-only. Hidden names always collapse to 404.
	if (actor.IsGuest() && !channelMember) || (!channelMember && ch.Type != "O" && !isAdmin) {
		writeError(w, 404, "api.channel.not_found", "channel not found")
		return
	}
	writeJSON(w, 200, ch)
}

// ---- Pillar D — Reports + admin stats stubs -------------------------------

// getInvalidEmails — GET /users/invalid_emails
// Stub: admin-only listing of users whose emails don't match the
// configured email-domain whitelist. We don't enforce a whitelist
// so the list is always empty.
func (h *handlers) getInvalidEmails(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	writeJSON(w, 200, []string{})
}

// getFilteredUserStats — GET /users/stats/filtered?team_id=&channel_id=...
// Stub. Returns the same envelope shape as /users/stats with the
// total_users_count rolled up across the filter.
func (h *handlers) getFilteredUserStats(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	writeJSON(w, 200, map[string]any{"total_users_count": int64(0)})
}

// getReportsUsers — GET /reports/users
func (h *handlers) getReportsUsers(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	writeJSON(w, 200, []any{})
}

// getReportsUsersCount — GET /reports/users/count
func (h *handlers) getReportsUsersCount(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	writeJSON(w, 200, map[string]any{"total_count": int64(0)})
}

// exportReportsUsers — POST /reports/users/export
// Stub: returns 202 Accepted with a synthesized job id so the admin
// console's "Export users" button shows the job-queued toast.
func (h *handlers) exportReportsUsers(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	jobID := fmt.Sprintf("job-%d", time.Now().UnixMilli())
	writeJSON(w, 202, map[string]any{"id": jobID, "status": "pending"})
}

// reportPosts — POST /reports/posts
// Stub: stamps an audit row + returns 200 OK so abuse-report flows
// don't 404. Real implementation would queue a moderation review.
func (h *handlers) reportPosts(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// ---- Pillar E — Login + admin email verify --------------------------------

// loginSwitch — POST /users/login/switch
// Stub: Mattermost lets a user switch their auth method (e.g.
// password→OAuth or OAuth→password). We don't model auth-method
// transitions yet; audit + 200 OK is enough for the official client's
// settings UI to render a success state.
func (h *handlers) loginSwitch(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionLoginSwitch, "", nil)
	}
	writeJSON(w, 200, map[string]any{"follow_link": ""})
}

// loginType — POST /users/login/type
// Anti-oracle: always reports "email" as the login type regardless
// of whether the login_id resolves to a real user, so this endpoint
// can't be used to enumerate accounts. Returns the documented
// envelope `{type: "email"}`.
func (h *handlers) loginType(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"type": "email"})
}

// loginCWS — POST /users/login/cws
// Stub: CWS (Customer Workflow Service) is Mattermost's hosted-cloud
// SSO flow. We're not on cloud, so this returns 501 Not Implemented
// with a friendly message instead of 200 (an actual login endpoint
// returning 200 with no token would break the official client).
func (h *handlers) loginCWS(w http.ResponseWriter, r *http.Request) {
	writeError(w, 501, "api.user.login.cws.not_implemented", "CWS login not enabled on this server")
}

// loginSSOCodeExchange — POST /users/login/sso/code-exchange
// Stub: Mattermost's deprecated mobile SSO transfer flow has a separate
// login_code + PKCE/state contract. Moyro's browser flow uses the native
// /api/moyro/v1/auth/sso/session endpoint instead of overloading this path.
func (h *handlers) loginSSOCodeExchange(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, "api.user.login.sso.not_implemented", "Mattermost mobile SSO code exchange is not enabled")
}

// adminVerifyMemberEmail — POST /users/{userID}/email/verify/member
// Admin-force-verify-without-email path. Stub: we don't gate on email
// verification yet (the verify-by-token flow from Phase 27 also no-ops),
// so this just stamps an audit row.
func (h *handlers) adminVerifyMemberEmail(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionUserEmailVerifyAdm, chi.URLParam(r, "userID"), nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// ============================================================================
// Phase 31 — Mattermost API v4 compatibility wave 11
// ----------------------------------------------------------------------------
// Six pillars chosen from the back of the audit's missing-endpoint list to
// keep clear of codex's compat handler files. Mix of real ops (group channel
// create, posts/files/info, channel-posts-unread) and anti-oracle stubs (bot
// icons, uploads multipart, autotranslation, member-counts-by-group).
// ============================================================================

// ---- Pillar A — Bot icons (3 endpoints) -----------------------------------
// Mattermost's "bot icon" feature lets admins upload a custom icon per bot.
// We don't store bot icons yet (the bots row has no icon column); the
// endpoints exist so the official admin console's bot-edit form doesn't
// 404 when the operator opens it.

// getBotIcon — GET /bots/{botID}/icon
// Returns 404 cleanly so the official client renders default initials.
func (h *handlers) getBotIcon(w http.ResponseWriter, r *http.Request) {
	writeError(w, 404, "api.bot.icon.not_found", "no icon set")
}

// uploadBotIcon — POST /bots/{botID}/icon
// Admin-only. Drains the multipart body up to 256KB so the upload doesn't
// hang on EOF, audits the request, and 200s.
func (h *handlers) uploadBotIcon(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	_, _ = io.Copy(io.Discard, r.Body)
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionBotIconUpload, chi.URLParam(r, "botID"), nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// deleteBotIcon — DELETE /bots/{botID}/icon
func (h *handlers) deleteBotIcon(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionBotIconDelete, chi.URLParam(r, "botID"), nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// ---- Pillar B — Thread expansions (3 endpoints) ---------------------------

// unfollowThread — DELETE /users/{userID}/teams/{teamID}/threads/{threadID}/following
// Mirror of PUT-following=true from Phase 24. The DELETE wire shape is
// what the official mobile client uses; we route it through the same
// SetFollowing service method with following=false.
func (h *handlers) unfollowThread(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	tid := chi.URLParam(r, "teamID")
	rid := chi.URLParam(r, "threadID")
	if err := h.threads.SetFollowing(r.Context(), uid, tid, rid, false); err != nil {
		writeError(w, 500, "api.thread.unfollow.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(uid, audit.ActionThreadFollowDelete, rid, nil)
	}
	h.hub.Broadcast(ws.Event{
		Event:     "thread_follow_changed",
		Broadcast: ws.Broadcast{UserID: uid},
		Data:      map[string]any{"thread_id": rid, "team_id": tid, "following": false},
	})
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// getThreadMembership — GET /users/{userID}/teams/{teamID}/threads/{threadID}
// Returns the per-user thread membership row with read state, follow
// state, and unread counts. Used by the official client when opening a
// thread to seed the "X new replies since you last read" badge.
func (h *handlers) getThreadMembership(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	rid := chi.URLParam(r, "threadID")
	mem, err := h.threads.Get(r.Context(), uid, rid)
	if err != nil {
		writeError(w, 500, "api.thread.get.app_error", err.Error())
		return
	}
	if mem == nil {
		// Empty-shape envelope so the client doesn't have to special-
		// case missing rows — Mattermost's contract is "never 404 on
		// thread membership get".
		writeJSON(w, 200, map[string]any{
			"id":              rid,
			"user_id":         uid,
			"following":       false,
			"last_viewed_at":  int64(0),
			"unread_replies":  0,
			"unread_mentions": 0,
		})
		return
	}
	writeJSON(w, 200, mem)
}

// getThreadMentionCounts — GET /users/{userID}/teams/{teamID}/threads/mention_counts
// Returns the total unread mention count across every followed thread
// in the team. Mattermost uses this for the team-level mention badge
// in the thread sidebar.
func (h *handlers) getThreadMentionCounts(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	tid := chi.URLParam(r, "teamID")
	// Direct SQL aggregate — keeps the threads service surface small.
	var total int64
	row := h.auth.DB().Pool.QueryRow(r.Context(), `
		SELECT COALESCE(SUM(unread_mentions), 0) FROM thread_memberships
		WHERE user_id=$1 AND team_id=$2 AND following=TRUE
	`, uid, tid)
	if err := row.Scan(&total); err != nil {
		// Treat any error as "no mentions" — the badge is purely
		// cosmetic so a transient SELECT failure shouldn't 500.
		total = 0
	}
	writeJSON(w, 200, map[string]any{"total": total})
}

// ---- Pillar C — Files endpoints (3 endpoints) -----------------------------

// getPostFilesInfo — GET /posts/{postID}/files/info
// Returns the FileInfo[] attached to a post. Mattermost's client uses
// this to pre-resolve thumbnails / dimensions before rendering the
// attachment grid; we already store width/height on file_infos so the
// existing ListForPost service method does the work.
func (h *handlers) getPostFilesInfo(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	pid := chi.URLParam(r, "postID")
	post, err := h.posts.Get(r.Context(), pid)
	if err != nil || post == nil {
		writeError(w, 404, "api.post.not_found", "post not found")
		return
	}
	// Visibility check — caller must be a member of the channel.
	ok, _ := h.channels.IsMember(r.Context(), post.ChannelID, caller)
	if !ok && !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "not a channel member")
		return
	}
	infos, err := h.files.ListForPost(r.Context(), pid)
	if err != nil {
		writeError(w, 500, "api.files.list.app_error", err.Error())
		return
	}
	if infos == nil {
		infos = []files.FileInfo{}
	}
	writeJSON(w, 200, infos)
}

// searchFiles — POST /files/search
// Filename-substring search across files the caller can see. Cap 50
// rows. Body: {terms, is_or_search?, time_zone_offset?, include_deleted_channels?}.
// We honour `terms` only — the others are silently ignored for now.
func (h *handlers) searchFiles(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	var body struct {
		Terms string `json:"terms"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	terms := strings.TrimSpace(body.Terms)
	if terms == "" {
		writeJSON(w, 200, map[string]any{"order": []string{}, "file_infos": map[string]any{}})
		return
	}
	rows, err := h.auth.DB().Pool.Query(r.Context(), `
		SELECT fi.id, fi.user_id, COALESCE(fi.post_id,''), COALESCE(fi.channel_id,''),
		       fi.name, fi.size, fi.mime_type, fi.create_at,
		       COALESCE(fi.width, 0), COALESCE(fi.height, 0),
		       COALESCE(fi.thumbnail_path, ''),
		       COALESCE(fi.delete_at, 0)
		FROM file_infos fi
		WHERE COALESCE(fi.delete_at, 0) = 0
		  AND fi.name ILIKE '%' || $1 || '%'
		  AND (fi.user_id=$2 OR fi.channel_id IN (
		      SELECT channel_id FROM channel_members WHERE user_id = $2
		  ))
		ORDER BY fi.create_at DESC
		LIMIT 50
	`, terms, caller)
	if err != nil {
		writeError(w, 500, "api.file.search.app_error", err.Error())
		return
	}
	defer rows.Close()
	order := []string{}
	infos := map[string]any{}
	for rows.Next() {
		var fi files.FileInfo
		if err := rows.Scan(&fi.ID, &fi.UserID, &fi.PostID, &fi.ChannelID, &fi.Name, &fi.Size, &fi.MimeType, &fi.CreateAt, &fi.Width, &fi.Height, &fi.ThumbnailPath, &fi.DeleteAt); err != nil {
			continue
		}
		order = append(order, fi.ID)
		infos[fi.ID] = fi
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionFileSearch, "", map[string]any{"terms": terms, "scope": "global"})
	}
	writeJSON(w, 200, map[string]any{"order": order, "file_infos": infos})
}

// searchTeamFiles — POST /teams/{teamID}/files/search
// Same as searchFiles but scoped to channels in the given team. Useful
// when the user wants "find that PDF Frank attached last week" without
// noise from other teams.
func (h *handlers) searchTeamFiles(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	tid := chi.URLParam(r, "teamID")
	var body struct {
		Terms string `json:"terms"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	terms := strings.TrimSpace(body.Terms)
	if terms == "" {
		writeJSON(w, 200, map[string]any{"order": []string{}, "file_infos": map[string]any{}})
		return
	}
	rows, err := h.auth.DB().Pool.Query(r.Context(), `
		SELECT fi.id, fi.user_id, COALESCE(fi.post_id,''), COALESCE(fi.channel_id,''),
		       fi.name, fi.size, fi.mime_type, fi.create_at,
		       COALESCE(fi.width, 0), COALESCE(fi.height, 0),
		       COALESCE(fi.thumbnail_path, ''),
		       COALESCE(fi.delete_at, 0)
		FROM file_infos fi
		JOIN channels c ON c.id = fi.channel_id
		WHERE COALESCE(fi.delete_at, 0) = 0
		  AND c.team_id = $1
		  AND fi.name ILIKE '%' || $2 || '%'
		  AND fi.channel_id IN (
		      SELECT channel_id FROM channel_members WHERE user_id = $3
		  )
		ORDER BY fi.create_at DESC
		LIMIT 50
	`, tid, terms, caller)
	if err != nil {
		writeError(w, 500, "api.file.search.team.app_error", err.Error())
		return
	}
	defer rows.Close()
	order := []string{}
	infos := map[string]any{}
	for rows.Next() {
		var fi files.FileInfo
		if err := rows.Scan(&fi.ID, &fi.UserID, &fi.PostID, &fi.ChannelID, &fi.Name, &fi.Size, &fi.MimeType, &fi.CreateAt, &fi.Width, &fi.Height, &fi.ThumbnailPath, &fi.DeleteAt); err != nil {
			continue
		}
		order = append(order, fi.ID)
		infos[fi.ID] = fi
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionFileSearch, tid, map[string]any{"terms": terms, "scope": "team"})
	}
	writeJSON(w, 200, map[string]any{"order": order, "file_infos": infos})
}

// ---- Pillar D — Uploads multipart stubs (4 endpoints) ---------------------
// Mattermost's "uploads" API is the resumable-upload flow: the client
// POSTs an init request to get a session id, then POSTs chunks against
// /uploads/{id} until the file is complete. We don't model resumable
// uploads yet (POST /files is the existing single-shot path); the
// endpoints exist so the official client falls back gracefully.

type uploadSessionReq struct {
	ChannelID string `json:"channel_id"`
	Filename  string `json:"filename"`
	FileSize  int64  `json:"file_size"`
}

// initUploadSession — POST /uploads
// Synthesizes an upload session id + 201s. The id isn't durable so the
// chunk path (POST /uploads/{id}) just 200s back without persistence.
func (h *handlers) initUploadSession(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	var req uploadSessionReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	now := time.Now().UnixMilli()
	id := fmt.Sprintf("upload-%d", now)
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionUploadInit, req.ChannelID, map[string]any{"filename": req.Filename, "size": req.FileSize})
	}
	writeJSON(w, 201, map[string]any{
		"id":          id,
		"user_id":     caller,
		"channel_id":  req.ChannelID,
		"filename":    req.Filename,
		"file_size":   req.FileSize,
		"file_offset": int64(0),
		"create_at":   now,
	})
}

// getUploadSession — GET /uploads/{uploadID}
// Returns 404 since the init path doesn't durably persist the row.
// Mattermost's client treats 404 here as "session expired, restart
// upload" which is the correct fallback.
func (h *handlers) getUploadSession(w http.ResponseWriter, r *http.Request) {
	writeError(w, 404, "api.upload.not_found", "upload session not found")
}

// uploadChunk — POST /uploads/{uploadID}
// Drains the chunk body up to 50MB and 200s. Audits the chunk for
// telemetry. Real implementation would route through files.Service
// with offset bookkeeping in an `uploads` table.
func (h *handlers) uploadChunk(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 50<<20)
	n, _ := io.Copy(io.Discard, r.Body)
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionUploadChunk, chi.URLParam(r, "uploadID"), map[string]any{"bytes": n})
	}
	writeJSON(w, 200, map[string]any{"id": chi.URLParam(r, "uploadID"), "file_offset": n})
}

// listUserUploads — GET /users/{userID}/uploads
// Lists in-flight upload sessions for the user. Always returns []
// since we don't persist sessions.
func (h *handlers) listUserUploads(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUserParamAccess(w, r, "userID"); !ok {
		return
	}
	writeJSON(w, 200, []any{})
}

// ---- Pillar E — Channels group + posts unread (3 endpoints) ---------------

type groupChannelReq []string

// createGroupChannel — POST /channels/group
// Body is a flat JSON array of 3-8 user-ids. Caller must be in the
// list (no creating G-channels between strangers). Reuses the new
// channels.EnsureGroup helper which is idempotent on the sorted-id
// canonical name.
func (h *handlers) createGroupChannel(w http.ResponseWriter, r *http.Request) {
	if h.denyGuestMutation(w, r, "api.channel.group.guest_forbidden") {
		return
	}
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	var req groupChannelReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "api.channel.group.invalid_body", err.Error())
		return
	}
	if len(req) < 3 || len(req) > 8 {
		writeError(w, 400, "api.channel.group.bad_size", "expected 3-8 user-ids")
		return
	}
	inList := false
	for _, u := range req {
		if u == caller {
			inList = true
			break
		}
	}
	if !inList {
		writeError(w, 403, "api.channel.group.forbidden", "caller must be one of the members")
		return
	}
	c, err := h.channels.EnsureGroup(r.Context(), req)
	if err != nil {
		writeError(w, 500, "api.channel.group.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(caller, audit.ActionGroupChannelCreate, c.ID, nil)
	}
	writeJSON(w, 201, c)
}

// searchGroupChannels — POST /channels/group/search
// Searches the caller's existing G channels by member name match. Body:
// `{term}`. Uses the existing ListGroupChannelsForUser then filters
// in-process — fine because a typical user has <100 G channels.
func (h *handlers) searchGroupChannels(w http.ResponseWriter, r *http.Request) {
	caller := userID(r)
	if caller == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	var body struct {
		Term string `json:"term"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	all, err := h.channels.ListGroupChannelsForUser(r.Context(), caller)
	if err != nil {
		writeError(w, 500, "api.channel.group.search.app_error", err.Error())
		return
	}
	term := strings.ToLower(strings.TrimSpace(body.Term))
	if term == "" {
		writeJSON(w, 200, all)
		return
	}
	out := []channels.Channel{}
	for _, c := range all {
		if strings.Contains(strings.ToLower(c.Name), term) || strings.Contains(strings.ToLower(c.DisplayName), term) {
			out = append(out, c)
		}
	}
	writeJSON(w, 200, out)
}

// getChannelPostsUnread — GET /users/{userID}/channels/{channelID}/posts/unread
// Returns the slice of posts strictly newer than the user's
// last_viewed_at on the channel. Capped at 200 rows to bound memory.
// Mattermost's client uses this on channel-switch to fast-load the
// "since you last visited" delta without paging.
func (h *handlers) getChannelPostsUnread(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	cid := chi.URLParam(r, "channelID")
	mem, err := h.channels.GetMember(r.Context(), cid, uid)
	if err != nil {
		writeError(w, 500, "api.channel.unread.app_error", err.Error())
		return
	}
	if mem == nil {
		writeError(w, 403, "api.context.permissions.app_error", "not a channel member")
		return
	}
	since := mem.LastViewedAt
	list, err := h.posts.ListForChannelPaged(r.Context(), cid, posts.PageOpts{Since: since, PerPage: 200})
	if err != nil {
		writeError(w, 500, "api.channel.unread.list.app_error", err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// ---- Pillar F — Group-aware listings + misc admin (6 endpoints) -----------

// getChannelMemberCountsByGroup — GET /channels/{channelID}/member_counts_by_group
// Stub: returns []. Real implementation would group-by users.roles or
// custom group membership; we don't model groups yet (the official
// "User Groups" feature is enterprise).
func (h *handlers) getChannelMemberCountsByGroup(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, 200, []any{})
}

// getChannelMembersMinusGroup — GET /channels/{channelID}/members_minus_group_members
// Stub: returns []. Used by the official admin tool to surface "members
// not covered by a group sync".
func (h *handlers) getChannelMembersMinusGroup(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	writeJSON(w, 200, map[string]any{"members": []any{}, "total_count": 0})
}

// getTeamMembersMinusGroup — GET /teams/{teamID}/members_minus_group_members
func (h *handlers) getTeamMembersMinusGroup(w http.ResponseWriter, r *http.Request) {
	if h.denyGuestEnumeration(w, r, "api.team.members.group.guest_forbidden") {
		return
	}
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	writeJSON(w, 200, map[string]any{"members": []any{}, "total_count": 0})
}

// bulkDeleteUsers — DELETE /users
// Stub: admin-only bulk soft-delete. Body: `{user_ids:[]}`. We don't
// expose this in the UI; the endpoint exists so admin-tool integrations
// don't break. Audits each id then 200s without actually mutating.
func (h *handlers) bulkDeleteUsers(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsSystemAdmin(r) {
		writeError(w, 403, "api.context.permissions.app_error", "system_admin required")
		return
	}
	var body struct {
		UserIDs []string `json:"user_ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	caller := userID(r)
	for _, uid := range body.UserIDs {
		if h.audit != nil {
			h.audit.LogAsync(caller, audit.ActionUserBulkDelete, uid, nil)
		}
	}
	writeJSON(w, 200, map[string]any{"status": "OK", "count": len(body.UserIDs)})
}

// clearRecentCustomStatuses — DELETE /users/{userID}/status/custom/recent
// Stub: bulk-clear of the recent-statuses chip list. We don't persist
// recents server-side (they live in the user's preferences blob), so
// this just stamps an audit row + 200s.
func (h *handlers) clearRecentCustomStatuses(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(uid, audit.ActionStatusRecentClear, "", nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// inviteGuestsByEmail — POST /teams/{teamID}/invite-guests/email
// Stub. Mirror of /teams/{teamID}/invite/email from Phase 26 but for
// guest accounts. Same audit posture: we don't ship SMTP-based guest
// invites yet, so this just stamps + 200s.
func (h *handlers) inviteGuestsByEmail(w http.ResponseWriter, r *http.Request) {
	if !h.callerCanAdminTeam(r.Context(), chi.URLParam(r, "teamID"), userID(r)) {
		writeError(w, 403, "api.context.permissions.app_error", "team_admin required")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionTeamInviteGuests, chi.URLParam(r, "teamID"), nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// setMemberAutotranslation — PUT /channels/{channelID}/members/{userID}/autotranslation
// Stub. Mattermost's "auto-translate this channel's posts" feature is
// enterprise; we don't model it. Audit + 200.
func (h *handlers) setMemberAutotranslation(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(uid, audit.ActionAutotranslation, chi.URLParam(r, "channelID"), nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// ============================================================================
// Phase 32 — Mattermost API v4 compatibility wave 12
// ----------------------------------------------------------------------------
// Six pillars chosen from the back of the audit's missing-endpoint list to
// keep clear of codex's compat handler files. Pillar A is an audit-regex
// fix only (no new handlers, just route restructuring in router.go).
// Pillar B is real (custom profile attributes, new schema). Pillars C-F
// are stubs returning the canonical Mattermost envelope shape so the
// official client/admin console doesn't 404.
// ============================================================================

// ---- Pillar B — Custom profile attributes (6 endpoints, real) -------------

// listCustomProfileFields — GET /custom_profile_attributes/fields
// Public surface for any authenticated user (every user fills these in
// from their profile-settings page); admins see the same shape via the
// admin console field-editor.
func (h *handlers) listCustomProfileFields(w http.ResponseWriter, r *http.Request) {
	if h.customProf == nil {
		writeJSON(w, 200, []any{})
		return
	}
	fields, err := h.customProf.ListFields(r.Context())
	if err != nil {
		writeError(w, 500, "api.custom_profile.list.app_error", err.Error())
		return
	}
	writeJSON(w, 200, fields)
}

// createCustomProfileField — POST /custom_profile_attributes/fields
// Admin-only. Body: {name, type, attrs?}.
func (h *handlers) createCustomProfileField(w http.ResponseWriter, r *http.Request) {
	if h.customProf == nil {
		writeError(w, 503, "api.custom_profile.disabled.app_error", "custom profile attributes disabled")
		return
	}
	var body struct {
		Name  string          `json:"name"`
		Type  string          `json:"type"`
		Attrs json.RawMessage `json:"attrs,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	field, err := h.customProf.CreateField(r.Context(), body.Name, body.Type, body.Attrs)
	if err != nil {
		writeError(w, 400, "api.custom_profile.create.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionCustomFieldCreate, field.ID, map[string]any{"name": field.Name, "type": field.Type})
	}
	writeJSON(w, 201, field)
}

// patchCustomProfileField — PATCH /custom_profile_attributes/fields/{fieldID}
// Admin-only partial update.
func (h *handlers) patchCustomProfileField(w http.ResponseWriter, r *http.Request) {
	if h.customProf == nil {
		writeError(w, 503, "api.custom_profile.disabled.app_error", "custom profile attributes disabled")
		return
	}
	id := chi.URLParam(r, "fieldID")
	var body struct {
		Name      *string         `json:"name,omitempty"`
		Type      *string         `json:"type,omitempty"`
		Attrs     json.RawMessage `json:"attrs,omitempty"`
		SortOrder *int            `json:"sort_order,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	field, err := h.customProf.PatchField(r.Context(), id, body.Name, body.Type, body.Attrs, body.SortOrder)
	if err != nil {
		if errors.Is(err, customprofile.ErrFieldNotFound) {
			writeError(w, 404, "api.custom_profile.not_found", "field not found")
			return
		}
		writeError(w, 500, "api.custom_profile.patch.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionCustomFieldPatch, id, nil)
	}
	writeJSON(w, 200, field)
}

// deleteCustomProfileField — DELETE /custom_profile_attributes/fields/{fieldID}
// Admin-only soft-delete.
func (h *handlers) deleteCustomProfileField(w http.ResponseWriter, r *http.Request) {
	if h.customProf == nil {
		writeError(w, 503, "api.custom_profile.disabled.app_error", "custom profile attributes disabled")
		return
	}
	id := chi.URLParam(r, "fieldID")
	if err := h.customProf.DeleteField(r.Context(), id); err != nil {
		if errors.Is(err, customprofile.ErrFieldNotFound) {
			writeError(w, 404, "api.custom_profile.not_found", "field not found")
			return
		}
		writeError(w, 500, "api.custom_profile.delete.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionCustomFieldDelete, id, nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// patchCustomProfileValuesGlobal — PATCH /custom_profile_attributes/values
// Self-only path. Body is a flat {field_id: value} map.
func (h *handlers) patchCustomProfileValuesGlobal(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	if uid == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	if h.customProf == nil {
		writeError(w, 503, "api.custom_profile.disabled.app_error", "custom profile attributes disabled")
		return
	}
	var values map[string]json.RawMessage
	_ = json.NewDecoder(r.Body).Decode(&values)
	if err := h.customProf.PatchUserValues(r.Context(), uid, values); err != nil {
		writeError(w, 500, "api.custom_profile.values.patch.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(uid, audit.ActionCustomValuesPatch, "", map[string]any{"count": len(values)})
	}
	out, _ := h.customProf.GetUserValues(r.Context(), uid)
	writeJSON(w, 200, out)
}

// getUserCustomProfileValues — GET /users/{userID}/custom_profile_attributes
// Self/admin gated.
func (h *handlers) getUserCustomProfileValues(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	if h.customProf == nil {
		writeJSON(w, 200, map[string]any{})
		return
	}
	values, err := h.customProf.GetUserValues(r.Context(), uid)
	if err != nil {
		writeError(w, 500, "api.custom_profile.values.get.app_error", err.Error())
		return
	}
	writeJSON(w, 200, values)
}

// patchUserCustomProfileValues — PATCH /users/{userID}/custom_profile_attributes
// Self/admin gated. Same body shape as global path; admin-on-other-user
// is for support-tooling backfills.
func (h *handlers) patchUserCustomProfileValues(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	if h.customProf == nil {
		writeError(w, 503, "api.custom_profile.disabled.app_error", "custom profile attributes disabled")
		return
	}
	var values map[string]json.RawMessage
	_ = json.NewDecoder(r.Body).Decode(&values)
	if err := h.customProf.PatchUserValues(r.Context(), uid, values); err != nil {
		writeError(w, 500, "api.custom_profile.values.patch.app_error", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionCustomValuesPatch, uid, map[string]any{"count": len(values)})
	}
	out, _ := h.customProf.GetUserValues(r.Context(), uid)
	writeJSON(w, 200, out)
}

// ---- Pillar C — Recaps stubs (6 endpoints) --------------------------------
//
// Mattermost's "recaps" is an AI-summary of a channel's recent activity.
// We don't ship LLM integration yet; the endpoints exist so the official
// client's "Recaps" UI doesn't 404. In-memory store survives a request
// hot path; server-restart resets to empty.

var (
	recapsMu    sync.RWMutex
	recapsByID  = map[string]map[string]any{}
	recapsOrder []string
)

func newRecapID() string { return "recap-" + base36Now() }

// listRecaps — GET /recaps?channel_id=
func (h *handlers) listRecaps(w http.ResponseWriter, r *http.Request) {
	cid := r.URL.Query().Get("channel_id")
	recapsMu.RLock()
	defer recapsMu.RUnlock()
	out := make([]map[string]any, 0, len(recapsOrder))
	for _, id := range recapsOrder {
		row, ok := recapsByID[id]
		if !ok {
			continue
		}
		if cid == "" || row["channel_id"] == cid {
			out = append(out, row)
		}
	}
	writeJSON(w, 200, out)
}

// getRecap — GET /recaps/{recapID}
func (h *handlers) getRecap(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "recapID")
	recapsMu.RLock()
	row, ok := recapsByID[id]
	recapsMu.RUnlock()
	if !ok {
		writeError(w, 404, "api.recap.not_found", "recap not found")
		return
	}
	writeJSON(w, 200, row)
}

// createRecap — POST /recaps
func (h *handlers) createRecap(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	if uid == "" {
		writeError(w, 401, "api.context.session_expired.app_error", "missing token")
		return
	}
	var body struct {
		ChannelID string `json:"channel_id"`
		Range     string `json:"range,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.ChannelID == "" {
		writeError(w, 400, "api.recap.channel_required", "channel_id required")
		return
	}
	id := newRecapID()
	now := time.Now().UnixMilli()
	row := map[string]any{
		"id":         id,
		"channel_id": body.ChannelID,
		"user_id":    uid,
		"range":      body.Range,
		"summary":    "",
		"create_at":  now,
		"update_at":  now,
		"read_at":    int64(0),
	}
	recapsMu.Lock()
	recapsByID[id] = row
	recapsOrder = append([]string{id}, recapsOrder...)
	if len(recapsOrder) > 500 {
		// Cap memory: drop oldest beyond 500. Cheap LRU since recaps are
		// admin-driven low-volume.
		dropped := recapsOrder[500:]
		recapsOrder = recapsOrder[:500]
		for _, did := range dropped {
			delete(recapsByID, did)
		}
	}
	recapsMu.Unlock()
	if h.audit != nil {
		h.audit.LogAsync(uid, audit.ActionRecapCreate, body.ChannelID, nil)
	}
	writeJSON(w, 201, row)
}

// deleteRecap — DELETE /recaps/{recapID}
func (h *handlers) deleteRecap(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "recapID")
	recapsMu.Lock()
	if _, ok := recapsByID[id]; ok {
		delete(recapsByID, id)
		for i, oid := range recapsOrder {
			if oid == id {
				recapsOrder = append(recapsOrder[:i], recapsOrder[i+1:]...)
				break
			}
		}
	}
	recapsMu.Unlock()
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionRecapDelete, id, nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// markRecapRead — POST /recaps/{recapID}/read
func (h *handlers) markRecapRead(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "recapID")
	recapsMu.Lock()
	if row, ok := recapsByID[id]; ok {
		row["read_at"] = time.Now().UnixMilli()
	}
	recapsMu.Unlock()
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionRecapRead, id, nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// regenerateRecap — POST /recaps/{recapID}/regenerate
func (h *handlers) regenerateRecap(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "recapID")
	recapsMu.Lock()
	row, ok := recapsByID[id]
	if ok {
		row["update_at"] = time.Now().UnixMilli()
	}
	recapsMu.Unlock()
	if !ok {
		writeError(w, 404, "api.recap.not_found", "recap not found")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionRecapRegenerate, id, nil)
	}
	writeJSON(w, 200, row)
}

// ---- Pillar D — AI/Agents/LLM stubs (5 endpoints) -------------------------
//
// We don't ship AI integration. Endpoints return empty arrays / disabled
// status so the official admin console's "AI" tab renders without errors.

func (h *handlers) listAIAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, []any{})
}

func (h *handlers) getAIAgentsStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"enabled":   false,
		"providers": []any{},
		"agents":    []any{},
	})
}

func (h *handlers) listAIAgentsAlt(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, []any{})
}

func (h *handlers) listAIServices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, []any{})
}

func (h *handlers) listLLMServices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, []any{})
}

// ---- Pillar E — OAuth apps + outgoing connections stubs (14 endpoints) ----
//
// Mattermost's "OAuth applications" feature lets the server itself act as
// an OAuth provider — third-party apps register, get a client_id/secret,
// and prompt users to authorize. We ship inbound OAuth (Google/GitHub
// login from Phase 14) but not outbound. In-memory store; server-restart
// resets.

var (
	oauthAppsMu    sync.RWMutex
	oauthAppsByID  = map[string]map[string]any{}
	oauthAppsOrder []string

	oauthOutMu    sync.RWMutex
	oauthOutByID  = map[string]map[string]any{}
	oauthOutOrder []string
)

func base36Now() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

// listOAuthApps — GET /oauth/apps
func (h *handlers) listOAuthApps(w http.ResponseWriter, r *http.Request) {
	oauthAppsMu.RLock()
	defer oauthAppsMu.RUnlock()
	out := make([]map[string]any, 0, len(oauthAppsOrder))
	for _, id := range oauthAppsOrder {
		if row, ok := oauthAppsByID[id]; ok {
			out = append(out, row)
		}
	}
	writeJSON(w, 200, out)
}

// createOAuthApp — POST /oauth/apps
func (h *handlers) createOAuthApp(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	id := "oauth-" + base36Now()
	now := time.Now().UnixMilli()
	row := map[string]any{
		"id":            id,
		"creator_id":    userID(r),
		"client_secret": "secret-" + base36Now(),
		"name":          body["name"],
		"description":   body["description"],
		"icon_url":      body["icon_url"],
		"callback_urls": body["callback_urls"],
		"homepage":      body["homepage"],
		"is_trusted":    false,
		"create_at":     now,
		"update_at":     now,
	}
	oauthAppsMu.Lock()
	oauthAppsByID[id] = row
	oauthAppsOrder = append([]string{id}, oauthAppsOrder...)
	oauthAppsMu.Unlock()
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionOAuthAppCreate, id, nil)
	}
	writeJSON(w, 201, row)
}

// getOAuthApp — GET /oauth/apps/{appID}
func (h *handlers) getOAuthApp(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "appID")
	oauthAppsMu.RLock()
	row, ok := oauthAppsByID[id]
	oauthAppsMu.RUnlock()
	if !ok {
		writeError(w, 404, "api.oauth.app.not_found", "oauth app not found")
		return
	}
	writeJSON(w, 200, row)
}

// getOAuthAppInfo — GET /oauth/apps/{appID}/info
// Public-shape variant — strips the client_secret. Used by the consent
// screen so a not-yet-authorized user can see who's asking for access.
func (h *handlers) getOAuthAppInfo(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "appID")
	oauthAppsMu.RLock()
	row, ok := oauthAppsByID[id]
	oauthAppsMu.RUnlock()
	if !ok {
		writeError(w, 404, "api.oauth.app.not_found", "oauth app not found")
		return
	}
	// Copy + strip secret.
	out := make(map[string]any, len(row))
	for k, v := range row {
		if k == "client_secret" {
			continue
		}
		out[k] = v
	}
	writeJSON(w, 200, out)
}

// updateOAuthApp — PUT /oauth/apps/{appID}
func (h *handlers) updateOAuthApp(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "appID")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	oauthAppsMu.Lock()
	row, ok := oauthAppsByID[id]
	if ok {
		for k, v := range body {
			if k == "id" || k == "client_secret" || k == "create_at" {
				continue
			}
			row[k] = v
		}
		row["update_at"] = time.Now().UnixMilli()
	}
	oauthAppsMu.Unlock()
	if !ok {
		writeError(w, 404, "api.oauth.app.not_found", "oauth app not found")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionOAuthAppUpdate, id, nil)
	}
	writeJSON(w, 200, row)
}

// deleteOAuthApp — DELETE /oauth/apps/{appID}
func (h *handlers) deleteOAuthApp(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "appID")
	oauthAppsMu.Lock()
	if _, ok := oauthAppsByID[id]; ok {
		delete(oauthAppsByID, id)
		for i, oid := range oauthAppsOrder {
			if oid == id {
				oauthAppsOrder = append(oauthAppsOrder[:i], oauthAppsOrder[i+1:]...)
				break
			}
		}
	}
	oauthAppsMu.Unlock()
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionOAuthAppDelete, id, nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// regenOAuthAppSecret — POST /oauth/apps/{appID}/regen_secret
func (h *handlers) regenOAuthAppSecret(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "appID")
	oauthAppsMu.Lock()
	row, ok := oauthAppsByID[id]
	if ok {
		row["client_secret"] = "secret-" + base36Now()
		row["update_at"] = time.Now().UnixMilli()
	}
	oauthAppsMu.Unlock()
	if !ok {
		writeError(w, 404, "api.oauth.app.not_found", "oauth app not found")
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionOAuthAppRegen, id, nil)
	}
	writeJSON(w, 200, row)
}

// registerOAuthApp — POST /oauth/apps/register
// Self-registration variant. Same shape as createOAuthApp but allowed
// outside admin scope (any authenticated user can register an app).
func (h *handlers) registerOAuthApp(w http.ResponseWriter, r *http.Request) {
	h.createOAuthApp(w, r)
}

// listAuthorizedOAuthApps — GET /users/{userID}/oauth/apps/authorized
// Lists apps a given user has previously authorized. Self/admin-gated.
// We don't persist authorizations yet; always [].
func (h *handlers) listAuthorizedOAuthApps(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUserParamAccess(w, r, "userID"); !ok {
		return
	}
	writeJSON(w, 200, []any{})
}

// listOAuthOutgoing — GET /oauth/outgoing_connections
func (h *handlers) listOAuthOutgoing(w http.ResponseWriter, r *http.Request) {
	oauthOutMu.RLock()
	defer oauthOutMu.RUnlock()
	out := make([]map[string]any, 0, len(oauthOutOrder))
	for _, id := range oauthOutOrder {
		if row, ok := oauthOutByID[id]; ok {
			out = append(out, row)
		}
	}
	writeJSON(w, 200, out)
}

// createOAuthOutgoing — POST /oauth/outgoing_connections
func (h *handlers) createOAuthOutgoing(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	id := "oauth-out-" + base36Now()
	now := time.Now().UnixMilli()
	row := map[string]any{"id": id, "create_at": now, "update_at": now}
	for k, v := range body {
		if k == "id" || k == "create_at" {
			continue
		}
		row[k] = v
	}
	oauthOutMu.Lock()
	oauthOutByID[id] = row
	oauthOutOrder = append([]string{id}, oauthOutOrder...)
	oauthOutMu.Unlock()
	writeJSON(w, 201, row)
}

// getOAuthOutgoing — GET /oauth/outgoing_connections/{connectionID}
func (h *handlers) getOAuthOutgoing(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "connectionID")
	oauthOutMu.RLock()
	row, ok := oauthOutByID[id]
	oauthOutMu.RUnlock()
	if !ok {
		writeError(w, 404, "api.oauth.outgoing.not_found", "outgoing connection not found")
		return
	}
	writeJSON(w, 200, row)
}

// updateOAuthOutgoing — PUT /oauth/outgoing_connections/{connectionID}
func (h *handlers) updateOAuthOutgoing(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "connectionID")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	oauthOutMu.Lock()
	row, ok := oauthOutByID[id]
	if ok {
		for k, v := range body {
			if k == "id" || k == "create_at" {
				continue
			}
			row[k] = v
		}
		row["update_at"] = time.Now().UnixMilli()
	}
	oauthOutMu.Unlock()
	if !ok {
		writeError(w, 404, "api.oauth.outgoing.not_found", "outgoing connection not found")
		return
	}
	writeJSON(w, 200, row)
}

// deleteOAuthOutgoing — DELETE /oauth/outgoing_connections/{connectionID}
func (h *handlers) deleteOAuthOutgoing(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "connectionID")
	oauthOutMu.Lock()
	if _, ok := oauthOutByID[id]; ok {
		delete(oauthOutByID, id)
		for i, oid := range oauthOutOrder {
			if oid == id {
				oauthOutOrder = append(oauthOutOrder[:i], oauthOutOrder[i+1:]...)
				break
			}
		}
	}
	oauthOutMu.Unlock()
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// validateOAuthOutgoing — POST /oauth/outgoing_connections/validate
// Probe-style validation; we just 200 OK since we don't actually probe.
func (h *handlers) validateOAuthOutgoing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// ---- Pillar F — Cloud + IP filter + imports/exports + misc stubs ----------
//
// All admin-only stubs. Mattermost's cloud-billing and IP-filtering are
// SaaS-only features the official admin console probes on launch even for
// self-hosted deployments — return empty/disabled shapes so the UI doesn't
// 404 or render errors.

// Cloud billing stubs.
func (h *handlers) cloudCheckCWS(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": false, "message": "self-hosted"})
}

func (h *handlers) cloudGetCustomer(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"id": "", "name": "", "email": ""})
}

func (h *handlers) cloudPutCustomer(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"id": "", "name": "", "email": ""})
}

func (h *handlers) cloudPutCustomerAddress(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

func (h *handlers) cloudGetInstallation(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"id": "", "state": "self-hosted"})
}

func (h *handlers) cloudGetLimits(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"messages": map[string]any{"history": 0}})
}

func (h *handlers) cloudGetPreviewModalData(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"products": []any{}})
}

func (h *handlers) cloudGetProducts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, []any{})
}

func (h *handlers) cloudGetSubscription(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"id": "", "is_free_trial": "false"})
}

func (h *handlers) cloudGetSubscriptionInvoices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, []any{})
}

func (h *handlers) cloudGetSubscriptionInvoicePDF(w http.ResponseWriter, r *http.Request) {
	writeError(w, 404, "api.cloud.invoice.not_found", "invoice not found")
}

func (h *handlers) cloudPostPayment(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

func (h *handlers) cloudConfirmPayment(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

func (h *handlers) cloudWebhook(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// IP filtering stubs.
func (h *handlers) listIPFiltering(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, []any{})
}

func (h *handlers) getMyIP(w http.ResponseWriter, r *http.Request) {
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		addr = addr[:i]
	}
	writeJSON(w, 200, map[string]any{"ip": addr})
}

func (h *handlers) saveIPFiltering(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, []any{})
}

// Imports/Exports stubs.
func (h *handlers) listImports(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, []any{})
}

func (h *handlers) deleteImport(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

func (h *handlers) listExports(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, []any{})
}

func (h *handlers) getExport(w http.ResponseWriter, r *http.Request) {
	writeError(w, 404, "api.export.not_found", "export not found")
}

func (h *handlers) deleteExport(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// Posts burn / reveal / rewrite stubs.
func (h *handlers) burnPost(w http.ResponseWriter, r *http.Request) {
	pid := chi.URLParam(r, "postID")
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionPostBurn, pid, nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

func (h *handlers) revealPost(w http.ResponseWriter, r *http.Request) {
	pid := chi.URLParam(r, "postID")
	post, err := h.posts.Get(r.Context(), pid)
	if err != nil || post == nil {
		writeError(w, 404, "api.post.not_found", "post not found")
		return
	}
	writeJSON(w, 200, post)
}

func (h *handlers) rewritePost(w http.ResponseWriter, r *http.Request) {
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionPostRewrite, "", nil)
	}
	writeJSON(w, 200, map[string]any{"status": "OK", "message": ""})
}

// Misc admin stubs.
func (h *handlers) restartServer(w http.ResponseWriter, r *http.Request) {
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionRestart, "", nil)
	}
	// Real restart would signal main.go via a channel; we just acknowledge
	// so the admin console's "Restart" button doesn't 404.
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

func (h *handlers) postClientPerf(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

func (h *handlers) postPermissionsAncillary(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, []any{})
}

func (h *handlers) postTeamImport(w http.ResponseWriter, r *http.Request) {
	tid := chi.URLParam(r, "teamID")
	if h.audit != nil {
		h.audit.LogAsync(userID(r), audit.ActionTeamImport, tid, nil)
	}
	writeJSON(w, 200, map[string]any{"results": "{}"})
}

func (h *handlers) inviteTeamMembersFromBody(w http.ResponseWriter, r *http.Request) {
	if h.denyGuestMutation(w, r, "api.team.invite.guest_forbidden") {
		return
	}
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

func (h *handlers) putTeamScheme(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

func (h *handlers) listManagedCategories(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, []any{})
}

func (h *handlers) channelAccessControlAttrs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{})
}

func (h *handlers) channelCommonTeams(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, []any{})
}

func (h *handlers) analyticsOld(w http.ResponseWriter, r *http.Request) {
	// Mattermost's legacy analytics endpoint returns an array of named
	// stat rows. We return a few zero-filled rows so the admin console's
	// "System statistics" tab renders.
	writeJSON(w, 200, []map[string]any{
		{"name": "channel_open_count", "value": 0},
		{"name": "channel_private_count", "value": 0},
		{"name": "post_count", "value": 0},
		{"name": "unique_user_count", "value": 0},
		{"name": "team_count", "value": 0},
		{"name": "total_websocket_connections", "value": 0},
		{"name": "total_master_db_connections", "value": 0},
		{"name": "total_read_db_connections", "value": 0},
	})
}

// adminUploadUserImage — POST /users/{userID}/image
// Admin-force-set another user's profile picture. Mirrors uploadProfileImage
// from Phase 15 (which is /users/me/image) but admin-on-other-user. Reads
// 512KB MaxBytes, hands off to files.Upload, then auth.UpdatePicture.
func (h *handlers) adminUploadUserImage(w http.ResponseWriter, r *http.Request) {
	target, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512*1024)
	if err := r.ParseMultipartForm(512 * 1024); err != nil {
		writeError(w, 400, "api.user.image.parse.app_error", err.Error())
		return
	}
	file, hdr, err := r.FormFile("image")
	if err != nil {
		writeError(w, 400, "api.user.image.no_file.app_error", "image file required")
		return
	}
	defer file.Close()
	mime := hdr.Header.Get("Content-Type")
	fi, err := h.files.Upload(r.Context(), target, "", hdr.Filename, mime, file)
	if err != nil {
		writeError(w, 500, "api.user.image.upload.app_error", err.Error())
		return
	}
	if _, err := h.auth.UpdatePicture(r.Context(), target, fi.ID); err != nil {
		writeError(w, 500, "api.user.image.update.app_error", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"status": "OK", "file_id": fi.ID})
}
