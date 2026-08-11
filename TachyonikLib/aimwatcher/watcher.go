// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package aimwatcher maintains a single WebSocket connection to AIManager and
// dispatches analysis-rule changes, bulk feed imports, and module-AI-setting
// updates to the handlers a caller registers (all optional — nil handlers make
// the corresponding events ignored). A service that only cares about its module
// AI settings sets just Handlers.OnModuleSettingChange; SourceAnalyser also uses
// OnRuleChange/OnFeedImported. On every (re)connect the watcher fires the
// feed-reload and module-setting resync handlers so a connection established
// after state was written pulls current state instead of waiting for a live event.
package aimwatcher

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"tachyonik/lib/internal/safehttp"
	"tachyonik/lib/logger"
)

const (
	// maxMessageBytes bounds a single inbound frame. gorilla's default read
	// limit is unlimited, so without this one oversized message is an OOM.
	// AIManager's own reads are capped at 512 bytes, so this is ample.
	maxMessageBytes = 1 << 20 // 1 MiB

	// readTimeout must exceed AIManager's ping period (54s — pongWait*9/10 in
	// its websocket client) with room for one missed ping. It turns a half-open
	// connection into a reconnect rather than a goroutine parked in ReadMessage
	// forever, silently missing every rule and settings update.
	readTimeout = 90 * time.Second

	// writeTimeout bounds a pong or close write against a peer that has stopped
	// reading.
	writeTimeout = 10 * time.Second

	// maxDebounceTimers caps the pending-rule map. Its keys come straight off
	// the wire, so an AIManager streaming events with distinct rule IDs would
	// otherwise allocate a timer per ID with nothing to stop it.
	maxDebounceTimers = 1024
)

// messageType represents the type of WebSocket message.
type messageType string

const (
	typeAnalysisRuleCreated    messageType = "ANALYSIS_RULE_CREATED"
	typeAnalysisRuleUpdated    messageType = "ANALYSIS_RULE_UPDATED"
	typeAnalysisRuleDeleted    messageType = "ANALYSIS_RULE_DELETED"
	typeFeedImported           messageType = "FEED_IMPORTED"
	typeModuleAISettingUpdated messageType = "MODULE_AI_SETTING_UPDATED"
	typePing                   messageType = "PING"
	typePong                   messageType = "PONG"
)

