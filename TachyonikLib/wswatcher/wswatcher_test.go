// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the shared connection machinery.
//
// These pin the behaviour that is easy to leave out of a hand-rolled watcher
// and expensive to omit: reconnecting, answering PING, surviving noise on the
// wire, resyncing on connect, and closing twice without panicking. Six daemons
// now share this code, so a regression here is a regression everywhere.

package wswatcher

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakeManager struct {
	srv  *httptest.Server
	mu   sync.Mutex
	n    int
	key  string
	path string
}

func newFakeManager(t *testing.T, fn func(*websocket.Conn)) *fakeManager {
	t.Helper()
	f := &fakeManager{}
	var up websocket.Upgrader
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.n++
		f.key = r.Header.Get("X-Internal-Service-Key")
		f.path = r.URL.Path
		f.mu.Unlock()
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		fn(conn)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeManager) conns() int       { f.mu.Lock(); defer f.mu.Unlock(); return f.n }
func (f *fakeManager) lastKey() string  { f.mu.Lock(); defer f.mu.Unlock(); return f.key }
func (f *fakeManager) lastPath() string { f.mu.Lock(); defer f.mu.Unlock(); return f.path }

func drain(conn *websocket.Conn) {
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

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

func TestURLDerivation(t *testing.T) {
	for _, c := range []struct{ in, path, want string }{
		{"http://localhost:8081", "", "ws://localhost:8081/ws"},
		{"https://assets.example.com", "", "wss://assets.example.com/ws"},
		{"http://localhost:8081/api/v1", "", "ws://localhost:8081/ws"},
		{"http://localhost:8081", "/events", "ws://localhost:8081/events"},
	} {
		got := New(Config{BaseURL: c.in, Path: c.path}).URL()
		if got != c.want {
			t.Errorf("URL(%q, path %q) = %q, want %q", c.in, c.path, got, c.want)
		}
	}
}

func TestDialsWithTheServiceKey(t *testing.T) {
	f := newFakeManager(t, drain)
	w := New(Config{Name: "Test", BaseURL: f.srv.URL, ServiceKey: "k"})
	w.Start()
	defer w.Close()

	waitFor(t, 2*time.Second, "a connection", func() bool { return f.conns() > 0 })
	if f.lastPath() != "/ws" {
		t.Errorf("path = %q", f.lastPath())
	}
	if f.lastKey() != "k" {
		t.Errorf("key = %q", f.lastKey())
	}
}

func TestNoKeyMeansNoHeader(t *testing.T) {
	f := newFakeManager(t, drain)
	w := New(Config{BaseURL: f.srv.URL})
	w.Start()
	defer w.Close()

	waitFor(t, 2*time.Second, "a connection", func() bool { return f.conns() > 0 })
	if f.lastKey() != "" {
		t.Errorf("key = %q with none configured", f.lastKey())
	}
}

func TestMessagesReachTheHandler(t *testing.T) {
	f := newFakeManager(t, func(conn *websocket.Conn) {
		raw, _ := json.Marshal(Message{Type: "THING_HAPPENED", Payload: json.RawMessage(`{"id":3}`)})
		conn.WriteMessage(websocket.TextMessage, raw)
		drain(conn)
	})

	var mu sync.Mutex
	var got []Message
	w := New(Config{BaseURL: f.srv.URL, OnMessage: func(m Message) {
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
	}})
	w.Start()
	defer w.Close()

	waitFor(t, 2*time.Second, "the message", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) > 0
	})
	mu.Lock()
	defer mu.Unlock()
	if got[0].Type != "THING_HAPPENED" || string(got[0].Payload) != `{"id":3}` {
		t.Errorf("message = %+v", got[0])
	}
}

