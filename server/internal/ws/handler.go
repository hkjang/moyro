package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// ClientAction is the envelope Mattermost clients send to the server.
// We currently only route "user_typing" but the envelope is stable so
// adding more actions later is a switch-case away.
type ClientAction struct {
	Seq    int64          `json:"seq"`
	Action string         `json:"action"`
	Data   map[string]any `json:"data"`
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(*http.Request) bool { return true },
}

const (
	writeWait        = 10 * time.Second
	pongWait         = 60 * time.Second
	pingPeriod       = 30 * time.Second
	authWait         = 10 * time.Second
	authMessageLimit = 16 << 10
	revalidatePeriod = 25 * time.Second
	revalidateWait   = 5 * time.Second
)

// TokenAuthenticator validates a credential supplied through an Authorization
// header or initial authentication_challenge frame and returns its active
// user. Keeping the callback in httpapi avoids coupling this transport package
// to JWT/session storage details.
type TokenAuthenticator func(context.Context, string) (string, error)

// Handle upgrades a request whose Authorization header has already been
// authenticated by the caller. The exact credential and authenticator are
// retained so logout, expiry, revocation, and deactivation terminate the live
// socket without waiting for another HTTP request.
func Handle(hub *Hub, userID, credential string, authenticate TokenAuthenticator) http.HandlerFunc {
	return handle(hub, userID, credential, authenticate, revalidatePeriod)
}

func handle(hub *Hub, userID, credential string, authenticate TokenAuthenticator, interval time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if userID == "" || credential == "" || authenticate == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serveConnection(hub, userID, credential, authenticate, interval, conn)
	}
}

// HandleChallenge upgrades an unauthenticated browser connection and accepts
// a credential only in its first WebSocket frame. This mirrors Mattermost's
// authentication_challenge action while keeping bearer tokens out of URLs,
// proxy logs, and browser history. The socket is never registered with the hub
// until authentication succeeds.
func HandleChallenge(hub *Hub, authenticate TokenAuthenticator) http.HandlerFunc {
	return handleChallenge(hub, authenticate, revalidatePeriod)
}

func handleChallenge(hub *Hub, authenticate TokenAuthenticator, interval time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		userID, credential, seq, err := authenticateFirstFrame(r.Context(), conn, authenticate)
		if err != nil {
			closePolicyViolation(conn, "authentication failed")
			return
		}
		if err := writeAuthReply(conn, seq); err != nil {
			_ = conn.Close()
			return
		}
		serveConnection(hub, userID, credential, authenticate, interval, conn)
	}
}

func authenticateFirstFrame(ctx context.Context, conn *websocket.Conn, authenticate TokenAuthenticator) (string, string, int64, error) {
	if authenticate == nil {
		return "", "", 0, context.Canceled
	}
	conn.SetReadLimit(authMessageLimit)
	if err := conn.SetReadDeadline(time.Now().Add(authWait)); err != nil {
		return "", "", 0, err
	}
	messageType, msg, err := conn.ReadMessage()
	if err != nil {
		return "", "", 0, err
	}
	if messageType != websocket.TextMessage {
		return "", "", 0, websocket.ErrBadHandshake
	}
	var act ClientAction
	if err := json.Unmarshal(msg, &act); err != nil {
		return "", "", 0, err
	}
	if act.Action != "authentication_challenge" {
		return "", "", 0, websocket.ErrBadHandshake
	}
	token, _ := act.Data["token"].(string)
	if token == "" {
		return "", "", 0, websocket.ErrBadHandshake
	}
	authCtx, cancel := context.WithTimeout(ctx, revalidateWait)
	defer cancel()
	userID, err := authenticate(authCtx, token)
	if err != nil || userID == "" {
		if err != nil {
			return "", "", 0, err
		}
		return "", "", 0, websocket.ErrBadHandshake
	}
	return userID, token, act.Seq, nil
}

func writeAuthReply(conn *websocket.Conn, seq int64) error {
	if err := conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return conn.WriteJSON(map[string]any{"status": "OK", "seq_reply": seq})
}

func closePolicyViolation(conn *websocket.Conn, reason string) {
	deadline := time.Now().Add(writeWait)
	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.ClosePolicyViolation, reason),
		deadline,
	)
	_ = conn.Close()
}

func serveConnection(hub *Hub, userID, credential string, authenticate TokenAuthenticator, interval time.Duration, conn *websocket.Conn) {
	c := &Client{
		UserID:       userID,
		Conn:         conn,
		Send:         make(chan []byte, 64),
		credential:   credential,
		authenticate: authenticate,
	}
	hub.Register(c)

	go writePump(c, interval)
	readPump(hub, c)
}

func readPump(hub *Hub, c *Client) {
	defer func() {
		hub.Unregister(c)
		_ = c.Conn.Close()
	}()
	c.Conn.SetReadLimit(1 << 20)
	_ = c.Conn.SetReadDeadline(time.Now().Add(pongWait))
	c.Conn.SetPongHandler(func(string) error {
		return c.Conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		_, msg, err := c.Conn.ReadMessage()
		if err != nil {
			return
		}
		handleClientAction(hub, c, msg)
	}
}

func handleClientAction(hub *Hub, c *Client, msg []byte) {
	var act ClientAction
	if err := json.Unmarshal(msg, &act); err != nil {
		return
	}
	switch act.Action {
	case "user_typing":
		channelID, _ := act.Data["channel_id"].(string)
		parentID, _ := act.Data["parent_id"].(string)
		if channelID == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), revalidateWait)
		allowed, err := hub.CanPublish(ctx, c.UserID, Broadcast{ChannelID: channelID})
		cancel()
		if err != nil || !allowed {
			return
		}
		hub.Broadcast(Event{
			Event: "typing",
			Data: map[string]any{
				"user_id":   c.UserID,
				"parent_id": parentID,
			},
			Broadcast: Broadcast{ChannelID: channelID, OmitUsers: []string{c.UserID}},
		})
	}
}

func writePump(c *Client, interval time.Duration) {
	if interval <= 0 || interval > revalidatePeriod {
		interval = revalidatePeriod
	}
	pingTicker := time.NewTicker(pingPeriod)
	revalidateTicker := time.NewTicker(interval)
	defer func() {
		pingTicker.Stop()
		revalidateTicker.Stop()
		_ = c.Conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.Send:
			_ = c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.Conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			if err := c.Conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-revalidateTicker.C:
			if !revalidateCredential(c) {
				closePolicyViolation(c.Conn, "session is no longer valid")
				return
			}
		case <-pingTicker.C:
			_ = c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func revalidateCredential(c *Client) bool {
	if c == nil || c.UserID == "" || c.credential == "" || c.authenticate == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), revalidateWait)
	defer cancel()
	userID, err := c.authenticate(ctx, c.credential)
	return err == nil && userID == c.UserID
}
