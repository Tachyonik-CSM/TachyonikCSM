// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package actionrulewatcher keeps a running daemon current with AIManager's
// action rules over a WebSocket, so a rule added or edited in the UI takes
// effect without a restart. It also carries explicit requests to re-evaluate a
// rule, which is how the UI's "test this rule" path reaches the daemon.
//
// It reconnects on its own when the connection drops, and debounces per rule:
// saving a rule several times in quick succession triggers one reload rather
// than a burst of code generation.
//
// The connection itself lives in TachyonikLib's wswatcher, shared with the
// other managers' watchers. What is here is what is specific to action rules:
// which message types matter and how their payloads are read.
package actionrulewatcher

import (
	"encoding/json"
	"time"

	"tachyonik/lib/logger"
	"tachyonik/lib/wswatcher"
)

// Message type names AIManager broadcasts for action rules.
const (
	typeActionRuleCreated             = "ACTION_RULE_CREATED"
	typeActionRuleUpdated             = "ACTION_RULE_UPDATED"
	typeActionRuleDeleted             = "ACTION_RULE_DELETED"
	typeActionRuleEvaluationRequested = "ACTION_RULE_EVALUATION_REQUESTED"
	typeFeedImported                  = "FEED_IMPORTED"
)

// rulePayload extracts the rule ID from the WebSocket payload
type rulePayload struct {
	ID int64 `json:"id"`
}

// RuleChangeEvent describes what happened to an action rule
type RuleChangeEvent struct {
	Type   string // "created", "updated", "deleted"
	RuleID int64
}

// EvaluationRequestEvent describes a request to evaluate a rule for a specific user
type EvaluationRequestEvent struct {
	RuleID int64
	UserID int64
	Force  bool
}

// evaluationPayload extracts ruleId and userId from the WebSocket payload.
// Separate from EvaluationRequestEvent because this one is the wire shape, with
// the JSON tags that go with it.
type evaluationPayload struct {
	RuleID int64 `json:"ruleId"`
	UserID int64 `json:"userId"`
	Force  bool  `json:"force,omitempty"`
}

// Watcher dispatches AIManager's action-rule events to the registered
// callbacks. Only the per-rule handler is required, and it is the constructor's
// argument; the other two are optional and registered with a setter, so adding
// an event type does not grow New's signature for every caller that ignores it.
type Watcher struct {
	aiManagerURL          string
	internalServiceKey    string
	onRuleChange          func(event RuleChangeEvent)
	onEvaluationRequested func(event EvaluationRequestEvent)
	onFeedImported        func()

	ws *wswatcher.Watcher
	db *wswatcher.Debouncer

	// debounceFor lets a test drive the debounce without sleeping through it.
	debounceFor time.Duration
}

// New creates a new WebSocket watcher for AIManager action rule events
func New(aiManagerURL, internalServiceKey string, onRuleChange func(event RuleChangeEvent)) (*Watcher, error) {
	return &Watcher{
		aiManagerURL:       aiManagerURL,
		internalServiceKey: internalServiceKey,
		onRuleChange:       onRuleChange,
		debounceFor:        5 * time.Second,
	}, nil
}

// SetEvaluationRequestedHandler sets the callback fired when AIManager
// broadcasts a request to evaluate one rule for one user.
func (w *Watcher) SetEvaluationRequestedHandler(handler func(event EvaluationRequestEvent)) {
	w.onEvaluationRequested = handler
}

// SetFeedImportedHandler sets the callback fired when AIManager
// broadcasts FEED_IMPORTED. The handler should perform a full reload
// of the caller's in-memory state; it runs in its own goroutine so a
// slow reload cannot block the WS read loop.
func (w *Watcher) SetFeedImportedHandler(handler func()) {
	w.onFeedImported = handler
}

// Start opens the connection and begins dispatching.
func (w *Watcher) Start() {
	w.db = wswatcher.NewDebouncer(w.debounceFor, "actionrulewatcher")
	w.ws = wswatcher.New(wswatcher.Config{
		Name:       "AIManager",
		BaseURL:    w.aiManagerURL,
		ServiceKey: w.internalServiceKey,
		OnMessage:  w.dispatch,
	})
	w.ws.Start()
}

func (w *Watcher) dispatch(msg wswatcher.Message) {
	switch msg.Type {
	case typeActionRuleCreated:
		w.handleRuleEvent(msg, "created")
	case typeActionRuleUpdated:
		w.handleRuleEvent(msg, "updated")
	case typeActionRuleDeleted:
		w.handleRuleEvent(msg, "deleted")
	case typeActionRuleEvaluationRequested:
		w.handleEvaluationRequested(msg)
	case typeFeedImported:
		// Global signal — no payload, no debounce. Run in a goroutine so a
		// slow reload cannot block the read loop.
		if w.onFeedImported != nil {
			logger.Info("FEED_IMPORTED received, triggering full reload")
			go w.onFeedImported()
		}
	}
}

// handleRuleEvent extracts the rule ID and debounces the callback.
func (w *Watcher) handleRuleEvent(msg wswatcher.Message, eventType string) {
	if w.onRuleChange == nil {
		return
	}

	var payload rulePayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		logger.Debugf("Failed to parse action rule payload: %v", err)
		return
	}

	logger.Infof("Received %s event for action rule %d, scheduling regeneration...", msg.Type, payload.ID)
	event := RuleChangeEvent{Type: eventType, RuleID: payload.ID}
	w.db.Trigger(payload.ID, func() {
		logger.Infof("Triggering action rule change handler for rule %d (%s)...", event.RuleID, event.Type)
		w.onRuleChange(event)
	})
}

// handleEvaluationRequested dispatches an explicit "evaluate this rule for this
// user" request. Not debounced: it is a deliberate act by a person, and
// collapsing two of them would drop one they asked for.
func (w *Watcher) handleEvaluationRequested(msg wswatcher.Message) {
	var payload evaluationPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		logger.Debugf("Failed to parse evaluation request payload: %v", err)
		return
	}

	logger.Infof("Received evaluation request for rule %d, user %d", payload.RuleID, payload.UserID)
	if w.onEvaluationRequested != nil {
		w.onEvaluationRequested(EvaluationRequestEvent(payload))
	}
}

// Close stops the watcher. Safe to call more than once, and on a watcher that
// was never started.
func (w *Watcher) Close() error {
	if w.db != nil {
		w.db.Stop()
	}
	if w.ws == nil {
		return nil
	}
	return w.ws.Close()
}
