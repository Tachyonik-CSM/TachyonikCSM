// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests of the Loader: a request loads, a failed load is retried until it
// succeeds, and nothing loads after Stop.
package reload

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// waitFor polls cond for up to two seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestARequestLoads(t *testing.T) {
	var loads atomic.Int32
	l := New("the rules", func() error { loads.Add(1); return nil })
	l.Start()
	defer l.Stop()

	l.Request()
	waitFor(t, "one load", func() bool { return loads.Load() == 1 })
	time.Sleep(30 * time.Millisecond)
	if n := loads.Load(); n != 1 {
		t.Fatalf("loads = %d after one request, want 1", n)
	}
}

func TestAFailedLoadIsRetriedUntilItSucceeds(t *testing.T) {
	var loads atomic.Int32
	l := New("the rules", func() error {
		if loads.Add(1) < 3 {
			return errors.New("connection refused")
		}
		return nil
	})
	l.Retry = 10 * time.Millisecond
	l.Start()
	defer l.Stop()

	l.Request()
	waitFor(t, "the third load", func() bool { return loads.Load() == 3 })
	time.Sleep(50 * time.Millisecond)
	if n := loads.Load(); n != 3 {
		t.Fatalf("loads = %d, want 3: a successful load must end the retries", n)
	}
}

func TestRequestsDuringALoadCoalesce(t *testing.T) {
	var loads atomic.Int32
	release := make(chan struct{})
	l := New("the rules", func() error {
		if loads.Add(1) == 1 {
			<-release
		}
		return nil
	})
	l.Start()
	defer l.Stop()

	l.Request()
	waitFor(t, "the first load", func() bool { return loads.Load() == 1 })
	for i := 0; i < 5; i++ {
		l.Request()
	}
	close(release)
	waitFor(t, "the follow-up load", func() bool { return loads.Load() == 2 })
	time.Sleep(30 * time.Millisecond)
	if n := loads.Load(); n != 2 {
		t.Fatalf("loads = %d, want 2: requests made during a load are served by one more", n)
	}
}

func TestNothingLoadsAfterStop(t *testing.T) {
	var loads atomic.Int32
	l := New("the rules", func() error { loads.Add(1); return errors.New("down") })
	l.Retry = 10 * time.Millisecond
	l.Start()

	l.Request()
	waitFor(t, "the first load", func() bool { return loads.Load() >= 1 })
	l.Stop()
	l.Stop() // a second Stop is harmless
	n := loads.Load()
	time.Sleep(50 * time.Millisecond)
	if after := loads.Load(); after > n+1 {
		t.Fatalf("loads went from %d to %d after Stop", n, after)
	}
	l.Request()
	time.Sleep(30 * time.Millisecond)
	if after := loads.Load(); after > n+1 {
		t.Fatalf("a request after Stop loaded (%d → %d)", n, after)
	}
}
