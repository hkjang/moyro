package oidcauth

import "testing"

func TestExpiredFlowCleanupLimitIsBounded(t *testing.T) {
	t.Parallel()

	if expiredFlowCleanupLimit < 1 || expiredFlowCleanupLimit > 1000 {
		t.Fatalf("expired flow cleanup limit = %d, want a positive bounded batch", expiredFlowCleanupLimit)
	}
}
