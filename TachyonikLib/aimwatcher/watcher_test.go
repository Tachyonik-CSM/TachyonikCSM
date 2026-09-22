// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Characterisation tests for the AIManager watcher.
//
// Six daemons depend on this package and it had no tests, so these were written
// before its internals were restructured and are what say the restructuring
// changed nothing observable: which events reach which handler, that a rule
// change is debounced per rule, that a module setting is filtered by module
// name, that a fresh connection resyncs, that PING is answered, and that Close
// can be called twice.
//
// They drive the watcher against a real WebSocket server rather than a stub,
// because the parts most worth pinning — the dial, the read loop, the pong —
// only exist on a real connection.

package aimwatcher

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeAIManager is a WebSocket server that hands each connection to fn.
type fakeAIManager struct {
	srv      *httptest.Server
	upgrader websocket.Upgrader
	mu       sync.Mutex
	conns    int
	lastKey  string
	lastPath string
}

func newFakeAIManager(t *testing.T, fn func(conn *websocket.Conn)) *fakeAIManager {
	t.Helper()
	f := &fakeAIManager{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.conns++
		f.lastKey = r.Header.Get("X-Internal-Service-Key")
		f.lastPath = r.URL.Path
		f.mu.Unlock()

		conn, err := f.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		fn(conn)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAIManager) connections() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns
}

func (f *fakeAIManager) key() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastKey
}

func (f *fakeAIManager) path() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastPath
}

// send writes one event frame.
func send(t *testing.T, conn *websocket.Conn, typ string, payload any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"type": typ, "payload": payload})
	if err != nil {
		t.Errorf("marshal: %v", err)
		return
	}
	conn.WriteMessage(websocket.TextMessage, raw)
}

// waitFor fails unless cond becomes true within limit.
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

// counter is a handler that records how often it fired.
type counter struct {
	mu sync.Mutex
	n  int
}

