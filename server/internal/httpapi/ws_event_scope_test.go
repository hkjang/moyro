package httpapi

import "testing"

func TestSecuritySensitiveWebSocketEventsCarryAudienceScope(t *testing.T) {
	team := teamScopedEvent("team_updated", "team-1", map[string]any{"team": "payload"})
	if team.Broadcast.TeamID != "team-1" || team.Broadcast.ChannelID != "" || team.Broadcast.UserID != "" {
		t.Fatalf("team event broadcast = %#v", team.Broadcast)
	}

	channel := channelScopedEvent("channel_member_updated", "channel-1", map[string]any{"user_id": "user-1"})
	if channel.Broadcast.ChannelID != "channel-1" || channel.Broadcast.TeamID != "" || channel.Broadcast.UserID != "" {
		t.Fatalf("channel event broadcast = %#v", channel.Broadcast)
	}
}
