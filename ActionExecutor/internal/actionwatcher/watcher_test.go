// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the action watcher's dispatch: ACTION_CREATED is handed on with its
// payload, everything else ActionManager broadcasts is ignored. The connection
// machinery is wswatcher's and is tested there.

package actionwatcher

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestOnlyCreatedActionsAreHandedOn(t *testing.T) {
	rule := int64(8)
	frames := []map[string]any{
		{"type": "ACTION_UPDATED", "payload": map[string]any{"id": 41}},
		{"type": "ACTION_CREATED", "payload": map[string]any{"id": 42, "userId": 52, "title": "Identify missing host assets", "actionRuleId": rule}},
	}
	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Service-Key") != "am-key" {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
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

	got := make(chan Action, 4)
	w := New(srv.URL, "am-key", func(a Action) { got <- a })
	w.Start()
	defer w.Close()

	select {
	case a := <-got:
		if a.ID != 42 || a.UserID != 52 || a.ActionRuleID == nil || *a.ActionRuleID != 8 {
			t.Errorf("handed on %+v, want action 42 of user 52 from rule 8", a)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ACTION_CREATED was not handed on")
	}
	select {
	case a := <-got:
		t.Errorf("a second action was handed on: %+v", a)
	case <-time.After(100 * time.Millisecond):
	}
}
