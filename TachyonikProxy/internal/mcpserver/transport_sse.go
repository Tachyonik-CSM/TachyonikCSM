// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"

	"tachyonik/lib/logger"
)

// SSETransport implements the MCP SSE+HTTP transport.
// - GET /mcp/sse — Server-Sent Events stream (server→client)
// - POST /mcp/message — JSON-RPC requests (client→server)
type SSETransport struct {
	server  *Server
	clients map[string]chan []byte
	mu      sync.RWMutex
	nextID  int
}

// NewSSETransport creates a new SSE transport for the given MCP server.
func NewSSETransport(server *Server) *SSETransport {
	return &SSETransport{
		server:  server,
		clients: make(map[string]chan []byte),
	}
}

// HandleSSE handles GET /mcp/sse — establishes an SSE connection.
func (t *SSETransport) HandleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	t.mu.Lock()
	t.nextID++
	clientID := fmt.Sprintf("client-%d", t.nextID)
	ch := make(chan []byte, 64)
	t.clients[clientID] = ch
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		delete(t.clients, clientID)
		close(ch)
		t.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Send the endpoint event so the client knows where to POST messages
	fmt.Fprintf(w, "event: endpoint\ndata: /mcp/message?sessionId=%s\n\n", clientID)
	flusher.Flush()

	logger.Infof("SSE client connected: %s", clientID)

	// Stream responses back to client
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			logger.Infof("SSE client disconnected: %s", clientID)
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(msg))
			flusher.Flush()
		}
	}
}

// HandleMessage handles POST /mcp/message — receives JSON-RPC requests.
func (t *SSETransport) HandleMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("sessionId")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	resp := t.server.HandleRequest(body)
	if resp == nil {
		// Notification — no response needed
		w.WriteHeader(http.StatusAccepted)
		return
	}

	respJSON, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "Failed to marshal response", http.StatusInternalServerError)
		return
	}

	// If there's an SSE session, send via SSE channel
	if sessionID != "" {
		t.mu.RLock()
		ch, ok := t.clients[sessionID]
		t.mu.RUnlock()

		if ok {
			select {
			case ch <- respJSON:
			default:
				logger.Warnf("SSE channel full for session %s, dropping message", sessionID)
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
	}

	// Fallback: return response directly in HTTP body
	w.Header().Set("Content-Type", "application/json")
	w.Write(respJSON)
}

// HandleDirect handles POST /mcp/message without SSE — direct JSON-RPC over HTTP.
// This is useful for simple tool calls without maintaining an SSE session.
func (t *SSETransport) HandleDirect(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	resp := t.server.HandleRequest(body)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
