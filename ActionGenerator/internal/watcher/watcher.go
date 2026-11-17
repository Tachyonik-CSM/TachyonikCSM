// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package watcher wakes the daemon when the data its rules reason about
// changes, over the /ws endpoint any manager exposes to internal services.
//
// There is no periodic sweep to fall back on: a change message is what
// triggers a fresh evaluation, so a message this package does not react to is
// a change no rule ever sees. Which messages matter follows from the routines:
// the x-invalidated-by of every API operation an active routine calls
// (PA-RT-07), decided when the message arrives (SetExtraTriggers). The only
// fixed triggers are SystemManager's users appearing and going, which change
// whom the rules are evaluated for rather than what a routine reads. Every
// connection also re-evaluates when it is (re)established, and reconnects on
// its own when it drops.
//
// The connection itself lives in TachyonikLib's wswatcher, shared with the
// rule watchers. What is here is the part that differs: which events matter to
// this daemon, and that any of them means the same thing — re-evaluate.
package watcher

import (
	"tachyonik/lib/logger"
	"tachyonik/lib/wswatcher"
)

// MessageType represents the type of WebSocket message
type MessageType string

const (
	TypeUserCreated MessageType = "USER_CREATED"
	TypeUserDeleted MessageType = "USER_DELETED"
)

// SystemTriggers is what the SystemManager connection reacts to whatever the
// routines read: a user created or deleted changes whom the rules are
// evaluated for (AG-EVAL-06), which no API operation's answer announces.
var SystemTriggers = []MessageType{TypeUserCreated, TypeUserDeleted}

// Watcher reacts to one manager's events by calling onChange.
type Watcher struct {
	name       string
	baseURL    string
	serviceKey string
	triggers   map[MessageType]bool
	onChange   func()
	// extra decides about message types the fixed set does not name: those
	// the API catalogue lists as invalidating what an active routine reads
	// (PA-RT-07). Nil: none.
	extra func(messageType string) bool

	ws *wswatcher.Watcher
}

// NewFor creates a watcher for any manager, reacting to the named triggers.
func NewFor(name, baseURL, internalServiceKey string, triggers []MessageType, onChange func()) (*Watcher, error) {
	set := make(map[MessageType]bool, len(triggers))
	for _, t := range triggers {
		set[t] = true
	}
	return &Watcher{
		name:       name,
		baseURL:    baseURL,
		serviceKey: internalServiceKey,
		triggers:   set,
		onChange:   onChange,
	}, nil
}

// SetExtraTriggers adds message types decided at the time a message
// arrives, from the API catalogue and the active routines. Call before Start.
func (w *Watcher) SetExtraTriggers(extra func(messageType string) bool) {
	w.extra = extra
}

// Start opens the connection and begins reacting to events.
func (w *Watcher) Start() {
	w.ws = wswatcher.New(wswatcher.Config{
		Name:       w.name,
		BaseURL:    w.baseURL,
		ServiceKey: w.serviceKey,
		OnConnect:  w.resync,
		OnMessage:  w.dispatch,
	})
	w.ws.Start()
}

// resync re-evaluates on every (re)connect, so a change that happened while
// disconnected — an asset created during an AssetManager restart — is not
// missed until the next live event.
func (w *Watcher) resync() {
	if w.onChange != nil {
		logger.Infof("%s WebSocket (re)connected — re-evaluating against current data", w.name)
		w.onChange()
	}
}

// dispatch reacts to any subscribed event. Which one it was does not matter:
// every one of them means what a routine reads may have moved, and the answer
// is always to evaluate again.
func (w *Watcher) dispatch(msg wswatcher.Message) {
	if w.onChange == nil {
		return
	}
	if !w.triggers[MessageType(msg.Type)] && (w.extra == nil || !w.extra(msg.Type)) {
		return
	}
	logger.Debugf("%s: received %s event", w.name, msg.Type)
	w.onChange()
}

// Close stops the watcher. Safe to call more than once, and on a watcher that
// was never started.
func (w *Watcher) Close() error {
	if w.ws == nil {
		return nil
	}
	return w.ws.Close()
}
