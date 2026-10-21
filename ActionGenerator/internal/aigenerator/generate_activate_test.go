// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests a generation request end to end against a fake AIManager and a fake
// AI: a request that asks for it makes the new routine the active one as it
// clears, one that does not leaves the active routine alone.

package aigenerator

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"tachyonik/actiongenerator/internal/actionrulewatcher"
	"tachyonik/actiongenerator/internal/aimanager"
	"tachyonik/actiongenerator/internal/config"
)

// A routine that passes validation: its check depends on the context, so it
// answers differently across the mock scenarios.
const generatedActionRoutine = `
var rules = [{
  name: "has-assets",
  ruleId: 1,
  check: function (ctx) { return ctx.assetCount > 0; },
  createAction: function (ctx) {
    return { title: "Review your assets", type: "Info", status: "New", priority: 50, description: "You have " + ctx.assetCount + " assets." };
  }
}];
`

type fakeChat struct{}

func (fakeChat) Chat(model, systemPrompt, userPrompt string) (string, error) {
	return generatedActionRoutine, nil
}

func generateThroughEvent(t *testing.T, activate bool) map[string]any {
	t.Helper()
	var mu sync.Mutex
	var cleared map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/internal/action-rules":
			_ = json.NewEncoder(w).Encode(map[string]any{"actionRules": []map[string]any{{
				"id": 1, "title": "Review your assets", "triggerPrompt": "There are assets.", "actionType": "Info",
				"actionPriority": 50, "generateRequestedAt": "2026-10-21T12:00:00Z", "generateActivate": activate,
			}}})
		case r.Method == "GET" && r.URL.Path == "/api/internal/action-options":
			_, _ = w.Write([]byte(`{"actionOptions": []}`))
		case r.Method == "POST" && r.URL.Path == "/api/internal/routines":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id": 99, "rule": 1, "type": "ActionGenerator", "status": "passed"}`))
		case r.Method == "PATCH" && r.URL.Path == "/api/internal/action-rules/1":
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

	ag := New(aimanager.NewClient(srv.URL, "k"), fakeChat{}, &config.Config{AI: config.AIConfig{SystemPrompt: "p", Model: "m", JSExecTimeoutSeconds: 5}})
	ag.HandleRuleChange(actionrulewatcher.RuleChangeEvent{Type: "updated", RuleID: 1})
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
	if cleared["activeRoutine"] != float64(99) {
		t.Errorf("cleared with %v, want activeRoutine 99", cleared)
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
