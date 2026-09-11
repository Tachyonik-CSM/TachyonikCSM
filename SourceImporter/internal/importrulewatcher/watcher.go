// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package importrulewatcher keeps a running daemon current with AIManager's
// import rules over a WebSocket, so a rule added or edited in the UI takes
// effect without a restart.
//
// It reconnects on its own when the connection drops, and debounces per rule:
// saving a rule several times in quick succession triggers one reload rather
// than a burst of code generation.
package importrulewatcher

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"tachyonik/lib/logger"
)

// MessageType represents the type of WebSocket message
type MessageType string

const (
	TypeImportRuleCreated MessageType = "IMPORT_RULE_CREATED"
	TypeImportRuleUpdated MessageType = "IMPORT_RULE_UPDATED"
	TypeImportRuleDeleted MessageType = "IMPORT_RULE_DELETED"
	TypeFeedImported      MessageType = "FEED_IMPORTED"
	TypePing              MessageType = "PING"
	TypePong              MessageType = "PONG"
)

// Message represents a WebSocket message
type Message struct {
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// rulePayload extracts the rule ID from the WebSocket payload
type rulePayload struct {
	ID int64 `json:"id"`
}

// RuleChangeEvent describes what happened to an import rule
type RuleChangeEvent struct {
	Type   string // "created", "updated", "deleted"
	RuleID int64
}

// Watcher connects to AIManager WebSocket and triggers callbacks on
// import rule changes and bulk feed imports. See analysisrulewatcher in
// SourceAnalyser for the rationale behind separating the two callbacks.
type Watcher struct {
	aiManagerURL       string
	internalServiceKey string
	onRuleChange       func(event RuleChangeEvent)
	onFeedImported     func()
	conn               *websocket.Conn
	done               chan struct{}
	mu                 sync.Mutex
	reconnectDelay     time.Duration
	debounceTimers     map[int64]*time.Timer
}

// New creates a new WebSocket watcher for AIManager import rule events.
// onFeedImported may be nil — FEED_IMPORTED messages are then ignored.
func New(aiManagerURL, internalServiceKey string, onRuleChange func(event RuleChangeEvent), onFeedImported func()) (*Watcher, error) {
	return &Watcher{
		aiManagerURL:       aiManagerURL,
		internalServiceKey: internalServiceKey,
		onRuleChange:       onRuleChange,
		onFeedImported:     onFeedImported,
		done:               make(chan struct{}),
		reconnectDelay:     5 * time.Second,
		debounceTimers:     make(map[int64]*time.Timer),
	}, nil
}

// getWebSocketURL converts HTTP URL to WebSocket URL
func (w *Watcher) getWebSocketURL() string {
	wsURL := w.aiManagerURL

	if strings.HasPrefix(wsURL, "https://") {
		wsURL = "wss://" + strings.TrimPrefix(wsURL, "https://")
	} else if strings.HasPrefix(wsURL, "http://") {
		wsURL = "ws://" + strings.TrimPrefix(wsURL, "http://")
	}

	u, err := url.Parse(wsURL)
	if err != nil {
		return wsURL + "/ws"
	}
	u.Path = "/ws"
	return u.String()
}

// connect establishes a WebSocket connection
func (w *Watcher) connect() error {
	wsURL := w.getWebSocketURL()
	logger.Infof("Connecting to AIManager WebSocket: %s", wsURL)

	// Authenticate to AIManager's WebSocket with the shared internal-service key. (SECURITY: CRIT-3)
	header := http.Header{}
	if w.internalServiceKey != "" {
		header.Set("X-Internal-Service-Key", w.internalServiceKey)
	}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		return err
	}

	w.mu.Lock()
	w.conn = conn
	w.mu.Unlock()

	logger.Info("Connected to AIManager WebSocket for import rule change events")
	return nil
}

// Start starts the WebSocket connection and message handling
func (w *Watcher) Start() {
	go w.run()
}

