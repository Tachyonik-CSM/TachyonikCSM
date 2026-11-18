// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins that a generation request still standing at startup — cut off by a
// stop, or asked for while SourceAnalyser was not running — is cleared with the
// reason, and a rule without one is left alone.

package aianalyser

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"tachyonik/sourceanalyser/internal/aimanager"
	"tachyonik/sourceanalyser/internal/config"
)

func TestAStandingRequestIsClearedAtStartup(t *testing.T) {
	var mu sync.Mutex
	cleared := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/internal/analysis-rules":
			w.Write([]byte(`{"analysisRules":[{"id":1,"type":"Standing","generateRequestedAt":"2026-11-18T10:00:00Z"},{"id":2,"type":"Idle"}]}`))
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
	x := New(aimanager.NewClient(srv.URL, "k"), nil, &config.Config{})
	if err := x.LoadRules(); err != nil {
		t.Fatal(err)
	}
	x.ClearStandingRequests()
	if len(cleared) != 1 {
		t.Fatalf("cleared %v, want only rule 1", cleared)
	}
	if body := cleared["/api/internal/analysis-rules/1"]; body["clearGenerateRequest"] != true || body["generateError"] != interruptedRequestReason {
		t.Errorf("clear request = %v", body)
	}
}
