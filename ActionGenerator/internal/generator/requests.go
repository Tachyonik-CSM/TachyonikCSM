// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The queue of evaluation requests: one rule, or every rule, for one user,
// asked for by a person (AG-EVAL-13, -29).
//
// The requests arrive in the read loop of the AIManager WebSocket, which must
// not wait for them: while it evaluates, no rule change, catalogue change or
// further request is read, and one request whose bridge calls run into their
// timeout holds back all the others. So the read loop only adds a request
// here, and one worker evaluates them in the order they came.
//
// Requests that wait collapse: the same rule for the same user is evaluated
// once (forced if either asked for it), and a request for every rule of a user
// takes in that user's waiting unforced ones. A forced request stays, since it
// asks for what a plain evaluation does not do.
//
// Full passes do not go through here (passrunner.go); they run beside the
// worker.
package generator

import (
	"strconv"
	"sync"

	"tachyonik/lib/logger"
)

// Request is one evaluation request. RuleID 0 asks for every rule.
type Request struct {
	RuleID int64
	UserID int64
	Force  bool
}

// all reports whether the request is for every rule of its user.
func (r Request) all() bool { return r.RuleID == 0 }

// RequestQueue evaluates requests one at a time, in order, outside the
// caller.
type RequestQueue struct {
	process func(Request)

	mu      sync.Mutex
	pending []Request
	stopped bool
	wake    chan struct{}
}

// NewRequestQueue returns a queue that calls process for each request, and
// starts its worker.
func NewRequestQueue(process func(Request)) *RequestQueue {
	q := &RequestQueue{process: process, wake: make(chan struct{}, 1)}
	go q.work()
	return q
}

// Add queues a request and returns at once.
func (q *RequestQueue) Add(r Request) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped {
		logger.Infof("Stopping: evaluation request %s not taken", r)
		return
	}
	if r.all() {
		q.addAllLocked(r)
	} else {
		q.addOneLocked(r)
	}
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// addOneLocked queues a request for one rule, unless it waits already.
func (q *RequestQueue) addOneLocked(r Request) {
	for i, p := range q.pending {
		if p.UserID != r.UserID {
			continue
		}
		if p.RuleID == r.RuleID {
			q.pending[i].Force = p.Force || r.Force
			return
		}
		if p.all() && !r.Force {
			return // the waiting request for every rule evaluates this one
		}
	}
	q.pending = append(q.pending, r)
}

// addAllLocked queues a request for every rule of a user. The user's waiting
// unforced requests are taken in, and it waits where the first of them did.
func (q *RequestQueue) addAllLocked(r Request) {
	at := -1
	kept := q.pending[:0]
	for _, p := range q.pending {
		if p.UserID == r.UserID && (p.all() || !p.Force) {
			if at < 0 {
				at = len(kept)
			}
			continue
		}
		kept = append(kept, p)
	}
	q.pending = kept
	if at < 0 {
		q.pending = append(q.pending, r)
		return
	}
	q.pending = append(q.pending, Request{})
	copy(q.pending[at+1:], q.pending[at:])
	q.pending[at] = r
}

// work evaluates the requests until Stop.
func (q *RequestQueue) work() {
	for {
		q.mu.Lock()
		for len(q.pending) == 0 && !q.stopped {
			q.mu.Unlock()
			<-q.wake
			q.mu.Lock()
		}
		if q.stopped {
			q.mu.Unlock()
			return
		}
		r := q.pending[0]
		q.pending = q.pending[1:]
		q.mu.Unlock()

		q.process(r)
	}
}

// Stop drops the requests still waiting — the next start evaluates every rule
// anyway (AG-OPS-09) — and ends the worker after the request it is on.
func (q *RequestQueue) Stop() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped {
		return
	}
	q.stopped = true
	if n := len(q.pending); n > 0 {
		logger.Infof("Stopping: %d waiting evaluation requests dropped; the next start evaluates every rule", n)
	}
	q.pending = nil
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// String names the request for the log.
func (r Request) String() string {
	if r.all() {
		return "for every rule, user " + strconv.FormatInt(r.UserID, 10)
	}
	s := "for rule " + strconv.FormatInt(r.RuleID, 10) + ", user " + strconv.FormatInt(r.UserID, 10)
	if r.Force {
		s += " (forced)"
	}
	return s
}
