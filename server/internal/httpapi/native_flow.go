package httpapi

import (
	"net/http"
	"sort"
	"time"

	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/teams"
)

type nativeFlowCounts struct {
	UnreadChannels int64 `json:"unread_channels"`
	Mentions       int64 `json:"mentions"`
}

type nativeFlowUnreadChannel struct {
	TeamID       string `json:"team_id"`
	ChannelID    string `json:"channel_id"`
	MsgCount     int64  `json:"msg_count"`
	MentionCount int64  `json:"mention_count"`
	LastViewedAt int64  `json:"last_viewed_at"`
}

type nativeFlowSummary struct {
	UpdatedAt         int64                       `json:"updated_at"`
	Counts            nativeFlowCounts            `json:"counts"`
	Teams             []teams.Team                `json:"teams"`
	Channels          []channels.Channel          `json:"channels"`
	Memberships       []channels.MemberWithCounts `json:"memberships"`
	TopUnreadChannels []nativeFlowUnreadChannel   `json:"top_unread_channels"`
}

// buildNativeFlowSummary keeps the global Flow index constrained to the
// intersection of team membership and channel membership. Direct/group
// messages do not have a team in the current Flow UI and stay on the
// workspace surface until that UI gains an explicit DM entry model.
func buildNativeFlowSummary(
	now int64,
	principal rbac.Principal,
	teamRows []teams.Team,
	channelRows []channels.Channel,
	membershipRows []channels.MemberWithCounts,
) nativeFlowSummary {
	constraints := rbac.ResourceConstraintsFor(principal)
	teamIDs := make(map[string]struct{}, len(teamRows))
	for _, team := range teamRows {
		if !constraints.AllowsTeam(team.ID) {
			continue
		}
		teamIDs[team.ID] = struct{}{}
	}

	visibleChannels := make([]channels.Channel, 0, len(channelRows))
	visibleChannelIDs := make(map[string]channels.Channel, len(channelRows))
	visibleTeamIDs := make(map[string]struct{}, len(teamIDs))
	for _, channel := range channelRows {
		if channel.DeleteAt != 0 || channel.TeamID == "" {
			continue
		}
		if _, ok := teamIDs[channel.TeamID]; !ok {
			continue
		}
		if !constraints.AllowsChannel(channel.ID) {
			continue
		}
		visibleChannels = append(visibleChannels, channel)
		visibleChannelIDs[channel.ID] = channel
		visibleTeamIDs[channel.TeamID] = struct{}{}
	}
	visibleTeams := make([]teams.Team, 0, len(teamRows))
	for _, team := range teamRows {
		if _, ok := teamIDs[team.ID]; !ok {
			continue
		}
		// A channel-constrained credential must not learn the names of the
		// caller's unrelated teams merely because it belongs to the same user.
		if len(constraints.ChannelIDs) > 0 {
			if _, ok := visibleTeamIDs[team.ID]; !ok {
				continue
			}
		}
		visibleTeams = append(visibleTeams, team)
	}

	visibleMemberships := make([]channels.MemberWithCounts, 0, len(membershipRows))
	unread := make([]nativeFlowUnreadChannel, 0)
	var counts nativeFlowCounts
	for _, membership := range membershipRows {
		channel, ok := visibleChannelIDs[membership.ChannelID]
		if !ok {
			continue
		}
		visibleMemberships = append(visibleMemberships, membership)
		if membership.MsgCount <= 0 && membership.MentionCount <= 0 {
			continue
		}
		counts.UnreadChannels++
		counts.Mentions += membership.MentionCount
		unread = append(unread, nativeFlowUnreadChannel{
			TeamID:       channel.TeamID,
			ChannelID:    channel.ID,
			MsgCount:     membership.MsgCount,
			MentionCount: membership.MentionCount,
			LastViewedAt: membership.LastViewedAt,
		})
	}

	sort.SliceStable(unread, func(i, j int) bool {
		if unread[i].MentionCount != unread[j].MentionCount {
			return unread[i].MentionCount > unread[j].MentionCount
		}
		if unread[i].MsgCount != unread[j].MsgCount {
			return unread[i].MsgCount > unread[j].MsgCount
		}
		return unread[i].ChannelID < unread[j].ChannelID
	})
	if len(unread) > 6 {
		unread = unread[:6]
	}

	return nativeFlowSummary{
		UpdatedAt:         now,
		Counts:            counts,
		Teams:             visibleTeams,
		Channels:          visibleChannels,
		Memberships:       visibleMemberships,
		TopUnreadChannels: unread,
	}
}

func (h *handlers) getNativeFlowSummary(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "authentication required")
		return
	}
	// Browser sessions are already bounded by their user and channel
	// memberships. Scoped API keys additionally need an explicit read grant;
	// otherwise a key created solely for AI or approvals could enumerate the
	// owner's team and private-channel metadata through this convenience API.
	if principal.Restricted {
		if _, granted := principal.GrantedPermissions[rbac.PermissionMCPRead]; !granted {
			writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "missing permission: "+rbac.PermissionMCPRead)
			return
		}
		if h.native == nil || h.native.rbac == nil {
			writeError(w, http.StatusServiceUnavailable, "api.moyro.disabled", "moyro management services are unavailable")
			return
		}
		allowed, err := h.native.rbac.Allowed(r.Context(), principal, rbac.PermissionMCPRead, rbac.Scope{})
		if err != nil {
			h.logger.Error("flow summary authorization failed", "actor_id", principal.UserID, "err", err)
			writeError(w, http.StatusInternalServerError, "api.moyro.flow_summary.authorization", "flow summary authorization failed")
			return
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "missing permission: "+rbac.PermissionMCPRead)
			return
		}
	}

	actorID := principal.UserID
	teamRows, err := h.teams.ListForUser(r.Context(), actorID)
	if err != nil {
		h.logger.Error("flow summary team query failed", "actor_id", actorID, "err", err)
		writeError(w, http.StatusInternalServerError, "api.moyro.flow_summary.app_error", "flow summary is unavailable")
		return
	}
	channelRows, err := h.channels.ListAllForUserIncludingDeleted(r.Context(), actorID, false)
	if err != nil {
		h.logger.Error("flow summary channel query failed", "actor_id", actorID, "err", err)
		writeError(w, http.StatusInternalServerError, "api.moyro.flow_summary.app_error", "flow summary is unavailable")
		return
	}
	membershipRows, err := h.channels.ListAllForUserWithCounts(r.Context(), actorID)
	if err != nil {
		h.logger.Error("flow summary membership query failed", "actor_id", actorID, "err", err)
		writeError(w, http.StatusInternalServerError, "api.moyro.flow_summary.app_error", "flow summary is unavailable")
		return
	}

	writeJSON(w, http.StatusOK, buildNativeFlowSummary(
		time.Now().UnixMilli(),
		principal,
		teamRows,
		channelRows,
		membershipRows,
	))
}
