package ws

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestHandleChallengeAuthenticatesBeforeRegistration(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)

	var gotToken string
	server := httptest.NewServer(HandleChallenge(hub, func(_ context.Context, token string) (string, error) {
		gotToken = token
		if token != "session-secret" {
			return "", errors.New("invalid token")
		}
		return "user-1", nil
	}))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(websocketURL(server.URL), nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(ClientAction{
		Seq:    42,
		Action: "authentication_challenge",
		Data:   map[string]any{"token": "session-secret"},
	}); err != nil {
		t.Fatalf("write authentication challenge: %v", err)
	}
	var reply struct {
		Status   string `json:"status"`
		SeqReply int64  `json:"seq_reply"`
	}
	if err := conn.ReadJSON(&reply); err != nil {
		t.Fatalf("read authentication reply: %v", err)
	}
	if gotToken != "session-secret" {
		t.Fatalf("authenticator token = %q", gotToken)
	}
	if reply.Status != "OK" || reply.SeqReply != 42 {
		t.Fatalf("unexpected authentication reply: %+v", reply)
	}

	deadline := time.Now().Add(time.Second)
	for hub.ClientCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := hub.ClientCount(); got != 1 {
		t.Fatalf("registered clients = %d, want 1", got)
	}
}

func TestHandleChallengeRejectsInvalidFirstFrame(t *testing.T) {
	hub := NewHub()
	called := false
	server := httptest.NewServer(HandleChallenge(hub, func(context.Context, string) (string, error) {
		called = true
		return "user-1", nil
	}))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(websocketURL(server.URL), nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(ClientAction{
		Seq:    1,
		Action: "user_typing",
		Data:   map[string]any{"channel_id": "private-channel"},
	}); err != nil {
		t.Fatalf("write unauthenticated action: %v", err)
	}
	_, _, err = conn.ReadMessage()
	if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("read error = %v, want policy-violation close", err)
	}
	if called {
		t.Fatal("authenticator called for a non-authentication first frame")
	}
	if got := hub.ClientCount(); got != 0 {
		t.Fatalf("registered clients = %d, want 0", got)
	}
}

func TestHandleChallengeRejectsInvalidCredential(t *testing.T) {
	hub := NewHub()
	server := httptest.NewServer(HandleChallenge(hub, func(_ context.Context, _ string) (string, error) {
		return "", errors.New("expired session")
	}))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(websocketURL(server.URL), nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(ClientAction{
		Seq:    7,
		Action: "authentication_challenge",
		Data:   map[string]any{"token": "rejected-secret"},
	}); err != nil {
		t.Fatalf("write authentication challenge: %v", err)
	}
	_, _, err = conn.ReadMessage()
	if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("read error = %v, want policy-violation close", err)
	}
	if got := hub.ClientCount(); got != 0 {
		t.Fatalf("registered clients = %d, want 0", got)
	}
}

func TestHandleChallengeRevalidatesLiveCredential(t *testing.T) {
	hub, stopHub := runningTestHub(t)
	defer stopHub()

	var valid atomic.Bool
	valid.Store(true)
	var calls atomic.Int32
	authenticate := func(_ context.Context, token string) (string, error) {
		calls.Add(1)
		if token != "session-secret" || !valid.Load() {
			return "", errors.New("invalid session")
		}
		return "user-1", nil
	}
	server := httptest.NewServer(handleChallenge(hub, authenticate, 5*time.Millisecond))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(websocketURL(server.URL), nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(ClientAction{
		Seq:    1,
		Action: "authentication_challenge",
		Data:   map[string]any{"token": "session-secret"},
	}); err != nil {
		t.Fatalf("write authentication challenge: %v", err)
	}
	var reply map[string]any
	if err := conn.ReadJSON(&reply); err != nil {
		t.Fatalf("read authentication reply: %v", err)
	}
	waitForClientCount(t, hub, 1)

	valid.Store(false)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err = conn.ReadMessage()
	if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("read error = %v, want policy-violation close", err)
	}
	if calls.Load() < 2 {
		t.Fatalf("authenticator calls = %d, want initial + periodic validation", calls.Load())
	}
	waitForClientCount(t, hub, 0)
}

func TestHandleHeaderCredentialIsPeriodicallyRevalidated(t *testing.T) {
	hub, stopHub := runningTestHub(t)
	defer stopHub()

	var valid atomic.Bool
	valid.Store(true)
	authenticate := func(_ context.Context, token string) (string, error) {
		if token != "header-secret" || !valid.Load() {
			return "", errors.New("invalid session")
		}
		return "user-1", nil
	}
	server := httptest.NewServer(handle(hub, "user-1", "header-secret", authenticate, 5*time.Millisecond))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(websocketURL(server.URL), nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	waitForClientCount(t, hub, 1)

	valid.Store(false)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err = conn.ReadMessage()
	if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("read error = %v, want policy-violation close", err)
	}
	waitForClientCount(t, hub, 0)
}

func runningTestHub(t *testing.T) (*Hub, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	hub := NewHub()
	go hub.Run(ctx)
	return hub, cancel
}

func waitForClientCount(t *testing.T, hub *Hub, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for hub.ClientCount() != want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := hub.ClientCount(); got != want {
		t.Fatalf("registered clients = %d, want %d", got, want)
	}
}

func websocketURL(serverURL string) string {
	return "ws" + strings.TrimPrefix(serverURL, "http")
}
