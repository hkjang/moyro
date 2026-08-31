package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/auth"
	"github.com/hkjang/moyro/server/internal/secrets"
	"github.com/hkjang/moyro/server/internal/store"
)

func TestSSOCodeExchangeReturnsSessionAcceptedByUsersMePostgres(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate SSO test schema: %v", err)
	}
	manager, err := secrets.New(bytes.Repeat([]byte{0x71}, secrets.MasterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.New(db, []byte("sso-http-signing-key-32-bytes!!"), time.Hour, manager)
	user, err := authService.Register(ctx, "sso-http-user", "sso-http@example.test", "long-test-password")
	if err != nil {
		t.Fatalf("register SSO user: %v", err)
	}
	handoff, err := authService.CreateLoginHandoff(ctx, user.ID)
	if err != nil {
		t.Fatalf("create SSO handoff: %v", err)
	}

	h := &handlers{auth: authService, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	router := chi.NewRouter()
	router.Post(ssoSessionExchangePath, h.nativeSSOSessionExchange)
	router.With(h.requireAuth).Get("/api/v4/users/me", h.me)

	body, _ := json.Marshal(map[string]string{"code": handoff.Code})
	unboundRecorder := httptest.NewRecorder()
	router.ServeHTTP(unboundRecorder, httptest.NewRequest(http.MethodPost, ssoSessionExchangePath, bytes.NewReader(body)))
	if unboundRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("SSO exchange without browser binding status = %d, want 401", unboundRecorder.Code)
	}

	exchangeRequest := httptest.NewRequest(http.MethodPost, ssoSessionExchangePath, bytes.NewReader(body))
	exchangeRequest.AddCookie(&http.Cookie{Name: ssoHandoffCookieName(handoff.Code), Value: handoff.BrowserBinding, Path: ssoSessionExchangePath})
	exchangeRecorder := httptest.NewRecorder()
	router.ServeHTTP(exchangeRecorder, exchangeRequest)
	if exchangeRecorder.Code != http.StatusOK {
		t.Fatalf("SSO exchange status = %d, body=%s", exchangeRecorder.Code, exchangeRecorder.Body.String())
	}
	var exchanged struct {
		Token string    `json:"token"`
		User  auth.User `json:"user"`
	}
	if err := json.NewDecoder(exchangeRecorder.Body).Decode(&exchanged); err != nil {
		t.Fatalf("decode SSO exchange: %v", err)
	}
	if exchanged.Token == "" || exchanged.User.ID != user.ID || exchangeRecorder.Header().Get("Token") != exchanged.Token {
		t.Fatalf("unexpected SSO exchange response: user=%q token=%t header_match=%t", exchanged.User.ID, exchanged.Token != "", exchangeRecorder.Header().Get("Token") == exchanged.Token)
	}
	if exchangeRecorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("SSO exchange Cache-Control = %q, want no-store", exchangeRecorder.Header().Get("Cache-Control"))
	}
	assertSSOHandoffCookieCleared(t, exchangeRecorder.Result(), handoff.Code)

	meRequest := httptest.NewRequest(http.MethodGet, "/api/v4/users/me", nil)
	meRequest.Header.Set("Authorization", "Bearer "+exchanged.Token)
	meRecorder := httptest.NewRecorder()
	router.ServeHTTP(meRecorder, meRequest)
	if meRecorder.Code != http.StatusOK {
		t.Fatalf("/users/me rejected exchanged SSO session: status=%d body=%s", meRecorder.Code, meRecorder.Body.String())
	}
	var me auth.User
	if err := json.NewDecoder(meRecorder.Body).Decode(&me); err != nil || me.ID != user.ID {
		t.Fatalf("/users/me response = (%+v, %v), want user %q", me, err, user.ID)
	}

	replayRecorder := httptest.NewRecorder()
	replayRequest := httptest.NewRequest(http.MethodPost, ssoSessionExchangePath, bytes.NewReader(body))
	replayRequest.AddCookie(&http.Cookie{Name: ssoHandoffCookieName(handoff.Code), Value: handoff.BrowserBinding, Path: ssoSessionExchangePath})
	router.ServeHTTP(replayRecorder, replayRequest)
	if replayRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("replayed SSO code status = %d, want 401", replayRecorder.Code)
	}
}
