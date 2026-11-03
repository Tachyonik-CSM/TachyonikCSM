// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// One connected client: a read pump and a write pump over a single WebSocket.
//
// A browser client is scoped to the users it may see, so a broadcast reaches
// only the connections entitled to it. An internal-service client connects
// unscoped, since the daemons react to changes across all users.

package websocket

import (
	"encoding/json"
	"time"

	"github.com/gorilla/websocket"
	"tachyonik/lib/logger"
)

const (
	// Time allowed to write a message to the peer
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer
	pongWait = 60 * time.Second

	// Send pings to peer with this period (must be less than pongWait)
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer
	maxMessageSize = 512
)

// Client represents a WebSocket client connection
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte

	// userID is the authenticated owner of this connection; visible is the
	// set of user IDs whose data this client may receive (self + group
	// members), resolved at connect time. The hub uses visible to filter
	// owner-scoped messages so a client only sees its own tenant's data.
	userID  int64
	visible map[int64]bool
	// all marks a trusted service-to-service consumer that receives every
	// event regardless of tenant scoping (authenticated via the
	// internal-service key, not a user JWT). (SECURITY: CRIT-3)
	all bool
}

// NewClient creates a new authenticated WebSocket client. visibleUserIDs is
// the set of user IDs whose events this client is allowed to receive.
func NewClient(hub *Hub, conn *websocket.Conn, userID int64, visibleUserIDs []int64) *Client {
	visible := make(map[int64]bool, len(visibleUserIDs))
	for _, id := range visibleUserIDs {
		visible[id] = true
	}
	return &Client{
		hub:     hub,
		conn:    conn,
		send:    make(chan []byte, 256),
		userID:  userID,
		visible: visible,
	}
}

// NewInternalClient creates a WebSocket client for a trusted service-to-service
// consumer (an internal daemon authenticated with the internal-service key). It
// is not scoped to any tenant and receives every event the hub broadcasts.
// (SECURITY: CRIT-3)
func NewInternalClient(hub *Hub, conn *websocket.Conn) *Client {
	return &Client{
		hub:  hub,
		conn: conn,
		send: make(chan []byte, 256),
		all:  true,
	}
}

// canSee reports whether this client may receive a message scoped to the
// given owner user IDs. An empty owner set means "broadcast to all".
func (c *Client) canSee(ownerUserIDs []int64) bool {
	if c.all {
		return true
	}
	if len(ownerUserIDs) == 0 {
		return true
	}
	for _, id := range ownerUserIDs {
		if c.visible[id] {
			return true
		}
	}
	return false
}

// ReadPump pumps messages from the WebSocket connection to the hub
func (c *Client) ReadPump() {
	defer func() {
		c.hub.Unregister(c)
		c.conn.Close()
	}()

	// Clients only ever send PING and PONG; a larger message ends the
	// connection rather than being read in full.
	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logger.Errorf("WebSocket error: %v", err)
			}
			break
		}

		// Handle incoming messages (like PING)
		var msg Message
		if err := json.Unmarshal(message, &msg); err == nil {
			if msg.Type == TypePing {
				// Respond with PONG
				pong, _ := NewMessage(TypePong, nil)
				pongData, _ := json.Marshal(pong)
				c.send <- pongData
			}
		}
	}
}

// WritePump pumps messages from the hub to the WebSocket connection
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Hub closed the channel
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
