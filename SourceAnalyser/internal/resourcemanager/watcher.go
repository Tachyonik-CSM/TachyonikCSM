// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The watcher of ResourceManager's source messages. ResourceManager announces
// every new source (SOURCE_CREATED) and every change to one (SOURCE_UPDATED)
// over its /ws, with the whole source as payload. A source announced with
// status "Analysing" is one waiting for SourceAnalyser — a new upload, URL or
// tool result, a test source, or one set back to be analysed again — and is
// handed on by id (SA-ANA-01). Everything else is ignored, SourceAnalyser's own
// results included, so writing a result causes no loop.
//
// Messages sent while the connection is down are lost, so every (re)connect
// calls onConnect, on which the daemon goes through the whole source list
// (SA-ANA-19). The connection itself is TachyonikLib's wswatcher.

package resourcemanager

import (
	"encoding/json"

	"tachyonik/lib/logger"
	"tachyonik/lib/wswatcher"
)

// Watcher hands on the sources ResourceManager announces as waiting.
type Watcher struct {
	ws *wswatcher.Watcher
}

// NewWatcher connects to ResourceManager at baseURL. onAnalysing receives the
// id of every source announced with status "Analysing"; onConnect runs on
// every (re)connect. Neither may block for long: onAnalysing runs on the read
// loop.
func NewWatcher(baseURL, serviceKey string, onAnalysing func(id int64), onConnect func()) *Watcher {
	return &Watcher{ws: wswatcher.New(wswatcher.Config{
		Name:       "ResourceManager",
		BaseURL:    baseURL,
		ServiceKey: serviceKey,
		OnConnect:  onConnect,
		OnMessage: func(msg wswatcher.Message) {
			if id, ok := waitingSource(msg); ok {
				onAnalysing(id)
			}
		},
	})}
}

// waitingSource reports the id of a source a message announces as waiting
// for analysis.
func waitingSource(msg wswatcher.Message) (int64, bool) {
	if msg.Type != "SOURCE_CREATED" && msg.Type != "SOURCE_UPDATED" {
		return 0, false
	}
	var s struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(msg.Payload, &s); err != nil {
		logger.Debugf("ResourceManager %s with a payload that cannot be read: %v", msg.Type, err)
		return 0, false
	}
	if s.ID == 0 || s.Status != "Analysing" {
		return 0, false
	}
	return s.ID, true
}

// Start opens the connection; it reconnects on its own.
func (w *Watcher) Start() { w.ws.Start() }

// Close ends the connection. Safe to call more than once.
func (w *Watcher) Close() error { return w.ws.Close() }
