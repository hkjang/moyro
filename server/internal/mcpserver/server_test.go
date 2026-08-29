package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hkjang/moyro/server/internal/approval"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/teams"
)

func testDependencies() Dependencies {
	return Dependencies{
		Teams: teams.New(nil), Channels: channels.New(nil), Posts: posts.New(nil),
		UserID:    func(context.Context) string { return "user-1" },
		Authorize: func(context.Context, string, string, string) (bool, error) { return true, nil },
	}
}

func TestNewRequiresAuthorizer(t *testing.T) {
	deps := testDependencies()
	deps.Authorize = nil
	if _, err := New(deps); err == nil {
		t.Fatal("New should fail closed without an authorizer")
	}
}

func TestNewRequiresDeferredCredentialAuthorizationWhenApprovalsEnabled(t *testing.T) {
	deps := testDependencies()
	deps.Approval = approval.New(nil, nil)
	if _, err := New(deps); err == nil {
		t.Fatal("New should fail closed without deferred credential authorization")
	}
}

func TestFilterAuthorizedApprovalsPreservesOnlyCredentialScopedRequests(t *testing.T) {
	service := &Service{deps: Dependencies{
		Authorize: func(_ context.Context, permission, resourceType, resourceID string) (bool, error) {
			if permission != "review_approval" || resourceType != "approval_request" {
				t.Fatalf("unexpected authorization tuple: %s %s %s", permission, resourceType, resourceID)
			}
			return resourceID == "team-a-request", nil
		},
	}}
	requests := []approval.Request{{ID: "team-a-request"}, {ID: "team-b-request"}}
	filtered, err := service.filterAuthorizedApprovals(context.Background(), requests)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].ID != "team-a-request" {
		t.Fatalf("filtered approvals = %#v", filtered)
	}
}

func TestPolicyDefaultsDenyAndCanBeReplaced(t *testing.T) {
	service, err := New(testDependencies())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.allowTool("search_messages"); err == nil {
		t.Fatal("empty tool policy should deny")
	}
	if err := service.allowResource("moyro://teams/t1"); err == nil {
		t.Fatal("empty resource policy should deny")
	}

	service.ConfigurePolicy([]string{"search_messages"}, []string{"moyro://teams"})
	if err := service.allowTool("search_messages"); err != nil {
		t.Fatalf("allowed tool rejected: %v", err)
	}
	if err := service.allowTool("create_post"); err == nil {
		t.Fatal("unlisted tool should be denied")
	}
	if err := service.allowResource("moyro://teams/t1"); err != nil {
		t.Fatalf("allowed resource rejected: %v", err)
	}
	if err := service.allowResource("moyro://teams-evil/t1"); err == nil {
		t.Fatal("resource prefix boundary must be enforced")
	}
}

func TestExecuteApprovedFailsClosedWhenOriginatingCredentialIsRevoked(t *testing.T) {
	payload, err := json.Marshal(makeApprovedPostPayload(createPostInput{
		ChannelID: "channel-1", Message: "approved message",
	}, "revoked-key"))
	if err != nil {
		t.Fatal(err)
	}
	var checks int
	service := &Service{deps: Dependencies{
		AuthorizeApproved: func(_ context.Context, requesterID, credentialID, permission, resourceType, resourceID string) (bool, error) {
			checks++
			if requesterID != "user-1" || credentialID != "revoked-key" || resourceType != "channel" || resourceID != "channel-1" {
				t.Fatalf("unexpected deferred authorization: %q %q %q %q %q", requesterID, credentialID, permission, resourceType, resourceID)
			}
			return false, nil
		},
	}}
	service.ConfigurePolicy([]string{"create_post"}, nil)
	request := &approval.Request{
		ID: "approval-1", ActionType: "mcp.create_post", RequesterID: "user-1",
		ResourceType: "channel", ResourceID: "channel-1", Payload: payload, Status: "approved",
	}
	if _, _, err := service.ExecuteApproved(context.Background(), request); err == nil {
		t.Fatal("approved action executed after its originating credential was revoked")
	}
	if checks != 1 {
		t.Fatalf("deferred authorization checks = %d, want 1", checks)
	}
}

func TestExecuteApprovedRevalidatesEveryOriginalPermission(t *testing.T) {
	payload, err := json.Marshal(makeApprovedPostPayload(createPostInput{
		ChannelID: "channel-1", Message: "approved message",
	}, "narrowed-key"))
	if err != nil {
		t.Fatal(err)
	}
	permissions := []string{}
	service := &Service{deps: Dependencies{
		AuthorizeApproved: func(_ context.Context, _, _ string, permission, _, _ string) (bool, error) {
			permissions = append(permissions, permission)
			return permission != "request_approval", nil
		},
	}}
	service.ConfigurePolicy([]string{"create_post"}, nil)
	request := &approval.Request{
		ActionType: "mcp.create_post", RequesterID: "user-1", ResourceType: "channel",
		ResourceID: "channel-1", Payload: payload, Status: "approved",
	}
	if _, _, err := service.ExecuteApproved(context.Background(), request); err == nil {
		t.Fatal("approved action executed after request_approval was removed")
	}
	if len(permissions) != 2 || permissions[0] != "mcp_write" || permissions[1] != "request_approval" {
		t.Fatalf("revalidated permissions = %#v", permissions)
	}
}

func TestExecuteApprovedRejectsLegacyPayloadWithoutCredentialProvenance(t *testing.T) {
	payload, err := json.Marshal(createPostInput{ChannelID: "channel-1", Message: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{}
	service.ConfigurePolicy([]string{"create_post"}, nil)
	request := &approval.Request{
		ActionType: "mcp.create_post", RequesterID: "user-1", ResourceType: "channel",
		ResourceID: "channel-1", Payload: payload, Status: "approved",
	}
	if _, _, err := service.ExecuteApproved(context.Background(), request); err == nil {
		t.Fatal("legacy approved action without credential provenance must fail closed")
	}
}
