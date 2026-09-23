// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the action-rule watcher's dispatch.
//
// The connection machinery is wswatcher's and is tested there; what is tested
// here is what this package decides — which AIManager events it reacts to, how
// their payloads are read, and which are debounced. The distinction that
// matters most: a rule change is collapsed, an explicit evaluation request
// never is, because that one is a person pressing a button.

package actionrulewatcher

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
		{"type": "ACTION_RULE_UPDATED", "payload": map[string]any{"id": 7}},
		{"type": "ACTION_RULE_UPDATED", "payload": map[string]any{"id": 7}},
		{"type": "ACTION_RULE_CREATED", "payload": map[string]any{"id": 8}},
	})

	var mu sync.Mutex
	seen := map[int64]RuleChangeEvent{}
	count := map[int64]int{}
	w, err := New(srv.URL, "k", func(e RuleChangeEvent) {
		mu.Lock()
		seen[e.RuleID] = e
		count[e.RuleID]++
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
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
	if count[7] != 1 {
		t.Errorf("rule 7 fired %d times, want 1 — two events should collapse", count[7])
	}
	if seen[7].Type != "updated" || seen[8].Type != "created" {
		t.Errorf("event types = %q / %q", seen[7].Type, seen[8].Type)
	}
}

// An explicit request carries the rule, the user and the force flag, and is
// NOT debounced: two presses of "test this rule" are two requests.
func TestEvaluationRequestIsNotDebounced(t *testing.T) {
	srv := fakeAIManager(t, []map[string]any{
		{"type": "ACTION_RULE_EVALUATION_REQUESTED", "payload": map[string]any{"ruleId": 7, "userId": 52, "force": true}},
		{"type": "ACTION_RULE_EVALUATION_REQUESTED", "payload": map[string]any{"ruleId": 7, "userId": 52, "force": true}},
	})

	var mu sync.Mutex
	var got []EvaluationRequestEvent
	w, _ := New(srv.URL, "", func(RuleChangeEvent) {})
	w.SetEvaluationRequestedHandler(func(e EvaluationRequestEvent) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	})
	w.Start()
	defer w.Close()

	waitFor(t, 3*time.Second, "both evaluation requests", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 2
	})
	mu.Lock()
	defer mu.Unlock()
	if got[0].RuleID != 7 || got[0].UserID != 52 || !got[0].Force {
		t.Errorf("event = %+v, want rule 7, user 52, forced", got[0])
	}
}

func TestFeedImportedTriggersReload(t *testing.T) {
	srv := fakeAIManager(t, []map[string]any{{"type": "FEED_IMPORTED"}})

	fired := make(chan struct{}, 1)
	w, _ := New(srv.URL, "", func(RuleChangeEvent) {})
	w.SetFeedImportedHandler(func() {
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

// Events this watcher does not own — another module's rule types — are ignored.
func TestUnrelatedEventsAreIgnored(t *testing.T) {
	srv := fakeAIManager(t, []map[string]any{
		{"type": "ANALYSIS_RULE_UPDATED", "payload": map[string]any{"id": 1}},
		{"type": "MODULE_AI_SETTING_UPDATED", "payload": map[string]any{"moduleName": "actiongenerator"}},
		{"type": "ACTION_RULE_UPDATED", "payload": map[string]any{"id": 9}},
	})

	var mu sync.Mutex
	var ids []int64
	w, _ := New(srv.URL, "", func(e RuleChangeEvent) {
		mu.Lock()
		ids = append(ids, e.RuleID)
		mu.Unlock()
	})
	w.debounceFor = 20 * time.Millisecond
	w.Start()
	defer w.Close()

	waitFor(t, 3*time.Second, "the action rule event", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(ids) > 0
	})
	time.Sleep(80 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(ids) != 1 || ids[0] != 9 {
		t.Errorf("dispatched %v, want only action rule 9", ids)
	}
}

// Optional handlers left unset must not crash the read loop.
func TestUnsetHandlersAreSafe(t *testing.T) {
	srv := fakeAIManager(t, []map[string]any{
		{"type": "ACTION_RULE_EVALUATION_REQUESTED", "payload": map[string]any{"ruleId": 1, "userId": 2}},
		{"type": "FEED_IMPORTED"},
	})

	w, _ := New(srv.URL, "", nil)
	w.Start()
	time.Sleep(150 * time.Millisecond)
	if err := w.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	srv := fakeAIManager(t, nil)
	w, _ := New(srv.URL, "", func(RuleChangeEvent) {})
	w.Start()
	time.Sleep(50 * time.Millisecond)
	if err := w.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// Close on a watcher that was never started must not panic on the nil inner
// watcher — main.go defers Close on a watcher whose Start it may have skipped.
func TestCloseWithoutStart(t *testing.T) {
	w, _ := New("http://127.0.0.1:1", "", func(RuleChangeEvent) {})
	if err := w.Close(); err != nil {
		t.Errorf("Close without Start: %v", err)
	}
}
