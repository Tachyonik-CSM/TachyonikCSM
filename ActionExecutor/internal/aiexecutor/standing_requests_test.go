// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins that a generation request still standing at startup — cut off by a
// stop, or asked for while ActionExecutor was not running — is cleared with
// the reason, and a rule without one is left alone.

package aiexecutor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"tachyonik/actionexecutor/internal/aimanager"
	"tachyonik/actionexecutor/internal/config"
)

func TestAStandingRequestIsClearedAtStartup(t *testing.T) {
	var mu sync.Mutex
	cleared := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/internal/execution-rules":
			w.Write([]byte(`{"executionRules":[{"id":1,"title":"Standing","generateRequestedAt":"2026-11-18T10:00:00Z"},{"id":2,"title":"Idle"}]}`))
		case r.Method == "PATCH" || r.Method == "PUT" || r.Method == "POST":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			cleared[r.URL.Path] = body
			mu.Unlock()
			w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ag := New(aimanager.NewClient(srv.URL, "k"), nil, &config.Config{})
	if err := ag.LoadRules(); err != nil {
		t.Fatal(err)
	}
	ag.ClearStandingRequests()
	if len(cleared) != 1 {
		t.Fatalf("cleared %v, want only rule 1", cleared)
	}
	for path, body := range cleared {
		if !strings.HasSuffix(path, "/1") || body["generateError"] != interruptedRequestReason {
			t.Errorf("clear request %s = %v", path, body)
		}
	}
}
