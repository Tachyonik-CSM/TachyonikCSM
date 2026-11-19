// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// One connected client: a read pump and a write pump over a single WebSocket.
//
// A browser client is scoped to the users it may see, so a broadcast reaches
// only the connections entitled to it. An internal-service client connects
// unscoped, since the daemons react to changes across all users.
//
// A browser client is closed when its token expires, and its visible set can
// be replaced while it is connected: the handlers re-check every user
// connection (AM-SEC-24).

package websocket

import (
	"encoding/json"
	"sync"
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

// Why a user's connection is closed (AM-SEC-24). Both go with close code 1008
// (policy violation).
const (
	ReasonTokenExpired       = "token expired"
	ReasonAccountDeactivated = "account deactivated or deleted"
)

// Client represents a WebSocket client connection
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte

	// userID is the authenticated owner of this connection; visible is the
	// set of user IDs whose data this client may receive (self + group
	// members), resolved at connect time and replaced by each re-check
	// (AM-SEC-24). The hub uses visible to filter owner-scoped messages so a
	// client only sees its own tenant's data. mu guards visible.
	userID  int64
	mu      sync.RWMutex
	visible map[int64]bool
	// expiresAt is when the connection's token expires, and the connection
	// with it; zero for none.
	expiresAt time.Time
	// all marks a trusted service-to-service consumer that receives every
	// event regardless of tenant scoping (authenticated via the
	// internal-service key, not a user JWT). (SECURITY: CRIT-3)
	all bool
}

// NewClient creates a new authenticated WebSocket client. visibleUserIDs is
// the set of user IDs whose events this client is allowed to receive, and
// expiresAt the expiry of its token (zero for none).
func NewClient(hub *Hub, conn *websocket.Conn, userID int64, visibleUserIDs []int64, expiresAt time.Time) *Client {
	return &Client{
		hub:       hub,
		conn:      conn,
		send:      make(chan []byte, 256),
		userID:    userID,
		visible:   visibleSet(visibleUserIDs),
		expiresAt: expiresAt,
	}
}

func visibleSet(ids []int64) map[int64]bool {
	visible := make(map[int64]bool, len(ids))
	for _, id := range ids {
		visible[id] = true
	}
	return visible
}

// UserID is the user the connection belongs to.
func (c *Client) UserID() int64 { return c.userID }

// SetVisible replaces the set of users whose records the connection receives
// (AM-SEC-24).
func (c *Client) SetVisible(ids []int64) {
	v := visibleSet(ids)
	c.mu.Lock()
	c.visible = v
	c.mu.Unlock()
}

// CloseFor closes the connection with close code 1008 and the given reason.
// The read pump then ends and unregisters the client. Safe to call from any
// goroutine, and more than once.
func (c *Client) CloseFor(reason string) {
	msg := websocket.FormatCloseMessage(websocket.ClosePolicyViolation, reason)
	_ = c.conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(writeWait))
	_ = c.conn.Close()
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
// given owner user IDs. A message whose owner is not known reaches no user's
// connection, only the services' (AM-SEC-19).
func (c *Client) canSee(ownerUserIDs []int64) bool {
	if c.all {
		return true
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
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

	// The connection ends with its token (AM-SEC-24).
	if !c.all && !c.expiresAt.IsZero() {
		expiry := time.AfterFunc(time.Until(c.expiresAt), func() { c.CloseFor(ReasonTokenExpired) })
		defer expiry.Stop()
	}

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
