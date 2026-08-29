package pat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hkjang/moyro/server/internal/bots"
)

func TestExtractAcceptsAuthorizationHeaderOnly(t *testing.T) {
	request := httptest.NewRequest("GET", "/api/v4/users/me?access_token=mdp_query_secret", nil)
	if token := extract(request); token != "" {
		t.Fatalf("query credential was accepted: %q", token)
	}

	request.Header.Set("Authorization", "Bearer mdp_header_secret")
	if token := extract(request); token != "mdp_header_secret" {
		t.Fatalf("header credential = %q", token)
	}
}

type resolverStub struct {
	resolved bots.ResolvedToken
	err      error
}

func (s resolverStub) ResolveTokenCredential(context.Context, string) (bots.ResolvedToken, error) {
	return s.resolved, s.err
}

func TestWithCarriesPATCredentialProvenance(t *testing.T) {
	type contextKey string
	const userKey contextKey = "user"
	const credentialKey contextKey = "credential"

	middleware := With(resolverStub{resolved: bots.ResolvedToken{ID: "pat-row-1", UserID: "user-1"}},
		func(ctx context.Context, userID, credentialID string) context.Context {
			ctx = context.WithValue(ctx, userKey, userID)
			return context.WithValue(ctx, credentialKey, credentialID)
		})
	request := httptest.NewRequest(http.MethodGet, "/api/v4/posts", nil)
	request.Header.Set("Authorization", "Bearer mdp_secret")
	recorder := httptest.NewRecorder()
	middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Context().Value(userKey); got != "user-1" {
			t.Fatalf("user context = %#v", got)
		}
		if got := r.Context().Value(credentialKey); got != "pat-row-1" {
			t.Fatalf("credential context = %#v", got)
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestWithFallsThroughWhenPATResolutionFails(t *testing.T) {
	setterCalled := false
	middleware := With(resolverStub{err: errors.New("invalid")}, func(ctx context.Context, _, _ string) context.Context {
		setterCalled = true
		return ctx
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v4/posts", nil)
	request.Header.Set("Authorization", "Bearer mdp_invalid")
	recorder := httptest.NewRecorder()
	middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})).ServeHTTP(recorder, request)
	if setterCalled || recorder.Code != http.StatusAccepted {
		t.Fatalf("setter=%v status=%d", setterCalled, recorder.Code)
	}
}