func (c *counter) inc()     { c.mu.Lock(); c.n++; c.mu.Unlock() }
func (c *counter) get() int { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

// The URL is rewritten to ws:// with the /ws path, and the service key rides
// on the upgrade request.
func TestConnectsToWSPathWithTheServiceKey(t *testing.T) {
	f := newFakeAIManager(t, func(conn *websocket.Conn) {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	w := New(f.srv.URL, "test-key", Handlers{ModuleName: "test"})
	w.Start()
	defer w.Close()

	waitFor(t, 2*time.Second, "a connection", func() bool { return f.connections() > 0 })
	if f.path() != "/ws" {
		t.Errorf("path = %q, want /ws", f.path())
	}
	if f.key() != "test-key" {
		t.Errorf("service key = %q", f.key())
	}
}

// A fresh connection resyncs: both the reload and the settings handler fire
// without any event being sent, so a watcher that connects after state was
// written does not sit on stale state.
func TestResyncsOnConnect(t *testing.T) {
	f := newFakeAIManager(t, func(conn *websocket.Conn) {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	var reload, settings counter
	w := New(f.srv.URL, "", Handlers{
		ModuleName:            "test",
		OnFeedImported:        reload.inc,
		OnModuleSettingChange: settings.inc,
	})
	w.Start()
	defer w.Close()

	waitFor(t, 2*time.Second, "the reload resync", func() bool { return reload.get() > 0 })
	waitFor(t, 2*time.Second, "the settings resync", func() bool { return settings.get() > 0 })
}

// A rule event reaches OnRuleChange — after the debounce, carrying the id and
// what happened to it.
func TestRuleEventIsDispatched(t *testing.T) {
	f := newFakeAIManager(t, func(conn *websocket.Conn) {
		send(t, conn, "ANALYSIS_RULE_UPDATED", map[string]any{"id": 42})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	var mu sync.Mutex
	var got []RuleChangeEvent
	w := New(f.srv.URL, "", Handlers{
		ModuleName: "test",
		OnRuleChange: func(e RuleChangeEvent) {
			mu.Lock()
			got = append(got, e)
			mu.Unlock()
		},
	})
	w.debounceFor = 20 * time.Millisecond
	w.Start()
	defer w.Close()

	waitFor(t, 3*time.Second, "the rule event", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) > 0
	})
	mu.Lock()
	defer mu.Unlock()
	if got[0].RuleID != 42 || got[0].Type != "updated" {
		t.Errorf("event = %+v, want rule 42 updated", got[0])
	}
}

// A burst for one rule collapses to a single callback: regenerating once per
// keystroke of an edit is the thing the debounce exists to prevent.
func TestRuleEventsAreDebouncedPerRule(t *testing.T) {
	f := newFakeAIManager(t, func(conn *websocket.Conn) {
		for i := 0; i < 5; i++ {
			send(t, conn, "ANALYSIS_RULE_UPDATED", map[string]any{"id": 7})
		}
		send(t, conn, "ANALYSIS_RULE_UPDATED", map[string]any{"id": 8})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	var mu sync.Mutex
	seen := map[int64]int{}
	w := New(f.srv.URL, "", Handlers{
		OnRuleChange: func(e RuleChangeEvent) {
			mu.Lock()
			seen[e.RuleID]++
			mu.Unlock()
		},
	})
	w.debounceFor = 30 * time.Millisecond
	w.Start()
	defer w.Close()

	waitFor(t, 3*time.Second, "both rules", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == 2
	})
	time.Sleep(100 * time.Millisecond) // let any extra callbacks land

	mu.Lock()
	defer mu.Unlock()
	if seen[7] != 1 {
		t.Errorf("rule 7 fired %d times, want 1 — five events should collapse", seen[7])
	}
	if seen[8] != 1 {
		t.Errorf("rule 8 fired %d times, want 1", seen[8])
	}
}

// Only this module's settings event triggers the handler; another module's is
// ignored. Every daemon registers the same event, so the filter is what keeps
// them from each reacting to all of them.
func TestModuleSettingIsFilteredByModuleName(t *testing.T) {
	f := newFakeAIManager(t, func(conn *websocket.Conn) {
		send(t, conn, "MODULE_AI_SETTING_UPDATED", map[string]any{"moduleName": "someoneelse"})
		time.Sleep(30 * time.Millisecond)
		send(t, conn, "MODULE_AI_SETTING_UPDATED", map[string]any{"moduleName": "mine"})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	var fired counter
	w := New(f.srv.URL, "", Handlers{ModuleName: "mine", OnModuleSettingChange: fired.inc})
	w.Start()
	defer w.Close()

	// One for the connect resync, one for the matching event; never three.
	waitFor(t, 3*time.Second, "the matching settings event", func() bool { return fired.get() >= 2 })
	time.Sleep(100 * time.Millisecond)
	if n := fired.get(); n != 2 {
		t.Errorf("settings handler fired %d times, want 2 (resync + the matching event)", n)
	}
}

// PING is answered with PONG, in the JSON envelope rather than a control frame.
func TestPingIsAnsweredWithPong(t *testing.T) {
	pong := make(chan string, 1)
	f := newFakeAIManager(t, func(conn *websocket.Conn) {
		send(t, conn, "PING", nil)
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(raw, &msg) == nil && msg.Type != "" {
				select {
				case pong <- msg.Type:
				default:
				}
			}
		}
	})

	w := New(f.srv.URL, "", Handlers{})
	w.Start()
	defer w.Close()

	select {
	case got := <-pong:
		if got != "PONG" {
			t.Errorf("answered a PING with %q, want PONG", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a PING went unanswered")
	}
}

// A dropped connection is re-established, and the resync fires again.
func TestReconnectsAfterTheConnectionDrops(t *testing.T) {
	var closedOnce sync.Once
	f := newFakeAIManager(t, func(conn *websocket.Conn) {
		closedOnce.Do(func() { conn.Close() })
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	var reload counter
	w := New(f.srv.URL, "", Handlers{OnFeedImported: reload.inc})
	w.reconnectDelay = 20 * time.Millisecond
	w.Start()
	defer w.Close()

	waitFor(t, 5*time.Second, "a second connection", func() bool { return f.connections() >= 2 })
	waitFor(t, 5*time.Second, "a second resync", func() bool { return reload.get() >= 2 })
}

// Unknown message types are ignored rather than treated as an error that ends
// the read loop — AIManager broadcasts plenty this package does not care about.
func TestUnknownMessagesAreIgnored(t *testing.T) {
	f := newFakeAIManager(t, func(conn *websocket.Conn) {
		send(t, conn, "SOMETHING_ELSE", map[string]any{"id": 1})
		conn.WriteMessage(websocket.TextMessage, []byte("not json at all"))
		time.Sleep(20 * time.Millisecond)
		send(t, conn, "ANALYSIS_RULE_UPDATED", map[string]any{"id": 5})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	var fired counter
	w := New(f.srv.URL, "", Handlers{OnRuleChange: func(RuleChangeEvent) { fired.inc() }})
	w.debounceFor = 20 * time.Millisecond
	w.Start()
	defer w.Close()

	waitFor(t, 3*time.Second, "the rule event after the noise", func() bool { return fired.get() > 0 })
	if f.connections() != 1 {
		t.Errorf("reconnected %d times; the noise should not have ended the read loop", f.connections())
	}
}

// Close twice must not panic: a shutdown path that both defers Close and calls
// it explicitly is easy to write, and a bare close(done) would panic.
func TestCloseIsIdempotent(t *testing.T) {
	f := newFakeAIManager(t, func(conn *websocket.Conn) {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	w := New(f.srv.URL, "", Handlers{})
	w.Start()
	waitFor(t, 2*time.Second, "a connection", func() bool { return f.connections() > 0 })

	if err := w.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// A watcher that was never started must still close cleanly.
func TestCloseWithoutStart(t *testing.T) {
	if err := New("http://127.0.0.1:1", "", Handlers{}).Close(); err != nil {
		t.Errorf("Close on an unstarted watcher: %v", err)
	}
}

// https:// becomes wss://, http:// becomes ws://, and the path is replaced.
func TestWebSocketURL(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"http://localhost:8085", "ws://localhost:8085/ws"},
		{"https://ai.example.com", "wss://ai.example.com/ws"},
		{"http://localhost:8085/api", "ws://localhost:8085/ws"},
	} {
		got := New(c.in, "", Handlers{}).getWebSocketURL()
		if got != c.want {
			t.Errorf("getWebSocketURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A nil handler is not a crash: a daemon that only wants settings registers
// only that one.
func TestNilHandlersAreIgnored(t *testing.T) {
	f := newFakeAIManager(t, func(conn *websocket.Conn) {
		send(t, conn, "ANALYSIS_RULE_UPDATED", map[string]any{"id": 1})
		send(t, conn, "FEED_IMPORTED", nil)
		send(t, conn, "MODULE_AI_SETTING_UPDATED", map[string]any{"moduleName": "x"})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	w := New(f.srv.URL, "", Handlers{})
	w.Start()
	defer w.Close()

	waitFor(t, 2*time.Second, "a connection", func() bool { return f.connections() > 0 })
	time.Sleep(100 * time.Millisecond)
	if f.connections() != 1 {
		t.Errorf("the watcher reconnected %d times; nil handlers should be no-ops", f.connections())
	}
}

// The service key is not sent when none is configured.
func TestNoKeyMeansNoHeader(t *testing.T) {
	f := newFakeAIManager(t, func(conn *websocket.Conn) {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	w := New(f.srv.URL, "", Handlers{})
	w.Start()
	defer w.Close()

	waitFor(t, 2*time.Second, "a connection", func() bool { return f.connections() > 0 })
	if key := f.key(); key != "" {
		t.Errorf("service key header = %q with no key configured", key)
	}
}

// Guard against the test helper drifting from the package's own constants.
func TestDebounceDefaultIsFiveSeconds(t *testing.T) {
	w := New("http://localhost:1", "", Handlers{})
	if w.debounceFor != 5*time.Second {
		t.Errorf("default debounce = %v, want 5s", w.debounceFor)
	}
	if !strings.Contains(w.getWebSocketURL(), "/ws") {
		t.Error("the default URL does not target /ws")
	}
}
