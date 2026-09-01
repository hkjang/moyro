package httpapi

import (
	"net/http"
)

type oidcOnboardingChannelTarget struct {
	ID          string `json:"id"`
	TeamID      string `json:"team_id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
}

type oidcOnboardingTeamTarget struct {
	ID          string                        `json:"id"`
	Name        string                        `json:"name"`
	DisplayName string                        `json:"display_name"`
	Channels    []oidcOnboardingChannelTarget `json:"channels"`
}

// listNativeOIDCOnboardingTargets supplies the existing Keycloak settings
// screen with active, non-DM destinations. It deliberately omits archived and
// direct/group channels so a saved mapping cannot bypass the collaboration
// scope validator.
func (h *handlers) listNativeOIDCOnboardingTargets(w http.ResponseWriter, r *http.Request) {
	rows, err := h.auth.DB().Pool.Query(r.Context(), `
		SELECT t.id, t.name, t.display_name, c.id, c.name, c.display_name, c.type
		FROM teams t
		LEFT JOIN channels c ON c.team_id=t.id AND c.delete_at=0 AND c.type IN ('O','P')
		WHERE t.delete_at=0
		ORDER BY LOWER(t.display_name), t.id, LOWER(c.display_name), c.id
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.oidc.targets", err.Error())
		return
	}
	defer rows.Close()

	teams := make([]oidcOnboardingTeamTarget, 0)
	indexes := map[string]int{}
	for rows.Next() {
		var team oidcOnboardingTeamTarget
		var channelID, channelName, channelDisplayName, channelType *string
		if err := rows.Scan(&team.ID, &team.Name, &team.DisplayName, &channelID, &channelName, &channelDisplayName, &channelType); err != nil {
			writeError(w, http.StatusInternalServerError, "api.moyro.oidc.targets", err.Error())
			return
		}
		index, exists := indexes[team.ID]
		if !exists {
			team.Channels = []oidcOnboardingChannelTarget{}
			index = len(teams)
			indexes[team.ID] = index
			teams = append(teams, team)
		}
		if channelID != nil {
			teams[index].Channels = append(teams[index].Channels, oidcOnboardingChannelTarget{
				ID: *channelID, TeamID: team.ID, Name: *channelName,
				DisplayName: *channelDisplayName, Type: *channelType,
			})
		}
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.oidc.targets", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"teams": teams})
}
