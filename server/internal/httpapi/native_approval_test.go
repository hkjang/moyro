package httpapi

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hkjang/moyro/server/internal/approval"
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

func TestApprovalRequestViewsNeverSerializeExecutionPayload(t *testing.T) {
	const (
		credentialID = "credential-id-must-never-leave-the-server"
		unknownValue = "unknown-payload-value-must-stay-hidden"
		messageToken = "message-secret-value-123456789"
	)
	payload, err := json.Marshal(map[string]any{
		"channel_id":           "channel-1",
		"message":              "운영 공지\nAPI key: " + messageToken,
		"_moyro_credential_id": credentialID,
		"unexpected":           unknownValue,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	request := &approval.Request{
		ID: "request-1", PolicyID: "policy-1", ActionType: "mcp.create_post",
		RequesterID: "user-1", TeamID: "team-1", ResourceType: "channel", ResourceID: "channel-1",
		Payload: payload, Status: "pending", IdempotencyKey: "operation-1",
		CreateAt: 1, UpdateAt: 2, DecidedAt: 3, ExecutedAt: 4, ExpiresAt: 5,
	}
	view := makeApprovalRequestView(request, "운영 공지")
	viewCopy := view

	for name, value := range map[string]any{
		"submit":   approvalResultView{ApprovalRequired: true, Request: &viewCopy},
		"mine":     []approvalRequestView{view},
		"review":   []approvalRequestView{view},
		"decision": view,
	} {
		t.Run(name, func(t *testing.T) {
			wire, marshalErr := json.Marshal(value)
			if marshalErr != nil {
				t.Fatalf("marshal safe view: %v", marshalErr)
			}
			serialized := string(wire)
			for _, forbidden := range []string{`"payload"`, "_moyro_credential_id", credentialID, unknownValue, messageToken} {
				if strings.Contains(serialized, forbidden) {
					t.Fatalf("response contains forbidden approval data %q: %s", forbidden, serialized)
				}
			}
			if !strings.Contains(serialized, `"preview"`) || !strings.Contains(serialized, approval.PreviewRedactedValue) {
				t.Fatalf("response lacks structured redacted preview: %s", serialized)
			}
		})
	}

	wire, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal request view: %v", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(wire, &object); err != nil {
		t.Fatalf("unmarshal request view: %v", err)
	}
	wantKeys := []string{
		"action_type", "create_at", "decided_at", "executed_at", "expires_at", "id", "idempotency_key",
		"policy_id", "preview", "requester_id", "resource_id", "resource_type", "status", "team_id", "update_at",
	}
	gotKeys := make([]string, 0, len(object))
	for key := range object {
		gotKeys = append(gotKeys, key)
	}
	sort.Strings(gotKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("response keys = %#v, want %#v", gotKeys, wantKeys)
	}
	if view.Preview.Title != "채널 메시지 작성" || view.Preview.RiskLevel != "medium" {
		t.Fatalf("preview identity = %#v", view.Preview)
	}
	if view.Preview.Actor.Type != "mcp_key" || view.Preview.Target.DisplayName != "운영 공지" {
		t.Fatalf("preview actor/target = %#v / %#v", view.Preview.Actor, view.Preview.Target)
	}
	if len(view.Preview.Changes) != 1 || !strings.Contains(view.Preview.Changes[0].After, approval.PreviewRedactedValue) {
		t.Fatalf("preview changes = %#v", view.Preview.Changes)
	}
}

func TestApprovalRequestPreviewOnlyIncludesMessageForSupportedActions(t *testing.T) {
	tests := []struct {
		name        string
		action      string
		wantTitle   string
		wantRisk    string
		wantChanges int
		wantLabel   string
	}{
		{name: "create post", action: "mcp.create_post", wantTitle: "채널 메시지 작성", wantRisk: "medium", wantChanges: 1, wantLabel: "작성할 메시지"},
		{name: "reply", action: "mcp.reply_to_thread", wantTitle: "스레드 답글 작성", wantRisk: "medium", wantChanges: 1, wantLabel: "작성할 답글"},
		{name: "unsupported", action: "plugin.external_action", wantTitle: "승인 요청", wantRisk: "unknown", wantChanges: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := &approval.Request{
				ActionType: test.action, ResourceType: "channel",
				Payload: json.RawMessage(`{"message":"표시 가능한 메시지","secret":"숨겨야 할 값"}`),
			}
			preview := makeApprovalRequestView(request, "테스트 채널").Preview
			if preview.Title != test.wantTitle || preview.RiskLevel != test.wantRisk || len(preview.Changes) != test.wantChanges {
				t.Fatalf("preview = %#v", preview)
			}
			if test.wantChanges > 0 && preview.Changes[0].Label != test.wantLabel {
				t.Fatalf("change label = %q, want %q", preview.Changes[0].Label, test.wantLabel)
			}
			if test.wantChanges == 0 && strings.Contains(mustJSON(t, preview), "표시 가능한 메시지") {
				t.Fatal("unsupported action exposed its payload message")
			}
		})
	}
}

func TestRedactApprovalPreviewMessageMasksValuesAndLimitsOutput(t *testing.T) {
	secrets := []string{
		"api-secret-value-123456789",
		"bearer-secret-value-123456789",
		"moyro_superSecretToken123",
		"url-password-123",
		"private-key-material",
	}
	message := strings.Join([]string{
		"운영 설정을 검토해 주세요.",
		`{"API key":"` + secrets[0] + `"}`,
		"Authorization: Bearer " + secrets[1],
		secrets[2],
		"https://operator:" + secrets[3] + "@internal.example.test/path",
		"-----BEGIN PRIVATE KEY-----\n" + secrets[4] + "\n-----END PRIVATE KEY-----",
		strings.Repeat("가", approval.PreviewMessageLimit+100),
	}, "\n")

	visible, redacted := approval.RedactPreviewMessage(message)
	if !redacted {
		t.Fatal("expected secret redaction")
	}
	if !strings.Contains(visible, "운영 설정을 검토해 주세요.") || !strings.Contains(visible, approval.PreviewRedactedValue) {
		t.Fatalf("safe message content missing: %q", visible)
	}
	for _, secret := range secrets {
		if strings.Contains(visible, secret) {
			t.Fatalf("visible preview contains secret %q", secret)
		}
	}
	if len([]rune(visible)) > approval.PreviewMessageLimit {
		t.Fatalf("visible preview length = %d, limit = %d", len([]rune(visible)), approval.PreviewMessageLimit)
	}
	if !strings.HasSuffix(visible, approval.PreviewOmittedValue) {
		t.Fatalf("truncated preview lacks omission marker: %q", visible[len(visible)-32:])
	}
	alreadyRedacted, _ := approval.RedactPreviewMessage("API key: " + approval.PreviewRedactedValue)
	if alreadyRedacted != "API key: "+approval.PreviewRedactedValue {
		t.Fatalf("already-redacted value changed: %q", alreadyRedacted)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}
