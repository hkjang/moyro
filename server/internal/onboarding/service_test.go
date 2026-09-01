package onboarding

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeMappingsDefaultsAndRejectsUnsafeGuest(t *testing.T) {
	mappings, err := normalizeMappings([]GroupMapping{{
		Group: " /Engineering ", TeamID: " team-a ", ChannelIDs: []string{" channel-b ", "channel-a", "channel-a"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := mappings[0]
	if got.Group != "/Engineering" || got.AccountRole != AccountRoleUser || got.TeamRole != MembershipRoleMember ||
		got.ChannelRole != MembershipRoleMember || strings.Join(got.ChannelIDs, ",") != "channel-a,channel-b" {
		t.Fatalf("unexpected normalized mapping: %#v", got)
	}

	_, err = normalizeMappings([]GroupMapping{{Group: "partners", AccountRole: AccountRoleGuest, TeamID: "team-a"}})
	if err == nil || !strings.Contains(err.Error(), "restricted channels") {
		t.Fatalf("unsafe guest mapping error = %v", err)
	}

	for _, mapping := range []GroupMapping{
		{Group: "partners", AccountRole: AccountRoleGuest, TeamID: "team-a", TeamRole: MembershipRoleAdmin, ChannelIDs: []string{"channel-a"}},
		{Group: "partners", AccountRole: AccountRoleGuest, TeamID: "team-a", ChannelIDs: []string{"channel-a"}, ChannelRole: MembershipRoleAdmin},
	} {
		if _, err := normalizeMappings([]GroupMapping{mapping}); err == nil || !strings.Contains(err.Error(), "admin") {
			t.Fatalf("guest admin mapping error = %v", err)
		}
	}
}

func TestBuildPlanIsCaseInsensitiveAdditiveAndChoosesHigherRole(t *testing.T) {
	mappings, err := normalizeMappings([]GroupMapping{
		{Group: "Engineering", TeamID: "team-a", ChannelIDs: []string{"channel-a"}},
		{Group: "LEADS", AccountRole: AccountRoleAdmin, TeamID: "team-a", TeamRole: MembershipRoleAdmin, ChannelIDs: []string{"channel-a"}, ChannelRole: MembershipRoleAdmin},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := buildPlan([]string{" engineering ", "leads", "unmapped"}, mappings)
	if !plan.matched || plan.accountRole != AccountRoleAdmin || plan.teams["team-a"] != MembershipRoleAdmin || plan.channels["channel-a"] != MembershipRoleAdmin {
		t.Fatalf("unexpected plan: %#v", plan)
	}
}

func TestAccountRoleOnlyPlanDoesNotSuppressDefaultCollaborationBootstrap(t *testing.T) {
	mappings, err := normalizeMappings([]GroupMapping{{Group: "platform-admins", AccountRole: AccountRoleAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	plan := buildPlan([]string{"platform-admins"}, mappings)
	if !plan.matched || plan.accountRole != AccountRoleAdmin {
		t.Fatalf("account role plan = %#v", plan)
	}
	if plan.hasCollaborationTarget() {
		t.Fatal("account-role-only mapping must not count as a team/channel target")
	}
}

func TestMergeAccountAccessNeverDemotesExistingRegularUser(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	guestPlan := accessPlan{
		matched: true, accountRole: AccountRoleGuest, teams: map[string]string{}, channels: map[string]string{},
		guestTTLSeconds: int64((24 * time.Hour) / time.Second),
	}
	roles, expiry, download := mergeAccountAccess("system_admin system_user custom", 0, true, guestPlan, false, now)
	if !strings.Contains(roles, "system_admin") || !strings.Contains(roles, "system_user") || expiry != 0 || !download {
		t.Fatalf("existing regular access was demoted: roles=%q expiry=%d download=%v", roles, expiry, download)
	}

	roles, expiry, download = mergeAccountAccess("system_user", 0, true, guestPlan, true, now)
	if roles != "system_guest" || expiry != now.Add(24*time.Hour).UnixMilli() || download {
		t.Fatalf("new guest access = roles=%q expiry=%d download=%v", roles, expiry, download)
	}
}

func TestNormalizeMappingsRejectsDuplicateCanonicalGroup(t *testing.T) {
	_, err := normalizeMappings([]GroupMapping{{Group: "Ops"}, {Group: " ops "}})
	if err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("duplicate error = %v", err)
	}
}
