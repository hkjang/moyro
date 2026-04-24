package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
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

func (h *handlers) listChannelsForUserID(w http.ResponseWriter, r *http.Request, targetID string) {
	teamID := chi.URLParam(r, "teamID")
	includeDeleted := r.URL.Query().Get("include_deleted") == "true"
	list, err := h.channels.ListForUserIncludingDeleted(r.Context(), targetID, teamID, includeDeleted)
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
