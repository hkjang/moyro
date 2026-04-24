package ws

import (
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
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = 30 * time.Second
)

// Handle upgrades a request to a WebSocket connection. The caller is
// responsible for authenticating and supplying userID.
func Handle(hub *Hub, userID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		c := &Client{UserID: userID, Conn: conn, Send: make(chan []byte, 64)}
		hub.Register(c)

		go writePump(c)
		readPump(hub, c)
	}
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

func writePump(c *Client) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
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
		case <-ticker.C:
			_ = c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
