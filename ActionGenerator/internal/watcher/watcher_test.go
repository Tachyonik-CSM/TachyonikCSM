// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests which events wake the daemon.
//
// The connection machinery is wswatcher's and is tested there. What is tested
// here is the trigger set: that a message the active routines' operations are
// invalidated by wakes a watcher, that SystemManager's users appearing and
// going always do, and that nothing else does — the connections share one
// callback, and a watcher reacting to everything would re-evaluate on every
// broadcast in the platform.

package watcher

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func fakeManager(t *testing.T, frames []string) *httptest.Server {
	t.Helper()
	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for _, typ := range frames {
			raw, _ := json.Marshal(map[string]any{"type": typ})
			conn.WriteMessage(websocket.TextMessage, raw)
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type counter struct {
	mu sync.Mutex
	n  int
}

func (c *counter) inc()     { c.mu.Lock(); c.n++; c.mu.Unlock() }
func (c *counter) get() int { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

func waitFor(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// AG-IF-24: the connect resync fires once; each message the extra triggers
// name adds one more, and nothing else does.
func TestTheRoutinesTriggersWakeTheDaemon(t *testing.T) {
	srv := fakeManager(t, []string{"ASSET_CREATED", "TOOL_ACTIVITY", "VULNERABILITY_UPDATED"})

	var fired counter
	w, err := NewFor("AssetManager", srv.URL, "k", nil, fired.inc)
	if err != nil {
		t.Fatalf("NewFor: %v", err)
	}
	w.SetExtraTriggers(func(m string) bool { return m == "ASSET_CREATED" || m == "VULNERABILITY_UPDATED" })
	w.Start()
	defer w.Close()

	waitFor(t, 3*time.Second, "resync + two events", func() bool { return fired.get() >= 3 })
	time.Sleep(150 * time.Millisecond)
	if n := fired.get(); n != 3 {
		t.Errorf("callback fired %d times, want 3: a message no routine depends on must not wake the daemon", n)
	}
}

// AG-IF-18: a user appearing or going re-evaluates whatever the routines read.
func TestUsersAppearingAndGoingWakeTheDaemon(t *testing.T) {
	srv := fakeManager(t, []string{"USER_CREATED", "USER_DELETED"})

	var fired counter
	w, err := NewFor("SystemManager", srv.URL, "k", SystemTriggers, fired.inc)
	if err != nil {
		t.Fatalf("NewFor: %v", err)
	}
	w.Start()
	defer w.Close()

	waitFor(t, 3*time.Second, "resync + two events", func() bool { return fired.get() >= 3 })
}

// A watcher must ignore what it did not subscribe to. The connections share
// one callback, so a watcher reacting to everything would multiply every
// re-evaluation.
func TestUnsubscribedEventsAreIgnored(t *testing.T) {
	srv := fakeManager(t, []string{"USER_UPDATED", "ORGANISATION_UPDATED", "SOMETHING_ELSE"})

	var fired counter
	w, _ := NewFor("SystemManager", srv.URL, "", SystemTriggers, fired.inc) // no extra triggers
	w.Start()
	defer w.Close()

	waitFor(t, 2*time.Second, "the connect resync", func() bool { return fired.get() >= 1 })
	time.Sleep(150 * time.Millisecond)
	if n := fired.get(); n != 1 {
		t.Errorf("callback fired %d times, want only the connect resync", n)
	}
}

// Reconnecting re-evaluates: a change made while the connection was down is
// otherwise missed until the next live event.
func TestResyncsOnConnect(t *testing.T) {
	var once sync.Once
	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		once.Do(func() { conn.Close() })
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	var fired counter
	w, _ := NewFor("AssetManager", srv.URL, "", nil, fired.inc)
	w.Start()
	defer w.Close()

	waitFor(t, 6*time.Second, "two resyncs", func() bool { return fired.get() >= 2 })
}

func TestCloseIsIdempotentAndSafeBeforeStart(t *testing.T) {
	srv := fakeManager(t, nil)
	w, _ := NewFor("AssetManager", srv.URL, "", nil, func() {})
	w.Start()
	time.Sleep(50 * time.Millisecond)
	if err := w.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}

	unstarted, _ := NewFor("AssetManager", "http://127.0.0.1:1", "", nil, func() {})
	if err := unstarted.Close(); err != nil {
		t.Errorf("Close without Start: %v", err)
	}
}
