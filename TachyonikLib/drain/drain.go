// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package drain lets a daemon stop without cutting a unit of work in half.
//
// A daemon's work comes in units that must not be split — an action rule
// evaluated for one user (create, then withdraw), an execution run and the
// report of its outcome. Each unit enters a Gate and leaves it when done. On
// SIGINT or SIGTERM the daemon closes its watchers, calls Stop — from then on
// no unit enters, and a long pass sees Stopping and goes no further — and
// Wait gives the units still running a bounded time to finish. A unit still
// running when the time is up is named, and its OnCutOff runs, so what was
// cut off is recorded rather than lost.
package drain

import (
	"sync"
	"time"
)

// Gate admits units of work until Stop.
type Gate struct {
	mu      sync.Mutex
	closed  bool
	running map[*Unit]struct{}
	idle    *sync.Cond
}

// Unit is one admitted unit of work.
type Unit struct {
	gate     *Gate
	name     string
	onCutOff func()
}

// Enter admits a unit named name, or returns nil once the gate is closed.
// onCutOff, when not nil, is called if the unit is still running when Wait
// gives up on it.
func (g *Gate) Enter(name string, onCutOff func()) *Unit {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	if g.running == nil {
		g.running = map[*Unit]struct{}{}
		g.idle = sync.NewCond(&g.mu)
	}
	u := &Unit{gate: g, name: name, onCutOff: onCutOff}
	g.running[u] = struct{}{}
	return u
}

// Rename names what the unit is doing now, for Wait's report — a pass moving
// from one rule and user to the next.
func (u *Unit) Rename(name string) {
	u.gate.mu.Lock()
	u.name = name
	u.gate.mu.Unlock()
}

// Leave marks the unit done.
func (u *Unit) Leave() {
	g := u.gate
	g.mu.Lock()
	delete(g.running, u)
	if len(g.running) == 0 {
		g.idle.Broadcast()
	}
	g.mu.Unlock()
}

// Stop closes the gate: no unit enters any more.
func (g *Gate) Stop() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
}

// Stopping reports whether Stop was called; a long unit checks it between
// its steps.
func (g *Gate) Stopping() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.closed
}

// Wait waits up to timeout for the running units to leave. It returns the
// names of those still running then, after calling their OnCutOff; nil when
// every unit finished.
func (g *Gate) Wait(timeout time.Duration) []string {
	done := make(chan struct{})
	go func() {
		g.mu.Lock()
		for len(g.running) > 0 {
			g.idle.Wait()
		}
		g.mu.Unlock()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
	}
	g.mu.Lock()
	var names []string
	var cutOff []func()
	for u := range g.running {
		names = append(names, u.name)
		if u.onCutOff != nil {
			cutOff = append(cutOff, u.onCutOff)
		}
	}
	g.mu.Unlock()
	for _, f := range cutOff {
		f()
	}
	return names
}
