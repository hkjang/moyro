package httpapi

import (
	"context"
	"testing"

	"github.com/hkjang/moyro/server/internal/rbac"
)

func TestCredentialIDFromContextUsesCredentialThenSession(t *testing.T) {
	ctx := context.WithValue(context.Background(), sessionIDKey, "session-1")
	if got := credentialIDFromContext(ctx); got != "session-1" {
		t.Fatalf("session credential id = %q", got)
	}
	ctx = setPrincipalOnContext(ctx, rbac.Principal{UserID: "user-1", CredentialID: "pat-1"})
	if got := credentialIDFromContext(ctx); got != "pat-1" {
		t.Fatalf("PAT credential id = %q", got)
	}
	ctx = ensureUserPrincipal(ctx, "user-1")
	if got := credentialIDFromContext(ctx); got != "pat-1" {
		t.Fatalf("preserved credential id = %q", got)
	}
}
