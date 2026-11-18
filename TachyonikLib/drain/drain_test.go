// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the gate: units enter until Stop, Wait returns once they left, and a
// unit still running when the time is up is named and cut off.

package drain

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestGate(t *testing.T) {
	var g Gate
	if names := g.Wait(time.Millisecond); names != nil {
		t.Fatalf("an empty gate: %v", names)
	}
	u := g.Enter("rule 1 for user 1", nil)
	if u == nil || g.Stopping() {
		t.Fatal("not admitted")
	}
	g.Stop()
	if !g.Stopping() || g.Enter("late", nil) != nil {
		t.Fatal("a unit entered after Stop")
	}
	go func() { time.Sleep(20 * time.Millisecond); u.Leave() }()
	if names := g.Wait(2 * time.Second); names != nil {
		t.Errorf("still running: %v", names)
	}
}

func TestACutOffUnitIsNamedAndReported(t *testing.T) {
	var g Gate
	var cut atomic.Int32
	u := g.Enter("run of rule 4", func() { cut.Add(1) })
	u.Rename("run of rule 4 for user 7")
	g.Enter("quick", nil).Leave()
	g.Stop()
	names := g.Wait(20 * time.Millisecond)
	if len(names) != 1 || names[0] != "run of rule 4 for user 7" || cut.Load() != 1 {
		t.Errorf("names %v, cut off %d", names, cut.Load())
	}
	u.Leave()
}
