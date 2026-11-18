// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the PassRunner: a change during a pass leads to exactly one more pass
// after it (AG-EVAL-04), however many arrive; passes never overlap
// (AG-EVAL-03); the follow-up waits for the debounce (AG-EVAL-02); and nothing
// starts after Stop.

package generator

import (
	"sync/atomic"
	"testing"
	"time"
)

const testDebounce = 20 * time.Millisecond

// blockingPass is a pass function that counts its runs, notices overlap,
// and, while hold is set, waits until release.
type blockingPass struct {
	runs, inside, overlaps atomic.Int32
	started                chan struct{}
	release                chan struct{}
	hold                   atomic.Bool
}

func newBlockingPass() *blockingPass {
	return &blockingPass{started: make(chan struct{}, 16), release: make(chan struct{})}
}

func (b *blockingPass) run() {
	if b.inside.Add(1) > 1 {
		b.overlaps.Add(1)
	}
	b.runs.Add(1)
	b.started <- struct{}{}
	if b.hold.Load() {
		<-b.release
	}
	b.inside.Add(-1)
}

func waitStarted(t *testing.T, b *blockingPass) {
	t.Helper()
	select {
	case <-b.started:
	case <-time.After(2 * time.Second):
		t.Fatal("no pass started")
	}
}

func settle() { time.Sleep(5 * testDebounce) }

func TestAChangeDuringAPassLeadsToOneMore(t *testing.T) {
	b := newBlockingPass()
	b.hold.Store(true)
	r := NewPassRunner(b.run, testDebounce)
	defer r.Stop()

	r.Now()
	waitStarted(t, b)
	// Several changes and requests while it runs ...
	for i := 0; i < 5; i++ {
		r.Debounced()
		r.Now()
	}
	settle()
	b.hold.Store(false)
	close(b.release)
	// ... end in exactly one follow-up.
	waitStarted(t, b)
	settle()
	if n := b.runs.Load(); n != 2 {
		t.Errorf("runs = %d, want 2: one pass and one follow-up", n)
	}
	if n := b.overlaps.Load(); n != 0 {
		t.Errorf("%d passes overlapped", n)
	}
}

func TestNothingPendingMeansNoFollowUp(t *testing.T) {
	b := newBlockingPass()
	r := NewPassRunner(b.run, testDebounce)
	defer r.Stop()
	r.Now()
	waitStarted(t, b)
	settle()
	if n := b.runs.Load(); n != 1 {
		t.Errorf("runs = %d, want 1", n)
	}
}

func TestABurstOfMessagesIsOnePass(t *testing.T) {
	b := newBlockingPass()
	r := NewPassRunner(b.run, testDebounce)
	defer r.Stop()
	for i := 0; i < 10; i++ {
		r.Debounced()
		time.Sleep(testDebounce / 4)
	}
	waitStarted(t, b)
	settle()
	if n := b.runs.Load(); n != 1 {
		t.Errorf("runs = %d, want 1 for one burst", n)
	}
}

func TestTheFollowUpWaitsForTheDebounce(t *testing.T) {
	b := newBlockingPass()
	b.hold.Store(true)
	r := NewPassRunner(b.run, 200*time.Millisecond)
	defer r.Stop()
	r.Now()
	waitStarted(t, b)
	r.Now() // pending
	ended := r.passEnded()
	b.hold.Store(false)
	close(b.release)
	<-ended
	start := time.Now()
	waitStarted(t, b)
	if waited := time.Since(start); waited < 150*time.Millisecond {
		t.Errorf("the follow-up started after %s, want about the debounce", waited)
	}
}

func TestNothingStartsAfterStop(t *testing.T) {
	b := newBlockingPass()
	b.hold.Store(true)
	r := NewPassRunner(b.run, testDebounce)
	r.Now()
	waitStarted(t, b)
	r.Now() // pending
	r.Debounced()
	r.Stop()
	b.hold.Store(false)
	close(b.release)
	r.Now()
	r.Debounced()
	settle()
	if n := b.runs.Load(); n != 1 {
		t.Errorf("runs = %d after Stop, want 1", n)
	}
}
