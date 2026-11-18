// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins that a generation request made before the start — cut off by a stop,
// or asked for while ActionExecutor was not running — is cleared with the
// reason. A rule without a request, and a request made after the
// start, are left alone.

package aiexecutor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"tachyonik/actionexecutor/internal/aimanager"
	"tachyonik/actionexecutor/internal/config"
)

func TestAStandingRequestIsClearedAtStartup(t *testing.T) {
	var mu sync.Mutex
	cleared := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/internal/execution-rules":
			w.Write([]byte(`{"executionRules":[{"id":1,"title":"Standing","generateRequestedAt":"2026-11-18T10:00:00Z"},{"id":2,"title":"Idle"},{"id":3,"title":"Fresh","generateRequestedAt":"2026-11-18T12:00:00Z"}]}`))
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
	// Started at 11:00: rule 1 was asked for before, rule 3 after —
	// rule 3 may be being handled right now.
	ag.ClearStandingRequests(time.Date(2026, 11, 18, 11, 0, 0, 0, time.UTC))
	if len(cleared) != 1 {
		t.Fatalf("cleared %v, want only rule 1", cleared)
	}
	for path, body := range cleared {
		if !strings.HasSuffix(path, "/1") || body["generateError"] != interruptedRequestReason {
			t.Errorf("clear request %s = %v", path, body)
		}
	}
}
