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
//
// The connection itself — dialling, reconnecting, read limits and deadlines,
// PING/PONG — lives in wswatcher, which the other managers' watchers share.
// What is here is only what is specific to AIManager: which message types
// matter, how their payloads are read, and the per-rule debounce.
package aimwatcher

import (
	"encoding/json"
	"time"

	"tachyonik/lib/logger"
	"tachyonik/lib/wswatcher"
)

// Message type names AIManager broadcasts.
const (
	typeAnalysisRuleCreated    = "ANALYSIS_RULE_CREATED"
	typeAnalysisRuleUpdated    = "ANALYSIS_RULE_UPDATED"
	typeAnalysisRuleDeleted    = "ANALYSIS_RULE_DELETED"
	typeFeedImported           = "FEED_IMPORTED"
	typeModuleAISettingUpdated = "MODULE_AI_SETTING_UPDATED"
)

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
	h  Handlers
	ws *wswatcher.Watcher
	db *wswatcher.Debouncer

	// reconnectDelay and debounceFor exist so a test can drive the loop
	// without sleeping through it. Nothing outside this package sets them;
	// they are read in Start, where the underlying watcher is built.
	reconnectDelay time.Duration
	debounceFor    time.Duration

	aiManagerURL       string
	internalServiceKey string
}

// New creates a new AIManager WebSocket watcher.
func New(aiManagerURL string, internalServiceKey string, h Handlers) *Watcher {
	return &Watcher{
		h:                  h,
		reconnectDelay:     wswatcher.DefaultReconnectDelay,
		debounceFor:        5 * time.Second,
		aiManagerURL:       aiManagerURL,
		internalServiceKey: internalServiceKey,
	}
}

// Start starts the WebSocket connection and message handling.
func (w *Watcher) Start() {
	w.db = wswatcher.NewDebouncer(w.debounceFor, "aimwatcher")
	w.ws = wswatcher.New(wswatcher.Config{
		Name:           "AIManager",
		BaseURL:        w.aiManagerURL,
		ServiceKey:     w.internalServiceKey,
		ReconnectDelay: w.reconnectDelay,
		OnConnect:      w.resync,
		OnMessage:      w.dispatch,
	})
	w.ws.Start()
}

// resync treats a fresh connection as a full reload plus a settings refresh.
// See the package comment: a connection made before the state existed, or
// remade after an AIManager restart, has missed whatever happened in between.
func (w *Watcher) resync() {
	if w.h.OnFeedImported != nil {
		logger.Info("WebSocket (re)connected — triggering full reload to resync analysis rules")
		go w.h.OnFeedImported()
	}
	if w.h.OnModuleSettingChange != nil {
		logger.Infof("WebSocket (re)connected — resyncing module AI settings for %s", w.h.ModuleName)
		go w.h.OnModuleSettingChange()
	}
}

func (w *Watcher) dispatch(msg wswatcher.Message) {
	switch msg.Type {
	case typeAnalysisRuleCreated:
		w.handleRuleEvent(msg, "created")
	case typeAnalysisRuleUpdated:
		w.handleRuleEvent(msg, "updated")
	case typeAnalysisRuleDeleted:
		w.handleRuleEvent(msg, "deleted")
	case typeFeedImported:
		// Global signal — no payload to parse, no per-rule debounce. Hand off
		// to the caller's reload closure in a goroutine so a slow reload
		// doesn't block the WS read loop.
		if w.h.OnFeedImported != nil {
			logger.Info("FEED_IMPORTED received, triggering full reload")
			go w.h.OnFeedImported()
		}
	case typeModuleAISettingUpdated:
		w.handleModuleSettingEvent(msg)
	}
}

// handleRuleEvent extracts the rule ID and debounces the callback.
func (w *Watcher) handleRuleEvent(msg wswatcher.Message, eventType string) {
	if w.h.OnRuleChange == nil {
		return
	}

	var payload rulePayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		logger.Debugf("Failed to parse analysis rule payload: %v", err)
		return
	}

	logger.Infof("Received %s event for analysis rule %d, scheduling regeneration...", msg.Type, payload.ID)
	event := RuleChangeEvent{Type: eventType, RuleID: payload.ID}
	w.db.Trigger(payload.ID, func() {
		logger.Infof("Triggering analysis rule change handler for rule %d (%s)...", event.RuleID, event.Type)
		w.h.OnRuleChange(event)
	})
}

// handleModuleSettingEvent triggers the module-setting callback when the event
// targets the watched module.
func (w *Watcher) handleModuleSettingEvent(msg wswatcher.Message) {
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

// Close stops the watcher and closes the WebSocket connection. It is safe to
// call more than once, and on a watcher that was never started.
func (w *Watcher) Close() error {
	if w.db != nil {
		w.db.Stop()
	}
	if w.ws == nil {
		return nil
	}
	return w.ws.Close()
}

// getWebSocketURL reports the address this watcher dials. Retained for this
// package's own tests; the derivation itself lives in wswatcher.
func (w *Watcher) getWebSocketURL() string {
	return wswatcher.New(wswatcher.Config{BaseURL: w.aiManagerURL}).URL()
}
