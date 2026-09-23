// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package watcher wakes the daemon when the data its rules reason about
// changes, over the /ws endpoint any manager exposes to internal services.
//
// There is no periodic sweep to fall back on: an asset appearing or a system
// setting changing is what triggers a fresh evaluation, so an event this
// package does not subscribe to is a change no rule ever sees. Each connection
// subscribes to its own set of message types, and reconnects on its own when it
// drops.
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
	TypeAssetCreated         MessageType = "ASSET_CREATED"
	TypeAssetUpdated         MessageType = "ASSET_UPDATED"
	TypeAssetDeleted         MessageType = "ASSET_DELETED"
	TypeVulnerabilityCreated MessageType = "VULNERABILITY_CREATED"
	TypeVulnerabilityUpdated MessageType = "VULNERABILITY_UPDATED"
	TypeVulnerabilityDeleted MessageType = "VULNERABILITY_DELETED"

	// A rule's context is not only assets: it reads the organisation record
	// and the user's own settings, and neither produces an AssetManager event.
	// Without these a rule that had matched went on matching after the thing it
	// tested had already moved on — an action saying no Research provider was
	// configured survived configuring one.
	TypeUserCreated         MessageType = "USER_CREATED"
	TypeUserUpdated         MessageType = "USER_UPDATED"
	TypeUserDeleted         MessageType = "USER_DELETED"
	TypeOrganisationUpdated MessageType = "ORGANISATION_UPDATED"
)

// DefaultTriggers is what the AssetManager connection reacts to.
var DefaultTriggers = []MessageType{
	TypeAssetCreated, TypeAssetUpdated, TypeAssetDeleted,
	TypeVulnerabilityCreated, TypeVulnerabilityUpdated, TypeVulnerabilityDeleted,
}

// SystemTriggers is what the SystemManager connection reacts to.
//
// USER_UPDATED is the broad one: SystemManager emits it for a role change, a
// quota change and — since preference writes stopped being silent — for a
// change to the user's own settings. The daemon cannot tell which from the
// event, and does not need to: it re-reads the context either way.
var SystemTriggers = []MessageType{
	TypeUserCreated, TypeUserUpdated, TypeUserDeleted, TypeOrganisationUpdated,
}

// Watcher reacts to one manager's events by calling onChange.
type Watcher struct {
	name       string
	baseURL    string
	serviceKey string
	triggers   map[MessageType]bool
	onChange   func()

	ws *wswatcher.Watcher
}

// New creates a watcher for AssetManager's asset and vulnerability events.
func New(assetManagerURL, internalServiceKey string, onChange func()) (*Watcher, error) {
	return NewFor("AssetManager", assetManagerURL, internalServiceKey, DefaultTriggers, onChange)
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
// every one of them means the context may have moved, and the answer is always
// to read it again.
func (w *Watcher) dispatch(msg wswatcher.Message) {
	if w.onChange == nil || !w.triggers[MessageType(msg.Type)] {
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
