package oidcauth

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestExpiredFlowCleanupLimitIsBounded(t *testing.T) {
	t.Parallel()

	if expiredFlowCleanupLimit < 1 || expiredFlowCleanupLimit > 1000 {
		t.Fatalf("expired flow cleanup limit = %d, want a positive bounded batch", expiredFlowCleanupLimit)
	}
}

func TestFlowProviderBindingAcceptsMaximumMappingShapeWithinOneMiB(t *testing.T) {
	channels := make([]string, 100)
	for index := range channels {
		channels[index] = fmt.Sprintf("channel-%028d", index)
	}
	mappings := make([]map[string]any, 100)
	for index := range mappings {
		mappings[index] = map[string]any{
			"group": fmt.Sprintf("/group-%03d", index), "account_role": "user",
			"team_id": "team-1", "team_role": "member",
			"channel_ids": channels, "channel_role": "member", "guest_file_download": true,
		}
	}
	policy, err := json.Marshal(map[string]any{"allow_signup": true, "group_mappings": mappings})
	if err != nil {
		t.Fatal(err)
	}
	if len(policy) <= 128<<10 || len(policy) > maxFlowPolicyBytes {
		t.Fatalf("maximum mapping policy size = %d, expected between old 128KiB cap and 1MiB", len(policy))
	}
	if err := validateFlowProviderBinding("snapshot-1", policy); err != nil {
		t.Fatalf("large valid flow policy was rejected: %v", err)
	}

	tooLarge, err := json.Marshal(map[string]string{"policy": strings.Repeat("x", maxFlowPolicyBytes)})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateFlowProviderBinding("snapshot-1", tooLarge); err == nil {
		t.Fatal("flow policy larger than 1MiB was accepted")
	}
}
