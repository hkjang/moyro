// Package pat bridges personal access tokens into the existing bearer-token
// auth flow.
//
// Tokens issued via bots.Service.CreateToken have the prefix "mdp_" so the
// middleware can branch at a glance: PAT → sha256 lookup, anything else →
// fall through to the JWT parser. Keeping this as a composable wrapper
// (rather than a replacement) lets the rest of the server treat the
// resulting user-id-in-context identically whether the caller was a human
// session or a bot PAT.
package pat

import (
	"context"
	"net/http"
	"strings"

	"github.com/moddle/moddle/server/internal/bots"
)

// ctx key type is unexported to prevent collisions; we re-use httpapi's
// userIDKey via a public setter passed in by the caller (see With).
type userCtxSetter func(context.Context, string) context.Context

// With returns a middleware that intercepts Authorization headers starting
// with "Bearer mdp_". On a valid PAT, it injects the owning user id into
// the request context via `setUserID` and hands off to `next`, bypassing
// the JWT middleware entirely. On anything else (empty header, JWT, or a
// malformed PAT), it simply calls through to the next handler so the
// downstream JWT middleware can run.
//
// This function is stateless beyond the Service + setter closure, so a
// single instance can safely serve every request.
func With(svc *bots.Service, setUserID userCtxSetter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok := extract(r)
			if tok == "" || !bots.IsPATFormat(tok) {
				next.ServeHTTP(w, r)
				return
			}
			uid, err := svc.ResolveToken(r.Context(), tok)
			if err != nil || uid == "" {
				// Don't 401 here — fall through so the downstream
				// JWT middleware can produce a coherent error
				// message that matches existing clients' expectations.
				next.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(setUserID(r.Context(), uid)))
		})
	}
}

func extract(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if t := r.URL.Query().Get("access_token"); t != "" {
		return t
	}
	return ""
}
