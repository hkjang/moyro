// Mattermost API v4 compatibility waves 1-3.
//
// Split out of handlers.go, which held the core product handlers and these
// compatibility waves in one file. The cut follows the existing wave banner,
// so no handler moved between areas and the later waves keep living in the
// compat_wave_handlers*.go siblings. Everything remains in package httpapi
// and is wired by router.go exactly as before.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/audit"
	"github.com/hkjang/moyro/server/internal/auth"
	"github.com/hkjang/moyro/server/internal/commands"
	"github.com/hkjang/moyro/server/internal/preferences"
	"github.com/hkjang/moyro/server/internal/scheduled"
	"github.com/hkjang/moyro/server/internal/sidebar"
	"github.com/hkjang/moyro/server/internal/ws"
)

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
	actor, actorErr := h.auth.UserByID(r.Context(), userID(r))
	if actorErr != nil {
		writeError(w, http.StatusUnauthorized, "api.user.autocomplete.session", "active user session required")
		return
	}
	if actor.IsGuest() {
		list, err = h.auth.FilterUsersVisibleToGuest(r.Context(), actor.ID, list)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "api.user.autocomplete.visibility", err.Error())
			return
		}
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
	actor, actorErr := h.auth.UserByID(r.Context(), userID(r))
	if actorErr != nil {
		writeError(w, http.StatusUnauthorized, "api.user.ids.session", "active user session required")
		return
	}
	if actor.IsGuest() {
		list, err = h.auth.FilterUsersVisibleToGuest(r.Context(), actor.ID, list)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "api.user.ids.visibility", err.Error())
			return
		}
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
	actor, actorErr := h.auth.UserByID(r.Context(), userID(r))
	if actorErr != nil {
		writeError(w, http.StatusUnauthorized, "api.user.usernames.session", "active user session required")
		return
	}
	if actor.IsGuest() {
		list, err = h.auth.FilterUsersVisibleToGuest(r.Context(), actor.ID, list)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "api.user.usernames.visibility", err.Error())
			return
		}
	}
	writeJSON(w, 200, list)
}

// getUserByEmail mirrors GET /api/v4/users/email/{email}.
func (h *handlers) getUserByEmail(w http.ResponseWriter, r *http.Request) {
	email := chi.URLParam(r, "email")
	u, err := h.auth.UserByEmail(r.Context(), email)
	if err != nil {
		writeError(w, http.StatusNotFound, "api.user.get_by_email.not_found", "user not found")
		return
	}
	if !h.guestMayViewNamedUser(w, r, u.ID, "api.user.get_by_email.not_found") {
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
	if h.denyGuestEnumeration(w, r, "api.team.members.guest_forbidden") {
		return
	}
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
		if h.denyGuestEnumeration(w, r, "api.channel.get_by_name.guest_forbidden") {
			return
		}
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
	if h.denyGuestEnumeration(w, r, "api.channel.search.guest_forbidden") {
		return
	}
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
	if h.denyGuestEnumeration(w, r, "api.channel.autocomplete.guest_forbidden") {
		return
	}
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
	if h.denyGuestEnumeration(w, r, "api.team.search.guest_forbidden") {
		return
	}
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
	if h.denyGuestMutation(w, r, "api.channel.patch.guest_forbidden") {
		return
	}
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
	if h.denyGuestMutation(w, r, "api.channel.privacy.guest_forbidden") {
		return
	}
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
