package httpapi

import (
	"context"
	"net/http/httptest"
	"reflect"
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

func TestRequestPrincipalPreservesSessionPATAndScopedAPIKeySemantics(t *testing.T) {
	request := httptest.NewRequest("GET", "/api/moyro/v1/me/work-items", nil)
	request = request.WithContext(context.WithValue(request.Context(), userIDKey, "session-user"))
	if got := requestPrincipal(request); got.UserID != "session-user" || got.Restricted || got.CredentialID != "" {
		t.Fatalf("session request principal = %#v", got)
	}

	pat := rbac.Principal{UserID: "pat-user", CredentialID: "pat-1"}
	request = request.WithContext(setPrincipalOnContext(request.Context(), pat))
	if got := requestPrincipal(request); !reflect.DeepEqual(got, pat) {
		t.Fatalf("PAT request principal = %#v, want %#v", got, pat)
	}

	scoped := rbac.Principal{
		UserID:            "api-user",
		CredentialID:      "api-key-1",
		Restricted:        true,
		AllowedTeamIDs:    map[string]struct{}{"team-1": {}},
		AllowedChannelIDs: map[string]struct{}{"channel-1": {}},
	}
	request = request.WithContext(setPrincipalOnContext(request.Context(), scoped))
	if got := requestPrincipal(request); !reflect.DeepEqual(got, scoped) {
		t.Fatalf("scoped API-key request principal = %#v, want %#v", got, scoped)
	}
}
