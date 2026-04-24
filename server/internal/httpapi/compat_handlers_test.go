package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func requestWithUserParam(actorID, targetID string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v4/users/"+targetID+"/teams", nil)
	ctx := context.WithValue(req.Context(), userIDKey, actorID)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("userID", targetID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, routeCtx)
	return req.WithContext(ctx)
}

func TestRequireUserParamAccessAllowsSelfAndMe(t *testing.T) {
	h := &handlers{}

	req := requestWithUserParam("u1", "u1")
	rr := httptest.NewRecorder()
	got, ok := h.requireUserParamAccess(rr, req, "userID")
	if !ok || got != "u1" || rr.Code != http.StatusOK {
		t.Fatalf("self access got id=%q ok=%v status=%d", got, ok, rr.Code)
	}

	req = requestWithUserParam("u1", "me")
	rr = httptest.NewRecorder()
	got, ok = h.requireUserParamAccess(rr, req, "userID")
	if !ok || got != "u1" || rr.Code != http.StatusOK {
		t.Fatalf("me access got id=%q ok=%v status=%d", got, ok, rr.Code)
	}
}

func TestRequireUserParamAccessRejectsOtherUserWithoutAdmin(t *testing.T) {
	h := &handlers{}
	req := requestWithUserParam("u1", "u2")
	rr := httptest.NewRecorder()

	got, ok := h.requireUserParamAccess(rr, req, "userID")
	if ok || got != "" {
		t.Fatalf("other-user access got id=%q ok=%v, want rejection", got, ok)
	}
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	var body apiError
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.StatusCode != http.StatusForbidden {
		t.Fatalf("body status = %d, want 403", body.StatusCode)
	}
}
