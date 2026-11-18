// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins that a generation request made before the start — cut off by a stop,
// or asked for while ActionGenerator was not running — is cleared with the
// reason (AG-GEN-02). A rule without a request, and a request made after the
// start, are left alone.

package aigenerator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"tachyonik/actiongenerator/internal/aimanager"
	"tachyonik/actiongenerator/internal/config"
)

func TestAStandingRequestIsClearedAtStartup(t *testing.T) {
	var mu sync.Mutex
	cleared := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/internal/action-rules":
			w.Write([]byte(`{"actionRules":[{"id":1,"title":"Standing","generateRequestedAt":"2026-11-18T10:00:00Z"},{"id":2,"title":"Idle"},{"id":3,"title":"Fresh","generateRequestedAt":"2026-11-18T12:00:00Z"}]}`))
		case r.Method == "PATCH":
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
	ag := New(aimanager.NewClient(srv.URL, "k"), nil, &config.Config{AI: config.AIConfig{JSExecTimeoutSeconds: 5}})
	if err := ag.LoadRules(); err != nil {
		t.Fatal(err)
	}
	// Started at 11:00: rule 1 was asked for before, rule 3 after —
	// rule 3 may be being handled right now.
	ag.ClearStandingRequests(time.Date(2026, 11, 18, 11, 0, 0, 0, time.UTC))
	if len(cleared) != 1 {
		t.Fatalf("cleared %v, want only rule 1", cleared)
	}
	body := cleared["/api/internal/action-rules/1"]
	if body["clearGenerateRequest"] != true || body["generateError"] != interruptedRequestReason {
		t.Errorf("clear request = %v", body)
	}
}
