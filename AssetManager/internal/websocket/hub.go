// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package websocket is the fan-out hub that pushes asset, vulnerability and
// detection changes to connected clients, so an open UI reflects an import or a
// deletion without polling.
//
// A client is scoped to the users it may see, so a broadcast reaches only the
// connections entitled to it; internal-service clients connect unscoped, since
// the daemons react to changes across all users.
package websocket

import (
	"encoding/json"
	"sync"

	"tachyonik/lib/logger"
)

// Hub maintains the set of active clients and broadcasts messages to clients
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan *Message
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex
}

// NewHub creates a new WebSocket hub
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan *Message, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// Run starts the hub's main loop
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			logger.Infof("WebSocket client connected. Total clients: %d", len(h.clients))

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()
			logger.Infof("WebSocket client disconnected. Total clients: %d", len(h.clients))

		case message := <-h.broadcast:
			// Serialize message once
			data, err := json.Marshal(message)
			if err != nil {
				logger.Errorf("Failed to marshal message: %v", err)
				continue
			}

			// Deliver only to clients permitted to see this message's owner
			// set (tenant isolation, AM-SEC-19). An empty owner set reaches
			// the services only.
			h.mu.RLock()
			for client := range h.clients {
				if !client.canSee(message.OwnerUserIDs) {
					continue
				}
				select {
				case client.send <- data:
				default:
					// Client's send buffer is full, disconnect it
					close(client.send)
					delete(h.clients, client)
				}
			}
			h.mu.RUnlock()
		}
	}
}

// Register registers a new client
func (h *Hub) Register(client *Client) {
	h.register <- client
}

// Unregister unregisters a client
func (h *Hub) Unregister(client *Client) {
	h.unregister <- client
}

// BroadcastScoped sends a message to the services and to the user clients
// whose visible-user set includes one of ownerUserIDs: the owner's, and those
// of the owner's workspace members who are connected (AM-SEC-19). Every
// announcement about a record goes this way, an id-only one too, since even
// an id tells another tenant that a record exists. With no owner given, it
// reaches the services only.
func (h *Hub) BroadcastScoped(message *Message, ownerUserIDs []int64) {
	message.OwnerUserIDs = ownerUserIDs
	h.broadcast <- message
}