// run handles the connection lifecycle with automatic reconnection
func (w *Watcher) run() {
	for {
		select {
		case <-w.done:
			return
		default:
		}

		if err := w.connect(); err != nil {
			logger.Errorf("Failed to connect to AIManager WebSocket: %v", err)
			logger.Infof("Retrying in %v...", w.reconnectDelay)

			select {
			case <-w.done:
				return
			case <-time.After(w.reconnectDelay):
				continue
			}
		}

		// Resync the full rule set on every (re)connect. Without this a daemon
		// that connected before the rules existed (e.g. before a feed import at
		// install time) — or that lost its connection when AIManager restarted —
		// would keep serving stale state until the next live event happened to
		// arrive. Treat a fresh connection like a FEED_IMPORTED. Run in a
		// goroutine so a slow reload can't block the read loop.
		if w.onFeedImported != nil {
			logger.Info("WebSocket (re)connected — triggering full reload to resync rules")
			go w.onFeedImported()
		}

		w.handleMessages()
		logger.Warn("AIManager WebSocket connection lost, reconnecting...")
	}
}

// handleMessages reads and processes messages from the WebSocket
func (w *Watcher) handleMessages() {
	for {
		select {
		case <-w.done:
			return
		default:
		}

		w.mu.Lock()
		conn := w.conn
		w.mu.Unlock()

		if conn == nil {
			return
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logger.Errorf("AIManager WebSocket read error: %v", err)
			}
			return
		}

		var msg Message
		if err := json.Unmarshal(message, &msg); err != nil {
			logger.Debugf("Failed to parse WebSocket message: %v", err)
			continue
		}

		switch msg.Type {
		case TypeImportRuleCreated:
			w.handleRuleEvent(msg, "created")
		case TypeImportRuleUpdated:
			w.handleRuleEvent(msg, "updated")
		case TypeImportRuleDeleted:
			w.handleRuleEvent(msg, "deleted")
		case TypeFeedImported:
			if w.onFeedImported != nil {
				logger.Info("FEED_IMPORTED received, triggering full reload")
				go w.onFeedImported()
			}
		case TypePing:
			pongMsg := Message{Type: TypePong}
			if data, err := json.Marshal(pongMsg); err == nil {
				w.mu.Lock()
				if w.conn != nil {
					w.conn.WriteMessage(websocket.TextMessage, data)
				}
				w.mu.Unlock()
			}
		default:
			// Ignore other message types (SOURCE_CREATED, etc.)
		}
	}
}

// handleRuleEvent extracts the rule ID and debounces the callback
func (w *Watcher) handleRuleEvent(msg Message, eventType string) {
	var payload rulePayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		logger.Debugf("Failed to parse import rule payload: %v", err)
		return
	}

	logger.Infof("Received %s event for import rule %d, scheduling regeneration...", msg.Type, payload.ID)
	w.debounceRuleChange(RuleChangeEvent{Type: eventType, RuleID: payload.ID})
}

// debounceRuleChange triggers the callback with a 5-second per-rule debounce
func (w *Watcher) debounceRuleChange(event RuleChangeEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if timer, exists := w.debounceTimers[event.RuleID]; exists {
		timer.Stop()
	}

	w.debounceTimers[event.RuleID] = time.AfterFunc(5*time.Second, func() {
		logger.Infof("Triggering import rule change handler for rule %d (%s)...", event.RuleID, event.Type)
		w.onRuleChange(event)

		w.mu.Lock()
		delete(w.debounceTimers, event.RuleID)
		w.mu.Unlock()
	})
}

// Close stops the watcher and closes the WebSocket connection
func (w *Watcher) Close() error {
	close(w.done)

	w.mu.Lock()
	defer w.mu.Unlock()

	// Stop all debounce timers
	for _, timer := range w.debounceTimers {
		timer.Stop()
	}
	w.debounceTimers = nil

	if w.conn != nil {
		w.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		err := w.conn.Close()
		w.conn = nil
		return err
	}

	return nil
}
