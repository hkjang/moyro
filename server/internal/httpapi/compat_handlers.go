package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/moddle/moddle/server/internal/ws"
)

func (h *handlers) listTeamsForUserID(w http.ResponseWriter, r *http.Request, targetID string) {
	list, err := h.teams.ListForUser(r.Context(), targetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.team.list.app_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *handlers) listTeamsForUserParam(w http.ResponseWriter, r *http.Request) {
	targetID, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	h.listTeamsForUserID(w, r, targetID)
}

func (h *handlers) getTeam(w http.ResponseWriter, r *http.Request) {
	teamID := chi.URLParam(r, "teamID")
	t, err := h.teams.Get(r.Context(), teamID)
	if err != nil || t == nil {
		writeError(w, http.StatusNotFound, "api.team.get.find.app_error", "team not found")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *handlers) canViewTeam(r *http.Request, teamID string) bool {
	actorID := userID(r)
	isMember, _ := h.teams.IsMember(r.Context(), teamID, actorID)
	if isMember {
		return true
	}
	isAdmin, _ := h.auth.HasRole(r.Context(), actorID, "system_admin")
	return isAdmin
}

func (h *handlers) getTeamMember(w http.ResponseWriter, r *http.Request) {
	teamID := chi.URLParam(r, "teamID")
	if !h.canViewTeam(r, teamID) {
		writeError(w, http.StatusForbidden, "api.team.member.get.forbidden", "not a team member")
		return
	}
	m, err := h.teams.GetMember(r.Context(), teamID, chi.URLParam(r, "userID"))
	if err != nil {
		writeError(w, http.StatusNotFound, "api.team.member.get.not_found", "member not found")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (h *handlers) listUserTeamMembers(w http.ResponseWriter, r *http.Request) {
	targetID, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	list, err := h.teams.ListMembershipsForUser(r.Context(), targetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.team.members.user.app_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *handlers) teamMembersByIDs(w http.ResponseWriter, r *http.Request) {
	teamID := chi.URLParam(r, "teamID")
	if !h.canViewTeam(r, teamID) {
		writeError(w, http.StatusForbidden, "api.team.members.ids.forbidden", "not a team member")
		return
	}
	var ids []string
	if err := json.NewDecoder(r.Body).Decode(&ids); err != nil {
		writeError(w, http.StatusBadRequest, "api.team.members.ids.invalid_body", err.Error())
		return
	}
	if len(ids) > 200 {
		ids = ids[:200]
	}
	list, err := h.teams.MembersByIDs(r.Context(), teamID, ids)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.team.members.ids.app_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *handlers) addTeamMember(w http.ResponseWriter, r *http.Request) {
	teamID := chi.URLParam(r, "teamID")
	var req struct {
		TeamID string `json:"team_id"`
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "api.team.member.add.invalid_body", err.Error())
		return
	}
	if req.UserID == "" {
		writeError(w, http.StatusBadRequest, "api.team.member.add.missing_user", "user_id required")
		return
	}
	if req.TeamID != "" && req.TeamID != teamID {
		writeError(w, http.StatusBadRequest, "api.team.member.add.team_mismatch", "team_id does not match route")
		return
	}
	actorID := userID(r)
	if req.UserID != actorID && !h.canManageTeamInvites(r.Context(), actorID, teamID) {
		writeError(w, http.StatusForbidden, "api.team.member.add.forbidden", "admin privilege required")
		return
	}
	m, err := h.teams.AddMember(r.Context(), teamID, req.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.team.member.add.app_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (h *handlers) addTeamMembersBatch(w http.ResponseWriter, r *http.Request) {
	teamID := chi.URLParam(r, "teamID")
	actorID := userID(r)
	if !h.canManageTeamInvites(r.Context(), actorID, teamID) {
		writeError(w, http.StatusForbidden, "api.team.members.batch.forbidden", "admin privilege required")
		return
	}
	var req []struct {
		UserID string `json:"user_id"`
		TeamID string `json:"team_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "api.team.members.batch.invalid_body", err.Error())
		return
	}
	out := []any{}
	for _, item := range req {
		if item.UserID == "" || (item.TeamID != "" && item.TeamID != teamID) {
			continue
		}
		m, err := h.teams.AddMember(r.Context(), teamID, item.UserID)
		if err != nil {
			if r.URL.Query().Get("graceful") == "true" {
				out = append(out, map[string]any{"user_id": item.UserID, "error": err.Error()})
				continue
			}
			writeError(w, http.StatusInternalServerError, "api.team.members.batch.app_error", err.Error())
			return
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *handlers) listChannelsForUserID(w http.ResponseWriter, r *http.Request, targetID string) {
	teamID := chi.URLParam(r, "teamID")
	includeDeleted := r.URL.Query().Get("include_deleted") == "true"
	var (
		list any
		err  error
	)
	if teamID == "" {
		list, err = h.channels.ListAllForUserIncludingDeleted(r.Context(), targetID, includeDeleted)
	} else {
		list, err = h.channels.ListForUserIncludingDeleted(r.Context(), targetID, teamID, includeDeleted)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.channel.list.app_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *handlers) listChannelsForUserParam(w http.ResponseWriter, r *http.Request) {
	targetID, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	h.listChannelsForUserID(w, r, targetID)
}

func (h *handlers) listChannelMembersForUserID(w http.ResponseWriter, r *http.Request, targetID string) {
	teamID := chi.URLParam(r, "teamID")
	list, err := h.channels.ListForUserWithCounts(r.Context(), targetID, teamID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.channel.members.me.app_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *handlers) listChannelMembersForUserParam(w http.ResponseWriter, r *http.Request) {
	targetID, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	h.listChannelMembersForUserID(w, r, targetID)
}

func (h *handlers) getChannelMember(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channelID")
	targetID := chi.URLParam(r, "targetUserID")
	actorID := userID(r)
	isMember, err := h.channels.IsMember(r.Context(), channelID, actorID)
	if err != nil || !isMember {
		writeError(w, http.StatusForbidden, "api.channel.members.get.forbidden", "not a channel member")
		return
	}
	list, err := h.channels.ListMembers(r.Context(), channelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.channel.members.get.app_error", err.Error())
		return
	}
	for _, member := range list {
		if member.UserID == targetID {
			writeJSON(w, http.StatusOK, member)
			return
		}
	}
	writeError(w, http.StatusNotFound, "api.channel.members.get.not_found", "member not found")
}

func (h *handlers) listUserTeamsUnread(w http.ResponseWriter, r *http.Request) {
	targetID, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	list, err := h.teams.ListUnreadForUser(r.Context(), targetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.team.unread.list.app_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *handlers) getUserTeamUnread(w http.ResponseWriter, r *http.Request) {
	targetID, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	teamID := chi.URLParam(r, "teamID")
	if targetID != userID(r) && !h.canViewTeam(r, teamID) {
		writeError(w, http.StatusForbidden, "api.team.unread.get.forbidden", "not a team member")
		return
	}
	unread, err := h.teams.GetUnread(r.Context(), targetID, teamID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.team.unread.get.app_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, unread)
}

func (h *handlers) getChannelUnread(w http.ResponseWriter, r *http.Request) {
	targetID, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	channelID := chi.URLParam(r, "channelID")
	if targetID != userID(r) {
		isMember, _ := h.channels.IsMember(r.Context(), channelID, userID(r))
		if !isMember {
			writeError(w, http.StatusForbidden, "api.channel.unread.get.forbidden", "not a channel member")
			return
		}
	}
	unread, err := h.channels.GetUnread(r.Context(), targetID, channelID)
	if err != nil {
		writeError(w, http.StatusNotFound, "api.channel.unread.get.not_found", "channel member not found")
		return
	}
	writeJSON(w, http.StatusOK, unread)
}

func (h *handlers) viewChannelForUser(w http.ResponseWriter, r *http.Request) {
	targetID, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	var req struct {
		ChannelID     string `json:"channel_id"`
		PrevChannelID string `json:"prev_channel_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "api.channel.view.invalid_body", err.Error())
		return
	}
	if req.ChannelID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "OK", "last_viewed_at_times": map[string]int64{}})
		return
	}
	isMember, err := h.channels.IsMember(r.Context(), req.ChannelID, targetID)
	if err != nil || !isMember {
		writeError(w, http.StatusForbidden, "api.channel.view.forbidden", "not a channel member")
		return
	}
	ts, err := h.channels.MarkViewed(r.Context(), req.ChannelID, targetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.channel.view.app_error", err.Error())
		return
	}
	h.hub.Broadcast(ws.Event{
		Event: "channel_viewed",
		Data: map[string]any{
			"channel_id":     req.ChannelID,
			"last_viewed_at": ts,
		},
		Broadcast: ws.Broadcast{UserID: targetID},
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"status":               "OK",
		"last_viewed_at_times": map[string]int64{req.ChannelID: ts},
	})
}

func (h *handlers) getPost(w http.ResponseWriter, r *http.Request) {
	postID := chi.URLParam(r, "postID")
	p, err := h.posts.Get(r.Context(), postID)
	if err != nil || p == nil || p.DeleteAt != 0 {
		writeError(w, http.StatusNotFound, "api.post.get.app_error", "post not found")
		return
	}
	isMember, err := h.channels.IsMember(r.Context(), p.ChannelID, userID(r))
	if err != nil || !isMember {
		writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "not a channel member")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *handlers) writeSessionsForUser(w http.ResponseWriter, r *http.Request, targetID string) {
	sessions, err := h.auth.ListSessions(r.Context(), targetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.session.list.app_error", err.Error())
		return
	}
	current := extractBearer(r)
	out := make([]sessionView, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, sessionView{
			ID:        s.ID,
			UserID:    s.UserID,
			DeviceID:  s.DeviceID,
			ExpiresAt: s.ExpiresAt,
			CreateAt:  s.CreateAt,
			IsCurrent: s.UserID == userID(r) && s.Token == current,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) listUserSessions(w http.ResponseWriter, r *http.Request) {
	targetID, ok := h.requireUserParamAccess(w, r, "userID")
	if !ok {
		return
	}
	h.writeSessionsForUser(w, r, targetID)
}
