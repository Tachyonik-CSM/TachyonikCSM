// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Per-key debouncing for rule-change events.
//
// A manager emits an event per write, so editing a rule in the UI produces a
// burst. Regenerating on each one wastes an AI call per keystroke and leaves
// the last generation racing the one before it, so a change waits for a quieter
// moment and only the last one in a burst is acted on.

package wswatcher

import (
	"sync"
	"time"

	"tachyonik/lib/logger"
)

// MaxDebounceKeys caps the pending map. Its keys come straight off the wire, so
// a manager streaming events with distinct rule IDs would otherwise allocate a
// timer per ID with nothing to stop it.
const MaxDebounceKeys = 1024

// Debouncer collapses repeated events for the same key into one call, fired
// once the key has been quiet for the configured delay.
//
// The zero value is not usable; call NewDebouncer.
type Debouncer struct {
	delay time.Duration
	name  string

	mu     sync.Mutex
	timers map[int64]*time.Timer
}

// NewDebouncer returns a debouncer that waits delay after the last event for a
// key. name appears in the log line when the cap is hit.
func NewDebouncer(delay time.Duration, name string) *Debouncer {
	return &Debouncer{delay: delay, name: name, timers: make(map[int64]*time.Timer)}
}

// Trigger schedules fn for key, replacing any pending call for that key.
//
// Dropped rather than queued when the cap is reached: the pending map is the
// thing being protected, and a change that is dropped is logged rather than
// silently lost.
func (d *Debouncer) Trigger(key int64, fn func()) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Stop nils the map; an event still in flight at that moment must not
	// assign into it, because writing to a nil map panics.
	if d.timers == nil {
		return
	}

	timer, exists := d.timers[key]
	if exists {
		timer.Stop()
	} else if len(d.timers) >= MaxDebounceKeys {
		logger.Warnf("%s: %d changes already pending — dropping the change for %d", d.name, len(d.timers), key)
		return
	}

	d.timers[key] = time.AfterFunc(d.delay, func() {
		fn()
		d.mu.Lock()
		delete(d.timers, key)
		d.mu.Unlock()
	})
}

// Stop cancels every pending call. Safe to call more than once.
func (d *Debouncer) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, timer := range d.timers {
		timer.Stop()
	}
	d.timers = nil
}
