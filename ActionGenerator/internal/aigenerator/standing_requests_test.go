// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins that a generation request still standing at startup — cut off by a
// stop, or asked for while ActionGenerator was not running — is cleared with
// the reason (AG-GEN-02), and a rule without one is left alone.

package aigenerator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"tachyonik/actiongenerator/internal/aimanager"
	"tachyonik/actiongenerator/internal/config"
)

func TestAStandingRequestIsClearedAtStartup(t *testing.T) {
	var mu sync.Mutex
	cleared := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/internal/action-rules":
			w.Write([]byte(`{"actionRules":[{"id":1,"title":"Standing","generateRequestedAt":"2026-11-18T10:00:00Z"},{"id":2,"title":"Idle"}]}`))
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
	ag.ClearStandingRequests()
	if len(cleared) != 1 {
		t.Fatalf("cleared %v, want only rule 1", cleared)
	}
	body := cleared["/api/internal/action-rules/1"]
	if body["clearGenerateRequest"] != true || body["generateError"] != interruptedRequestReason {
		t.Errorf("clear request = %v", body)
	}
}
