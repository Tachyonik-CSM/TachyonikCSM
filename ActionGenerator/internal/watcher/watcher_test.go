// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests which events wake the daemon.
//
// The connection machinery is wswatcher's and is tested there. What is tested
// here is the trigger set: that an asset event wakes the AssetManager watcher,
// that a user or organisation event wakes the SystemManager one, and that
// neither reacts to the other's — the two connections share one callback, and a
// watcher reacting to everything would re-evaluate on every broadcast in the
// platform.

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

// The connect resync fires once; each subscribed event adds one more.
func TestAssetEventsWakeTheDaemon(t *testing.T) {
	srv := fakeManager(t, []string{"ASSET_CREATED", "VULNERABILITY_UPDATED"})

	var fired counter
	w, err := New(srv.URL, "k", fired.inc)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w.Start()
	defer w.Close()

	waitFor(t, 3*time.Second, "resync + two events", func() bool { return fired.get() >= 3 })
}

func TestSystemEventsWakeTheDaemon(t *testing.T) {
	srv := fakeManager(t, []string{"USER_UPDATED", "ORGANISATION_UPDATED"})

	var fired counter
	w, err := NewFor("SystemManager", srv.URL, "k", SystemTriggers, fired.inc)
	if err != nil {
		t.Fatalf("NewFor: %v", err)
	}
	w.Start()
	defer w.Close()

	waitFor(t, 3*time.Second, "resync + two events", func() bool { return fired.get() >= 3 })
}

// A watcher must ignore what it did not subscribe to. The AssetManager and
// SystemManager connections share one callback, so a watcher reacting to
// everything would double every re-evaluation.
func TestUnsubscribedEventsAreIgnored(t *testing.T) {
	srv := fakeManager(t, []string{"USER_UPDATED", "ORGANISATION_UPDATED", "SOMETHING_ELSE"})

	var fired counter
	w, _ := New(srv.URL, "", fired.inc) // asset triggers only
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
	w, _ := New(srv.URL, "", fired.inc)
	w.Start()
	defer w.Close()

	waitFor(t, 6*time.Second, "two resyncs", func() bool { return fired.get() >= 2 })
}

func TestCloseIsIdempotentAndSafeBeforeStart(t *testing.T) {
	srv := fakeManager(t, nil)
	w, _ := New(srv.URL, "", func() {})
	w.Start()
	time.Sleep(50 * time.Millisecond)
	if err := w.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}

	unstarted, _ := New("http://127.0.0.1:1", "", func() {})
	if err := unstarted.Close(); err != nil {
		t.Errorf("Close without Start: %v", err)
	}
}
