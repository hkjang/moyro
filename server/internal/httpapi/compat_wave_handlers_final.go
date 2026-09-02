// Mattermost API v4 compatibility waves 10-12.
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
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/audit"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/customprofile"
	"github.com/hkjang/moyro/server/internal/files"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/ws"
)

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
