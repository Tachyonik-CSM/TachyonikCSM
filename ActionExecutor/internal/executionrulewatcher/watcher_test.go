// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the execution-rule watcher's dispatch.
//
// The connection machinery is wswatcher's and is tested there; what is tested
// here is what this package decides — which AIManager events it reacts to, how
// their payloads are read, and that per-rule events are debounced while a feed
// import is not.

package executionrulewatcher

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func fakeAIManager(t *testing.T, frames []map[string]any) *httptest.Server {
	t.Helper()
	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for _, f := range frames {
			raw, _ := json.Marshal(f)
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

func TestRuleEventsDispatchAndDebounce(t *testing.T) {
	srv := fakeAIManager(t, []map[string]any{
		{"type": "EXECUTION_RULE_UPDATED", "payload": map[string]any{"id": 4}},
		{"type": "EXECUTION_RULE_UPDATED", "payload": map[string]any{"id": 4}},
		{"type": "EXECUTION_RULE_DELETED", "payload": map[string]any{"id": 5}},
		{"type": "ACTION_RULE_UPDATED", "payload": map[string]any{"id": 6}}, // another module's
	})

	var mu sync.Mutex
	seen := map[int64]RuleChangeEvent{}
	count := map[int64]int{}
	w, _ := New(srv.URL, "k", func(e RuleChangeEvent) {
		mu.Lock()
		seen[e.RuleID] = e
		count[e.RuleID]++
		mu.Unlock()
	}, nil)
	w.debounceFor = 30 * time.Millisecond
	w.Start()
	defer w.Close()

	waitFor(t, 3*time.Second, "both rules", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == 2
	})
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if count[4] != 1 {
		t.Errorf("rule 4 fired %d times, want 1 — two events should collapse", count[4])
	}
	if seen[4].Type != "updated" || seen[5].Type != "deleted" {
		t.Errorf("event types = %q / %q", seen[4].Type, seen[5].Type)
	}
	if _, ok := seen[6]; ok {
		t.Error("an action-rule event reached the execution-rule handler")
	}
}

func TestFeedImportedTriggersReload(t *testing.T) {
	srv := fakeAIManager(t, []map[string]any{{"type": "FEED_IMPORTED"}})
	fired := make(chan struct{}, 1)
	w, _ := New(srv.URL, "", func(RuleChangeEvent) {}, func() {
		select {
		case fired <- struct{}{}:
		default:
		}
	})
	w.Start()
	defer w.Close()
	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Fatal("FEED_IMPORTED did not trigger a reload")
	}
}

func TestUnsetHandlersAreSafe(t *testing.T) {
	srv := fakeAIManager(t, []map[string]any{
		{"type": "EXECUTION_RULE_UPDATED", "payload": map[string]any{"id": 1}},
		{"type": "FEED_IMPORTED"},
	})
	w, _ := New(srv.URL, "", nil, nil)
	w.Start()
	time.Sleep(150 * time.Millisecond)
	if err := w.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestCloseIsIdempotentAndSafeWithoutStart(t *testing.T) {
	w, _ := New("http://127.0.0.1:1", "", func(RuleChangeEvent) {}, nil)
	if err := w.Close(); err != nil {
		t.Errorf("Close without Start: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// A user's request to run a rule now is handed on at once, not debounced:
// two presses are two requests.
func TestRunRequestsAreHandedOnUndebounced(t *testing.T) {
	srv := fakeAIManager(t, []map[string]any{
		{"type": "EXECUTION_REQUESTED", "payload": map[string]any{"id": 9, "executionRuleId": 4, "userId": 52, "userRole": "admin"}},
		{"type": "EXECUTION_REQUESTED", "payload": map[string]any{"id": 10, "executionRuleId": 4, "userId": 52, "userRole": "admin"}},
	})
	got := make(chan RunRequest, 4)
	w, _ := New(srv.URL, "", func(RuleChangeEvent) {}, nil)
	w.SetRunRequestHandler(func(r RunRequest) { got <- r })
	w.Start()
	defer w.Close()

	seen := map[int64]RunRequest{}
	for len(seen) < 2 {
		select {
		case r := <-got:
			seen[r.ID] = r
		case <-time.After(3 * time.Second):
			t.Fatalf("got %v, want requests 9 and 10", seen)
		}
	}
	if r := seen[9]; r.ExecutionRuleID != 4 || r.UserID != 52 || r.UserRole != "admin" {
		t.Errorf("request 9 = %+v", r)
	}
}

// Every connect is reported, so requests made while it was down are picked up.
func TestAConnectIsReported(t *testing.T) {
	srv := fakeAIManager(t, nil)
	connected := make(chan struct{}, 1)
	w, _ := New(srv.URL, "", func(RuleChangeEvent) {}, nil)
	w.SetConnectHandler(func() {
		select {
		case connected <- struct{}{}:
		default:
		}
	})
	w.Start()
	defer w.Close()
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("the connect was not reported")
	}
}
