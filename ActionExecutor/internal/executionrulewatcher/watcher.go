// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package executionrulewatcher keeps a running daemon current with AIManager's
// execution rules over a WebSocket, so a rule added or edited in the UI takes
// effect without a restart.
//
// It debounces per rule: saving a rule several times in quick succession
// triggers one reload rather than a burst of code generation. It also hands on
// a user's request to run a rule now (EXECUTION_REQUESTED), never debounced —
// every press is its own request — and reports each (re)connect, so requests
// made while the connection was down are picked up. The connection
// itself — dialling with the service key, reconnecting, the read limit and
// deadline, answering PING — and the debouncer are tachyonik/lib/wswatcher's,
// as in ActionGenerator's watchers.
package executionrulewatcher

import (
	"encoding/json"
	"time"

	"tachyonik/lib/logger"
	"tachyonik/lib/wswatcher"
)

const (
	typeExecutionRuleCreated = "EXECUTION_RULE_CREATED"
	typeExecutionRuleUpdated = "EXECUTION_RULE_UPDATED"
	typeExecutionRuleDeleted = "EXECUTION_RULE_DELETED"
	typeFeedImported         = "FEED_IMPORTED"
	typeExecutionRequested   = "EXECUTION_REQUESTED"
)

// RunRequest is a user's request to run an execution rule now.
type RunRequest struct {
	ID              int64  `json:"id"`
	ExecutionRuleID int64  `json:"executionRuleId"`
	UserID          int64  `json:"userId"`
	UserRole        string `json:"userRole"`
}

type rulePayload struct {
	ID int64 `json:"id"`
}

// RuleChangeEvent describes what happened to an execution rule.
type RuleChangeEvent struct {
	Type   string // "created", "updated", "deleted"
	RuleID int64
}

// Watcher connects to AIManager WebSocket and triggers callbacks on
// execution rule changes and bulk feed imports. Per-rule events are
// debounced; the global FEED_IMPORTED reload signal is not.
type Watcher struct {
	onRuleChange   func(event RuleChangeEvent)
	onFeedImported func()
	onRunRequested func(RunRequest)
	onConnect      func()

	ws *wswatcher.Watcher
	db *wswatcher.Debouncer

	// debounceFor lets a test drive the debounce without sleeping through it.
	debounceFor time.Duration
}

// New creates a new WebSocket watcher for AIManager execution rule events.
// onFeedImported may be nil — FEED_IMPORTED messages are then ignored.
func New(aiManagerURL, internalServiceKey string, onRuleChange func(event RuleChangeEvent), onFeedImported func()) (*Watcher, error) {
	w := &Watcher{
		onRuleChange:   onRuleChange,
		onFeedImported: onFeedImported,
		debounceFor:    5 * time.Second,
	}
	w.ws = wswatcher.New(wswatcher.Config{
		Name:       "AIManager",
		BaseURL:    aiManagerURL,
		ServiceKey: internalServiceKey,
		OnMessage:  w.dispatch,
		OnConnect: func() {
			if w.onConnect != nil {
				w.onConnect()
			}
		},
	})
	return w, nil
}

// SetRunRequestHandler sets the callback for a user's request to run a rule
// now. It is called in its own goroutine, so a run never blocks the read loop.
func (w *Watcher) SetRunRequestHandler(handler func(RunRequest)) {
	w.onRunRequested = handler
}

// SetConnectHandler sets the callback fired on every successful (re)connect,
// in its own goroutine — the moment to pick up what was missed meanwhile.
func (w *Watcher) SetConnectHandler(handler func()) {
	w.onConnect = handler
}

// Start opens the connection and begins dispatching.
func (w *Watcher) Start() {
	w.db = wswatcher.NewDebouncer(w.debounceFor, "executionrulewatcher")
	w.ws.Start()
}

func (w *Watcher) dispatch(msg wswatcher.Message) {
	switch msg.Type {
	case typeExecutionRuleCreated:
		w.handleRuleEvent(msg, "created")
	case typeExecutionRuleUpdated:
		w.handleRuleEvent(msg, "updated")
	case typeExecutionRuleDeleted:
		w.handleRuleEvent(msg, "deleted")
	case typeExecutionRequested:
		var req RunRequest
		if err := json.Unmarshal(msg.Payload, &req); err != nil || req.ID == 0 {
			logger.Debugf("Failed to parse execution request payload: %v", err)
			return
		}
		logger.Infof("Received request %d to run execution rule %d for user %d", req.ID, req.ExecutionRuleID, req.UserID)
		if w.onRunRequested != nil {
			go w.onRunRequested(req)
		}
	case typeFeedImported:
		// Global signal — no payload, no debounce. Run in a goroutine so a
		// slow reload cannot block the read loop.
		if w.onFeedImported != nil {
			logger.Info("FEED_IMPORTED received, triggering full reload")
			go w.onFeedImported()
		}
	}
}

func (w *Watcher) handleRuleEvent(msg wswatcher.Message, eventType string) {
	if w.onRuleChange == nil {
		return
	}
	var payload rulePayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		logger.Debugf("Failed to parse execution rule payload: %v", err)
		return
	}

	logger.Infof("Received %s event for execution rule %d, scheduling regeneration...", msg.Type, payload.ID)
	event := RuleChangeEvent{Type: eventType, RuleID: payload.ID}
	w.db.Trigger(payload.ID, func() {
		logger.Infof("Triggering execution rule change handler for rule %d (%s)...", event.RuleID, event.Type)
		w.onRuleChange(event)
	})
}

// Close stops the watcher. Safe to call more than once, and on a watcher that
// was never started.
func (w *Watcher) Close() error {
	if w.db != nil {
		w.db.Stop()
	}
	return w.ws.Close()
}
