package httpapi

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestConfiguredReviewerRolesCanonicalizes(t *testing.T) {
	roles, err := configuredReviewerRoles(json.RawMessage(`{"reviewer_roles":[" system_admin ","team_lead","system_admin",""]}`))
	if err != nil {
		t.Fatalf("configuredReviewerRoles: %v", err)
	}
	want := []string{"system_admin", "team_lead"}
	if !reflect.DeepEqual(roles, want) {
		t.Fatalf("roles = %#v, want %#v", roles, want)
	}
}

func TestConfiguredReviewerRolesFailsClosedOnInvalidJSON(t *testing.T) {
	if _, err := configuredReviewerRoles(json.RawMessage(`{"reviewer_roles":`)); err == nil {
		t.Fatal("invalid reviewer configuration must return an error")
	}
}

func TestSupportedApprovalActionsMatchExecutableOutboxActions(t *testing.T) {
	for _, action := range []string{"mcp.create_post", "mcp.reply_to_thread"} {
		if _, ok := supportedApprovalActions[action]; !ok {
			t.Fatalf("executable action %q is missing from policy allow-list", action)
		}
	}
	if _, ok := supportedApprovalActions["arbitrary.action"]; ok {
		t.Fatal("arbitrary actions must not be accepted into the approval outbox")
	}
}