// PING is answered inside the JSON envelope and never reaches the handler.
func TestPingIsAnsweredAndNotDispatched(t *testing.T) {
	pong := make(chan string, 1)
	f := newFakeManager(t, func(conn *websocket.Conn) {
		raw, _ := json.Marshal(Message{Type: "PING"})
		conn.WriteMessage(websocket.TextMessage, raw)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m Message
			if json.Unmarshal(data, &m) == nil {
				select {
				case pong <- m.Type:
				default:
				}
			}
		}
	})

	var dispatched sync.Map
	w := New(Config{BaseURL: f.srv.URL, OnMessage: func(m Message) { dispatched.Store(m.Type, true) }})
	w.Start()
	defer w.Close()

	select {
	case got := <-pong:
		if got != "PONG" {
			t.Errorf("answered PING with %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("PING went unanswered")
	}
	if _, seen := dispatched.Load("PING"); seen {
		t.Error("PING reached the message handler; it is answered here")
	}
}

func TestResyncsOnEveryConnect(t *testing.T) {
	var once sync.Once
	f := newFakeManager(t, func(conn *websocket.Conn) {
		once.Do(func() { conn.Close() })
		drain(conn)
	})

	var mu sync.Mutex
	n := 0
	w := New(Config{BaseURL: f.srv.URL, ReconnectDelay: 20 * time.Millisecond,
		OnConnect: func() { mu.Lock(); n++; mu.Unlock() }})
	w.Start()
	defer w.Close()

	waitFor(t, 5*time.Second, "two resyncs", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return n >= 2
	})
}

// Noise must not end the read loop: managers broadcast plenty a given watcher
// does not care about, and some of it may not even be the expected shape.
func TestNoiseDoesNotDropTheConnection(t *testing.T) {
	f := newFakeManager(t, func(conn *websocket.Conn) {
		conn.WriteMessage(websocket.TextMessage, []byte("not json"))
		time.Sleep(20 * time.Millisecond)
		raw, _ := json.Marshal(Message{Type: "AFTER"})
		conn.WriteMessage(websocket.TextMessage, raw)
		drain(conn)
	})

	got := make(chan string, 4)
	w := New(Config{BaseURL: f.srv.URL, OnMessage: func(m Message) { got <- m.Type }})
	w.Start()
	defer w.Close()

	select {
	case typ := <-got:
		if typ != "AFTER" {
			t.Errorf("first dispatched message = %q", typ)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the message after the noise never arrived")
	}
	if f.conns() != 1 {
		t.Errorf("reconnected %d times over unparseable input", f.conns())
	}
}

func TestCloseIsIdempotentAndSafeBeforeStart(t *testing.T) {
	f := newFakeManager(t, drain)
	w := New(Config{BaseURL: f.srv.URL})
	w.Start()
	waitFor(t, 2*time.Second, "a connection", func() bool { return f.conns() > 0 })

	if err := w.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if err := New(Config{BaseURL: "http://127.0.0.1:1"}).Close(); err != nil {
		t.Errorf("Close before Start: %v", err)
	}
}

// A closed watcher stops reconnecting rather than spinning on a dead server.
func TestCloseStopsReconnecting(t *testing.T) {
	f := newFakeManager(t, func(conn *websocket.Conn) { conn.Close() })
	w := New(Config{BaseURL: f.srv.URL, ReconnectDelay: 10 * time.Millisecond})
	w.Start()
	waitFor(t, 3*time.Second, "a couple of connections", func() bool { return f.conns() >= 2 })

	w.Close()
	settled := f.conns()
	time.Sleep(150 * time.Millisecond)
	if grew := f.conns() - settled; grew > 1 {
		t.Errorf("reconnected %d more times after Close", grew)
	}
}

func TestDebouncerCollapsesPerKey(t *testing.T) {
	d := NewDebouncer(30*time.Millisecond, "test")
	defer d.Stop()

	var mu sync.Mutex
	fired := map[int64]int{}
	for i := 0; i < 5; i++ {
		d.Trigger(1, func() { mu.Lock(); fired[1]++; mu.Unlock() })
	}
	d.Trigger(2, func() { mu.Lock(); fired[2]++; mu.Unlock() })

	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if fired[1] != 1 {
		t.Errorf("key 1 fired %d times, want 1", fired[1])
	}
	if fired[2] != 1 {
		t.Errorf("key 2 fired %d times, want 1", fired[2])
	}
}

// Stop must make a later Trigger a no-op rather than a panic: a message can
// still be in flight when the watcher closes, and writing to a nil map panics.
func TestDebouncerStopIsSafe(t *testing.T) {
	d := NewDebouncer(10*time.Millisecond, "test")
	d.Stop()
	d.Stop()
	d.Trigger(1, func() { t.Error("a trigger after Stop fired") })
	time.Sleep(50 * time.Millisecond)
}
