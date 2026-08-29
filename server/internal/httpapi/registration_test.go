package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeRegisterRequest(t *testing.T) {
	req := registerReq{
		Username: "  Alice.Dev  ",
		Email:    "  Alice@Example.COM ",
		Password: "long-test-password",
	}
	if err := normalizeRegisterRequest(&req); err != nil {
		t.Fatalf("normalize registration: %v", err)
	}
	if req.Username != "alice.dev" || req.Email != "alice@example.com" {
		t.Fatalf("normalized request = %#v", req)
	}
}

func TestNormalizeRegisterRequestRejectsUnsafeFields(t *testing.T) {
	tests := []registerReq{
		{Username: "a", Email: "a@example.com", Password: "long-test-password"},
		{Username: "bad name", Email: "a@example.com", Password: "long-test-password"},
		{Username: "alice", Email: "not-an-email", Password: "long-test-password"},
		{Username: "alice", Email: "a@example.com", Password: "short"},
		{Username: "alice", Email: "a@example.com", Password: "long-test-password", InviteID: "bad\ninvite"},
	}
	for _, req := range tests {
		if err := normalizeRegisterRequest(&req); err == nil {
			t.Fatalf("unsafe registration accepted: %#v", req)
		}
	}
}

func TestRegisterFailsClosedWhenLocalSignupIsDisabled(t *testing.T) {
	h := &handlers{}
	body := bytes.NewBufferString(`{"username":"alice","email":"alice@example.com","password":"long-test-password"}`)
	recorder := httptest.NewRecorder()
	h.register(recorder, httptest.NewRequest(http.MethodPost, "/api/v4/users", body))

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("registration status = %d, want 403; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRegisterRejectsUnknownOrOversizedBodyBeforeBcrypt(t *testing.T) {
	h := &handlers{}
	for _, body := range [][]byte{
		[]byte(`{"username":"alice","email":"alice@example.com","password":"long-test-password","admin":true}`),
		bytes.Repeat([]byte("x"), (32<<10)+1),
	} {
		recorder := httptest.NewRecorder()
		h.register(recorder, httptest.NewRequest(http.MethodPost, "/api/v4/users", bytes.NewReader(body)))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("registration status = %d, want 400", recorder.Code)
		}
	}
}
