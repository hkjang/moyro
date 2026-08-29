package httpapi

import (
	"context"

	"github.com/hkjang/moyro/server/internal/rbac"
)

type principalContextKey struct{}

func setPrincipalOnContext(ctx context.Context, principal rbac.Principal) context.Context {
	ctx = context.WithValue(ctx, principalContextKey{}, principal)
	if principal.UserID != "" {
		ctx = SetUserIDOnContext(ctx, principal.UserID)
	}
	return ctx
}

// PrincipalFromContext returns the credential-aware principal used by native
// REST and MCP authorization. Browser sessions are unrestricted user
// principals; rotating API/MCP keys carry their explicit grant intersection.
func PrincipalFromContext(ctx context.Context) (rbac.Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(rbac.Principal)
	return principal, ok && principal.UserID != ""
}

func ensureUserPrincipal(ctx context.Context, userID string) context.Context {
	if principal, ok := PrincipalFromContext(ctx); ok && principal.UserID == userID {
		return ctx
	}
	return setPrincipalOnContext(ctx, rbac.UserPrincipal(userID))
}