// message represents a WebSocket message. The fields stay exported so
// encoding/json can populate them; the type itself is internal to the watcher.
type message struct {
	Type    messageType     `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// rulePayload extracts the rule ID from an analysis rule event payload.
type rulePayload struct {
	ID int64 `json:"id"`
}

// settingPayload extracts the module name from a module AI setting event payload.
type settingPayload struct {
	ModuleName string `json:"moduleName"`
}

// RuleChangeEvent describes what happened to an analysis rule.
type RuleChangeEvent struct {
	Type   string // "created", "updated", "deleted"
	RuleID int64
}

// Handlers holds the callbacks the watcher dispatches to. Any callback may be
// nil, in which case the corresponding events are ignored.
type Handlers struct {
	// ModuleName filters MODULE_AI_SETTING_UPDATED events; only settings for
	// this module trigger OnModuleSettingChange.
	ModuleName string
	// OnRuleChange fires (debounced per rule ID) for analysis rule
	// create/update/delete events.
	OnRuleChange func(event RuleChangeEvent)
	// OnFeedImported fires on a bulk feed import — a global "everything was
	// replaced" signal the caller should respond to with a full reload.
	OnFeedImported func()
	// OnModuleSettingChange fires when this module's AI settings change.
	OnModuleSettingChange func()
}

// Watcher connects to the AIManager WebSocket and dispatches events to the
// registered handlers, reconnecting automatically on connection loss.
type Watcher struct {
	aiManagerURL       string
	internalServiceKey string
	h                  Handlers
	conn               *websocket.Conn
	done               chan struct{}
	closeOnce          sync.Once
	mu                 sync.Mutex
	reconnectDelay     time.Duration
	debounceTimers     map[int64]*time.Timer
}

// New creates a new AIManager WebSocket watcher.
func New(aiManagerURL string, internalServiceKey string, h Handlers) *Watcher {
	if safehttp.CredentialExposed(aiManagerURL, internalServiceKey != "") {
		logger.Warnf("AIManager watcher configured with an internal service key over a non-TLS URL (%s) — the key will be sent in cleartext", aiManagerURL)
	}
	return &Watcher{
		aiManagerURL:       aiManagerURL,
		internalServiceKey: internalServiceKey,
		h:                  h,
		done:               make(chan struct{}),
		reconnectDelay:     5 * time.Second,
		debounceTimers:     make(map[int64]*time.Timer),
	}
}

// getWebSocketURL converts the HTTP(S) AIManager URL to its ws(s):// /ws form.
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

// connect establishes a WebSocket connection.
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

	conn.SetReadLimit(maxMessageBytes)
	conn.SetReadDeadline(time.Now().Add(readTimeout))
	// Replaces gorilla's default ping handler, which replies with a pong but
	// does not extend the read deadline — without the extension a connection
	// carrying nothing but pings would time out.
	conn.SetPingHandler(func(appData string) error {
		conn.SetReadDeadline(time.Now().Add(readTimeout))
		conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		if err := conn.WriteMessage(websocket.PongMessage, []byte(appData)); err != nil && err != websocket.ErrCloseSent {
			return err
		}
		return nil
	})

	w.mu.Lock()
	w.conn = conn
	w.mu.Unlock()

	logger.Info("Connected to AIManager WebSocket for analysis rule and module AI setting events")
	return nil
}

// Start starts the WebSocket connection and message handling.
func (w *Watcher) Start() {
	go w.run()
}

// run handles the connection lifecycle with automatic reconnection.
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

		// Resync on every (re)connect. A connection established before the
		// rules/settings existed (e.g. before a feed import at install time),
		// or re-established after an AIManager restart, must pull current state
		// rather than wait for the next live event. Treat a fresh connection
		// like a full reload plus a settings refresh. Run in goroutines so a
		// slow reload can't block the read loop.
		if w.h.OnFeedImported != nil {
			logger.Info("WebSocket (re)connected — triggering full reload to resync analysis rules")
			go w.h.OnFeedImported()
		}
		if w.h.OnModuleSettingChange != nil {
			logger.Infof("WebSocket (re)connected — resyncing module AI settings for %s", w.h.ModuleName)
			go w.h.OnModuleSettingChange()
		}

		w.handleMessages()
		logger.Warn("AIManager WebSocket connection lost, reconnecting...")
	}
}

// handleMessages reads and processes messages from the WebSocket.
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

		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logger.Errorf("AIManager WebSocket read error: %v", err)
			}
			return
		}
		conn.SetReadDeadline(time.Now().Add(readTimeout))

		var msg message
		if err := json.Unmarshal(raw, &msg); err != nil {
			logger.Debugf("Failed to parse WebSocket message: %v", err)
			continue
		}

		switch msg.Type {
		case typeAnalysisRuleCreated:
			w.handleRuleEvent(msg, "created")
		case typeAnalysisRuleUpdated:
			w.handleRuleEvent(msg, "updated")
		case typeAnalysisRuleDeleted:
			w.handleRuleEvent(msg, "deleted")
		case typeFeedImported:
			// Global signal — no payload to parse, no per-rule debounce.
			// Hand off to the caller's reload closure in a goroutine so a
			// slow reload doesn't block the WS read loop.
			if w.h.OnFeedImported != nil {
				logger.Info("FEED_IMPORTED received, triggering full reload")
				go w.h.OnFeedImported()
			}
		case typeModuleAISettingUpdated:
			w.handleModuleSettingEvent(msg)
		case typePing:
			pongMsg := message{Type: typePong}
			if data, err := json.Marshal(pongMsg); err == nil {
				w.mu.Lock()
				if w.conn != nil {
					w.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
					w.conn.WriteMessage(websocket.TextMessage, data)
				}
				w.mu.Unlock()
			}
		default:
			// Ignore other message types
		}
	}
}

// handleRuleEvent extracts the rule ID and debounces the callback.
func (w *Watcher) handleRuleEvent(msg message, eventType string) {
	if w.h.OnRuleChange == nil {
		return
	}

	var payload rulePayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		logger.Debugf("Failed to parse analysis rule payload: %v", err)
		return
	}

	logger.Infof("Received %s event for analysis rule %d, scheduling regeneration...", msg.Type, payload.ID)
	w.debounceRuleChange(RuleChangeEvent{Type: eventType, RuleID: payload.ID})
}

// handleModuleSettingEvent triggers the module-setting callback when the event
// targets the watched module.
func (w *Watcher) handleModuleSettingEvent(msg message) {
	if w.h.OnModuleSettingChange == nil {
		return
	}

	var payload settingPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		logger.Debugf("Failed to parse module AI setting payload: %v", err)
		return
	}
	if payload.ModuleName == w.h.ModuleName {
		logger.Infof("Module AI setting updated for %s, triggering handler...", w.h.ModuleName)
		w.h.OnModuleSettingChange()
	}
}

// debounceRuleChange triggers the rule-change callback with a 5-second per-rule debounce.
func (w *Watcher) debounceRuleChange(event RuleChangeEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Close nils the map; a message still in flight at that moment must not
	// assign into it (writing to a nil map panics).
	if w.debounceTimers == nil {
		return
	}

	timer, exists := w.debounceTimers[event.RuleID]
	if exists {
		timer.Stop()
	} else if len(w.debounceTimers) >= maxDebounceTimers {
		logger.Warnf("aimwatcher: %d rule changes already pending — dropping the change for rule %d", len(w.debounceTimers), event.RuleID)
		return
	}

	w.debounceTimers[event.RuleID] = time.AfterFunc(5*time.Second, func() {
		logger.Infof("Triggering analysis rule change handler for rule %d (%s)...", event.RuleID, event.Type)
		w.h.OnRuleChange(event)

		w.mu.Lock()
		delete(w.debounceTimers, event.RuleID)
		w.mu.Unlock()
	})
}

// Close stops the watcher and closes the WebSocket connection. It is safe to
// call more than once — a second bare close(w.done) would panic, and a shutdown
// path that both defers Close and calls it explicitly is easy to write.
// Subsequent calls are no-ops and return nil.
func (w *Watcher) Close() error {
	var err error

	w.closeOnce.Do(func() {
		close(w.done)

		w.mu.Lock()
		defer w.mu.Unlock()

		// Stop all debounce timers
		for _, timer := range w.debounceTimers {
			timer.Stop()
		}
		w.debounceTimers = nil

		if w.conn != nil {
			w.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			w.conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			err = w.conn.Close()
			w.conn = nil
		}
	})

	return err
}
