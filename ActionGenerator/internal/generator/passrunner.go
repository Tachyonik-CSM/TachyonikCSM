// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// One gate for every full evaluation: never two at once, and no change left
// behind.
//
// Full passes are asked for from many places: the managers' change messages,
// a rule change, a catalogue change, a reload of the rules, the start. A
// PassRunner runs them one at a time (AG-EVAL-03). A pass asked for while one
// runs is not dropped but marked pending, and one more pass follows the
// running one (AG-EVAL-04) — however many were asked for meanwhile. The
// follow-up waits for the debounce, which every new change message pushes
// back, so an import that outlasts a pass still ends in one pass rather than
// one per message (AG-EVAL-02).
//
// Evaluation requests (one rule for one user, AG-EVAL-13) are not full passes
// and do not go through here: a person is waiting for their answer.

package generator

import (
	"sync"
	"time"
)

// PassRunner serialises full passes.
type PassRunner struct {
	run      func()
	debounce time.Duration

	mu      sync.Mutex
	running bool
	pending bool
	stopped bool
	timer   *time.Timer
	// idle is closed and replaced as passes end, so a test can wait for one.
	idle chan struct{}
}

// NewPassRunner returns a runner calling run for each pass, with debounce as
// the wait after the last change message (2 s, AG-EVAL-02).
func NewPassRunner(run func(), debounce time.Duration) *PassRunner {
	return &PassRunner{run: run, debounce: debounce, idle: make(chan struct{})}
}

// Debounced asks for a pass debounce after the last call: a change message.
func (r *PassRunner) Debounced() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	r.armLocked()
}

// Now asks for a pass at once, or right after the running one: a rule
// change, a catalogue change, a reload, the start. It does not wait for the
// pass.
func (r *PassRunner) Now() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	if r.running {
		r.pending = true
		return
	}
	// The pass starting now reads the data after every message so far, so a
	// debounce still waiting is served by it.
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.startLocked()
}

// Stop ends the runner: no pass starts any more. A running pass is not
// waited for; the generator's own Stop and Wait see to that (AG-OPS-09).
func (r *PassRunner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	r.pending = false
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
}

func (r *PassRunner) armLocked() {
	if r.timer != nil {
		r.timer.Stop()
	}
	r.timer = time.AfterFunc(r.debounce, r.fire)
}

func (r *PassRunner) fire() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.timer = nil
	if r.stopped {
		return
	}
	if r.running {
		r.pending = true
		return
	}
	r.startLocked()
}

func (r *PassRunner) startLocked() {
	r.running = true
	go func() {
		r.run()
		r.mu.Lock()
		defer r.mu.Unlock()
		r.running = false
		close(r.idle)
		r.idle = make(chan struct{})
		if r.pending && !r.stopped {
			r.pending = false
			r.armLocked()
		}
	}()
}

// passEnded returns a channel closed when the next pass ends.
func (r *PassRunner) passEnded() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.idle
}
