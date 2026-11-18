// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package reload loads a daemon's rules again whenever asked, and keeps
// trying while the load fails.
//
// A daemon loads its rules from AIManager at startup, and again whenever
// what it holds may be stale: after a feed import, and after every
// (re)connect to AIManager, since a connection that was down has missed the
// changes announced meanwhile. A load that fails — AIManager not reachable
// yet, or still starting — is tried again every Retry until one succeeds, so
// a daemon started before AIManager does not stay without rules until
// someone happens to change one. Requests made while a load runs are
// coalesced into one more load after it.
package reload

import (
	"sync"
	"time"

	"tachyonik/lib/logger"
)

// DefaultRetry is how long a failed load waits before it is tried again.
const DefaultRetry = 30 * time.Second

// Loader runs a load function on request, one at a time, retrying it until
// it succeeds.
type Loader struct {
	what string
	load func() error

	// Retry is the wait after a failed load. Set it before Start.
	Retry time.Duration

	kick     chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
}

// New returns a Loader for load, which loads what (e.g. "the action rules",
// used in its log lines) and returns why it could not.
func New(what string, load func() error) *Loader {
	return &Loader{
		what:  what,
		load:  load,
		Retry: DefaultRetry,
		kick:  make(chan struct{}, 1),
		stop:  make(chan struct{}),
	}
}

// Start begins serving requests in its own goroutine.
func (l *Loader) Start() {
	go l.run()
}

// Request asks for a load. It never blocks; requests made before the next
// load starts are served by that one load.
func (l *Loader) Request() {
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

// Stop ends the loader: no load starts any more. A load running now is not
// waited for.
func (l *Loader) Stop() {
	l.stopOnce.Do(func() { close(l.stop) })
}

func (l *Loader) run() {
	var retry *time.Timer
	var retryC <-chan time.Time
	failures := 0
	for {
		select {
		case <-l.stop:
			return
		case <-l.kick:
		case <-retryC:
		}
		if retry != nil {
			retry.Stop()
			retry, retryC = nil, nil
		}
		// A Stop that arrived together with the request wins.
		select {
		case <-l.stop:
			return
		default:
		}
		if err := l.load(); err != nil {
			failures++
			logger.Warnf("Could not load %s (attempt %d), trying again in %s: %v", l.what, failures, l.Retry, err)
			retry = time.NewTimer(l.Retry)
			retryC = retry.C
			continue
		}
		if failures > 0 {
			logger.Infof("Loaded %s after %d failed attempts", l.what, failures)
			failures = 0
		}
	}
}
