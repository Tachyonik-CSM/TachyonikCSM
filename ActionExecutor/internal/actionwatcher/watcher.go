// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package actionwatcher subscribes to ActionManager over a WebSocket and reports
// each newly created action, which is what gives the auto-execute path its
// trigger.
//
// The connection itself — dialling with the service key, reconnecting, the
// read limit and deadline, answering PING — is tachyonik/lib/wswatcher's. What
// stays here is which message matters and what it carries.
package actionwatcher

import (
	"encoding/json"

	"tachyonik/lib/logger"
	"tachyonik/lib/wswatcher"
)

const typeActionCreated = "ACTION_CREATED"

// Action represents the action payload broadcast by ActionManager.
type Action struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"userId"`
	Title        string `json:"title"`
	Type         string `json:"type"`
	Status       string `json:"status"`
	ActionRuleID *int64 `json:"actionRuleId"`
}

// Watcher connects to ActionManager's WebSocket and triggers a callback on ACTION_CREATED events.
type Watcher struct {
	onActionCreated func(action Action)
	ws              *wswatcher.Watcher
}

// New creates a new WebSocket watcher for ActionManager action events. The
// callback runs on the read loop; a caller doing slow work hands it off.
func New(actionManagerURL, internalServiceKey string, onActionCreated func(action Action)) *Watcher {
	w := &Watcher{onActionCreated: onActionCreated}
	w.ws = wswatcher.New(wswatcher.Config{
		Name:       "ActionManager",
		BaseURL:    actionManagerURL,
		ServiceKey: internalServiceKey,
		OnMessage:  w.dispatch,
	})
	return w
}

// Start begins the watcher in a background goroutine.
func (w *Watcher) Start() { w.ws.Start() }

// Close shuts down the watcher. Safe to call more than once.
func (w *Watcher) Close() error { return w.ws.Close() }

func (w *Watcher) dispatch(msg wswatcher.Message) {
	if msg.Type != typeActionCreated {
		return
	}
	var action Action
	if err := json.Unmarshal(msg.Payload, &action); err != nil {
		logger.Debugf("Failed to parse action payload: %v", err)
		return
	}
	logger.Debugf("Received ACTION_CREATED for action %d (user %d, ruleID=%v)",
		action.ID, action.UserID, action.ActionRuleID)
	w.onActionCreated(action)
}
