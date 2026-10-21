// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests a generation request end to end against a fake AIManager and a fake
// AI: a request that asks for it makes the new routine the active one as it
// clears, one that does not leaves the active routine alone — and in neither
// case does the new routine run here before AIManager names it.

package aiimporter

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"tachyonik/sourceimporter/internal/aimanager"
	"tachyonik/sourceimporter/internal/config"
	"tachyonik/sourceimporter/internal/importrulewatcher"
)

type fakeChat struct{}

func (fakeChat) Chat(model, systemPrompt, userPrompt string) (string, error) {
	return sampleImportRoutine, nil
}

func generateThroughEvent(t *testing.T, activate bool) map[string]any {
	t.Helper()
	var mu sync.Mutex
	var cleared map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/internal/import-rules/1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 1, "type": "nmap", "version": "1.0", "rulePrompt": "hosts", "generateRequestedAt": "2026-10-21T12:00:00Z", "generateActivate": activate,
			})
		case r.Method == "POST" && r.URL.Path == "/api/internal/routines":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id": 88, "rule": 1, "type": "SourceImporter", "status": "passed"}`))
		case r.Method == "PATCH" && r.URL.Path == "/api/internal/import-rules/1":
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			_ = json.Unmarshal(body, &cleared)
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	ai := New(nil, aimanager.NewClient(srv.URL, "k"), nil, fakeChat{}, &config.Config{AI: config.AIConfig{SystemPrompt: "p", Model: "m"}})
	ai.HandleRuleChange(importrulewatcher.RuleChangeEvent{Type: "updated", RuleID: 1})
	if ai.HasExecutor(1) {
		t.Error("the new routine runs before AIManager names it the active one")
	}
	mu.Lock()
	defer mu.Unlock()
	if cleared == nil {
		t.Fatal("the generation request was not cleared")
	}
	return cleared
}

func TestAGenerationAskedToActivateMakesTheRoutineActive(t *testing.T) {
	cleared := generateThroughEvent(t, true)
	if cleared["generateError"] != "" {
		t.Fatalf("generation failed: %v", cleared["generateError"])
	}
	if cleared["activeRoutine"] != float64(88) {
		t.Errorf("cleared with %v, want activeRoutine 88", cleared)
	}
}

func TestAGenerationNotAskedToActivateLeavesTheActiveRoutine(t *testing.T) {
	cleared := generateThroughEvent(t, false)
	if cleared["generateError"] != "" {
		t.Fatalf("generation failed: %v", cleared["generateError"])
	}
	if _, ok := cleared["activeRoutine"]; ok {
		t.Errorf("cleared with %v, want no activeRoutine", cleared)
	}
}
