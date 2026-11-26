// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the evaluation request queue (AG-EVAL-13, -29): adding returns at once
// while a request is being evaluated, requests are taken in order, waiting
// duplicates collapse, a request for every rule takes in a user's unforced
// ones, and Stop drops what waits.

package generator

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

// heldQueue is a queue whose worker blocks on its first request until
// release, so the test can line up the rest behind it.
type heldQueue struct {
	q       *RequestQueue
	release chan struct{}
	started chan struct{}
	mu      sync.Mutex
	done    []Request
	all     chan struct{}
	want    int
}

func newHeldQueue(t *testing.T, want int) *heldQueue {
	t.Helper()
	h := &heldQueue{release: make(chan struct{}), started: make(chan struct{}), all: make(chan struct{}), want: want}
	first := true
	h.q = NewRequestQueue(func(r Request) {
		if first {
			first = false
			close(h.started)
			<-h.release
		}
		h.mu.Lock()
		h.done = append(h.done, r)
		if len(h.done) == h.want {
			close(h.all)
		}
		h.mu.Unlock()
	})
	t.Cleanup(h.q.Stop)
	return h
}

func (h *heldQueue) wait(t *testing.T) []Request {
	t.Helper()
	select {
	case <-h.all:
	case <-time.After(2 * time.Second):
		h.mu.Lock()
		defer h.mu.Unlock()
		t.Fatalf("evaluated %v, want %d requests", h.done, h.want)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Request(nil), h.done...)
}

func TestAddReturnsWhileARequestIsEvaluated(t *testing.T) {
	h := newHeldQueue(t, 2)
	h.q.Add(Request{RuleID: 1, UserID: 7})
	<-h.started
	returned := make(chan struct{})
	go func() {
		h.q.Add(Request{RuleID: 2, UserID: 7})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("Add waited for the request being evaluated")
	}
	close(h.release)
	if got := h.wait(t); !reflect.DeepEqual(got, []Request{{RuleID: 1, UserID: 7}, {RuleID: 2, UserID: 7}}) {
		t.Errorf("evaluated %v", got)
	}
}

func TestWaitingRequestsCollapse(t *testing.T) {
	h := newHeldQueue(t, 5)
	h.q.Add(Request{RuleID: 9, UserID: 1}) // being evaluated
	<-h.started
	h.q.Add(Request{RuleID: 3, UserID: 7})
	h.q.Add(Request{RuleID: 3, UserID: 7, Force: true}) // same rule: one, forced
	h.q.Add(Request{RuleID: 4, UserID: 8})
	h.q.Add(Request{RuleID: 5, UserID: 7})
	h.q.Add(Request{RuleID: 6, UserID: 7, Force: false})
	h.q.Add(Request{UserID: 7})            // takes in 5 and 6, not the forced 3
	h.q.Add(Request{RuleID: 2, UserID: 7}) // taken in by the waiting one for every rule
	h.q.Add(Request{UserID: 7})            // waits already
	h.q.Add(Request{RuleID: 1, UserID: 7, Force: true})
	close(h.release)
	got := h.wait(t)
	want := []Request{
		{RuleID: 9, UserID: 1},
		{RuleID: 3, UserID: 7, Force: true},
		{RuleID: 4, UserID: 8},
		{UserID: 7}, // where 5 waited
		{RuleID: 1, UserID: 7, Force: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("evaluated\n %v\nwant\n %v", got, want)
	}
}

func TestStopDropsTheWaitingRequests(t *testing.T) {
	var mu sync.Mutex
	var done []Request
	release := make(chan struct{})
	started := make(chan struct{})
	q := NewRequestQueue(func(r Request) {
		mu.Lock()
		done = append(done, r)
		first := len(done) == 1
		mu.Unlock()
		if first {
			close(started)
			<-release
		}
	})
	q.Add(Request{RuleID: 1, UserID: 1})
	<-started
	q.Add(Request{RuleID: 2, UserID: 1})
	q.Stop()
	q.Add(Request{RuleID: 3, UserID: 1}) // after Stop: not taken
	close(release)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(done, []Request{{RuleID: 1, UserID: 1}}) {
		t.Errorf("evaluated %v, want only the request that was running", done)
	}
}
