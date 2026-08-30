package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/teams"
)

func TestGetNativeFlowSummaryRejectsAnonymousCaching(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/moyro/v1/me/flow-summary", nil)
	(&handlers{}).getNativeFlowSummary(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q, want private, no-store", got)
	}
}

func TestGetNativeFlowSummaryRejectsRestrictedCredentialWithoutReadGrant(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/moyro/v1/me/flow-summary", nil)
	request = request.WithContext(setPrincipalOnContext(request.Context(), rbac.Principal{
		UserID:             "user-1",
		CredentialID:       "key-1",
		Restricted:         true,
		GrantedPermissions: map[string]struct{}{rbac.PermissionUseAI: {}},
	}))
	(&handlers{}).getNativeFlowSummary(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("restricted status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestBuildNativeFlowSummaryScopesAndRanksUnreadChannels(t *testing.T) {
	teamRows := []teams.Team{{ID: "team-1", DisplayName: "운영"}}
	channelRows := []channels.Channel{
		{ID: "channel-1", TeamID: "team-1", DisplayName: "공지"},
		{ID: "channel-2", TeamID: "team-1", DisplayName: "장애"},
		{ID: "dm-1", Type: "D", DisplayName: "direct"},
		{ID: "foreign", TeamID: "team-2", DisplayName: "other"},
		{ID: "deleted", TeamID: "team-1", DeleteAt: 10},
	}
	membershipRows := []channels.MemberWithCounts{
		{ChannelID: "channel-1", UserID: "user-1", MsgCount: 8, MentionCount: 1},
		{ChannelID: "channel-2", UserID: "user-1", MsgCount: 2, MentionCount: 3},
		{ChannelID: "dm-1", UserID: "user-1", MsgCount: 10, MentionCount: 10},
		{ChannelID: "foreign", UserID: "user-1", MsgCount: 10, MentionCount: 10},
		{ChannelID: "deleted", UserID: "user-1", MsgCount: 10, MentionCount: 10},
	}

	summary := buildNativeFlowSummary(1234, rbac.UserPrincipal("user-1"), teamRows, channelRows, membershipRows)
	if summary.UpdatedAt != 1234 || len(summary.Teams) != 1 {
		t.Fatalf("summary metadata = %#v", summary)
	}
	if len(summary.Channels) != 2 || len(summary.Memberships) != 2 {
		t.Fatalf("visible rows = channels:%d memberships:%d", len(summary.Channels), len(summary.Memberships))
	}
	if summary.Counts.UnreadChannels != 2 || summary.Counts.Mentions != 4 {
		t.Fatalf("counts = %#v", summary.Counts)
	}
	if len(summary.TopUnreadChannels) != 2 || summary.TopUnreadChannels[0].ChannelID != "channel-2" {
		t.Fatalf("ranked unread = %#v", summary.TopUnreadChannels)
	}
}

func TestBuildNativeFlowSummaryCapsPriorityItems(t *testing.T) {
	teamRows := []teams.Team{{ID: "team-1"}}
	channelRows := make([]channels.Channel, 0, 8)
	membershipRows := make([]channels.MemberWithCounts, 0, 8)
	for i := 0; i < 8; i++ {
		id := string(rune('a' + i))
		channelRows = append(channelRows, channels.Channel{ID: id, TeamID: "team-1"})
		membershipRows = append(membershipRows, channels.MemberWithCounts{
			ChannelID: id,
			MsgCount:  int64(i + 1),
		})
	}

	summary := buildNativeFlowSummary(1, rbac.UserPrincipal("user-1"), teamRows, channelRows, membershipRows)
	if len(summary.TopUnreadChannels) != 6 {
		t.Fatalf("top unread length = %d, want 6", len(summary.TopUnreadChannels))
	}
	if summary.TopUnreadChannels[0].ChannelID != "h" || summary.TopUnreadChannels[5].ChannelID != "c" {
		t.Fatalf("top unread ordering = %#v", summary.TopUnreadChannels)
	}
}

func TestBuildNativeFlowSummaryAppliesCredentialResourceIntersection(t *testing.T) {
	teamRows := []teams.Team{
		{ID: "team-1", DisplayName: "allowed team"},
		{ID: "team-2", DisplayName: "unrelated team"},
	}
	channelRows := []channels.Channel{
		{ID: "channel-1", TeamID: "team-1", DisplayName: "allowed channel"},
		{ID: "channel-2", TeamID: "team-1", DisplayName: "same team but disallowed"},
		{ID: "channel-3", TeamID: "team-2", DisplayName: "different team"},
	}
	membershipRows := []channels.MemberWithCounts{
		{ChannelID: "channel-1", UserID: "user-1", MsgCount: 2},
		{ChannelID: "channel-2", UserID: "user-1", MsgCount: 4},
		{ChannelID: "channel-3", UserID: "user-1", MsgCount: 8},
	}
	principal := rbac.Principal{
		UserID:            "user-1",
		Restricted:        true,
		AllowedTeamIDs:    map[string]struct{}{"team-1": {}},
		AllowedChannelIDs: map[string]struct{}{"channel-1": {}},
	}

	summary := buildNativeFlowSummary(1, principal, teamRows, channelRows, membershipRows)
	if len(summary.Teams) != 1 || summary.Teams[0].ID != "team-1" {
		t.Fatalf("visible teams = %#v", summary.Teams)
	}
	if len(summary.Channels) != 1 || summary.Channels[0].ID != "channel-1" {
		t.Fatalf("visible channels = %#v", summary.Channels)
	}
	if len(summary.Memberships) != 1 || summary.Memberships[0].ChannelID != "channel-1" {
		t.Fatalf("visible memberships = %#v", summary.Memberships)
	}
	if summary.Counts.UnreadChannels != 1 || len(summary.TopUnreadChannels) != 1 {
		t.Fatalf("visible counts = %#v, top = %#v", summary.Counts, summary.TopUnreadChannels)
	}
}

func TestBuildNativeFlowSummaryHidesUnrelatedTeamsForChannelOnlyCredential(t *testing.T) {
	principal := rbac.Principal{
		UserID:            "user-1",
		Restricted:        true,
		AllowedChannelIDs: map[string]struct{}{"channel-1": {}},
	}
	summary := buildNativeFlowSummary(
		1,
		principal,
		[]teams.Team{{ID: "team-1"}, {ID: "team-2"}},
		[]channels.Channel{{ID: "channel-1", TeamID: "team-1"}, {ID: "channel-2", TeamID: "team-2"}},
		[]channels.MemberWithCounts{{ChannelID: "channel-1"}, {ChannelID: "channel-2"}},
	)
	if len(summary.Teams) != 1 || summary.Teams[0].ID != "team-1" {
		t.Fatalf("channel-constrained teams = %#v", summary.Teams)
	}
}
