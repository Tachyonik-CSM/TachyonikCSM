// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins that context routines — routines without `var routineContract = 2;`,
// written for the rule context earlier versions built — are no longer run or
// generated: an active one is held back wherever rules are loaded (AG-EVAL-24),
// a generated one fails validation, and a system prompt from before the
// platform API is refused before the AI is asked (AG-GEN-11, AG-GEN-21).

package aigenerator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"tachyonik/actiongenerator/internal/actionrulewatcher"
	"tachyonik/actiongenerator/internal/aimanager"
	"tachyonik/actiongenerator/internal/config"
)

const contextRoutine = `var rules = [{
  name: "has-assets", ruleId: 1,
  check: function (ctx) { return ctx.assetCount > 0; },
  createAction: function (ctx) { return { title: "Review your assets", type: "Info", priority: 50, description: "" }; }
}];`

// fakeAIManager serves rule 1 with active routine 5 holding code, and records
// the routines stored and the generation request cleared.
type fakeAIManager struct {
	mu      sync.Mutex
	stored  []map[string]any
	cleared map[string]any
}

func (f *fakeAIManager) serve(t *testing.T, code string, generate bool) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256([]byte(code))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/internal/action-rules":
			rule := map[string]any{"id": 1, "title": "Review your assets", "triggerPrompt": "There are assets.",
				"actionType": "Info", "actionPriority": 50, "activeRoutine": 5}
			if generate {
				rule["generateRequestedAt"] = "2026-11-17T12:00:00Z"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"actionRules": []map[string]any{rule}})
		case r.Method == "GET" && r.URL.Path == "/api/internal/routines/5":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 5, "rule": 1, "code": code, "status": "passed",
				"sha256": hex.EncodeToString(sum[:])})
		case r.Method == "GET" && r.URL.Path == "/api/internal/action-options":
			_, _ = w.Write([]byte(`{"actionOptions": []}`))
		case r.Method == "POST" && r.URL.Path == "/api/internal/routines":
			var body map[string]any
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			f.mu.Lock()
			f.stored = append(f.stored, body)
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id": 99, "rule": 1, "type": "ActionGenerator"}`))
		case r.Method == "PATCH" && r.URL.Path == "/api/internal/action-rules/1":
			raw, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			_ = json.Unmarshal(raw, &f.cleared)
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// AG-EVAL-24: an active context routine is not loaded, on startup or feed
// import (LoadRules) nor on a rule change (HandleRuleChange); a bridge routine
// in the same place is.
func TestAnActiveContextRoutineIsHeldBack(t *testing.T) {
	for _, c := range []struct {
		name string
		code string
		runs bool
	}{{"context routine", contextRoutine, false}, {"bridge routine", bridgeRoutine, true}} {
		f := &fakeAIManager{}
		srv := f.serve(t, c.code, false)
		ag := New(aimanager.NewClient(srv.URL, "k"), nil, &config.Config{AI: config.AIConfig{JSExecTimeoutSeconds: 5}})
		ag.SetCatalogue(thingsCatalogue(t))

		if err := ag.LoadRules(); err != nil {
			t.Fatal(err)
		}
		if _, runs := ag.Executors()[1]; runs != c.runs {
			t.Errorf("%s, LoadRules: runs = %v, want %v", c.name, runs, c.runs)
		}
		ag.HandleRuleChange(actionrulewatcher.RuleChangeEvent{Type: "updated", RuleID: 1})
		if _, runs := ag.Executors()[1]; runs != c.runs {
			t.Errorf("%s, rule change: runs = %v, want %v", c.name, runs, c.runs)
		}
	}
}

// AG-GEN-11: a model that writes a context routine has written one that fails.
func TestAGeneratedContextRoutineFails(t *testing.T) {
	f := &fakeAIManager{}
	srv := f.serve(t, bridgeRoutine, true)
	chat := &recordingChat{answer: contextRoutine}
	ag := New(aimanager.NewClient(srv.URL, "k"), chat, &config.Config{AI: config.AIConfig{
		SystemPrompt: "Write a routine.\n" + APICataloguePlaceholder, Model: "m", JSExecTimeoutSeconds: 5}})
	ag.SetCatalogue(thingsCatalogue(t))
	ag.HandleRuleChange(actionrulewatcher.RuleChangeEvent{Type: "updated", RuleID: 1})

	f.mu.Lock()
	defer f.mu.Unlock()
	const want = "the routine must begin with var routineContract = 2;"
	if len(f.stored) != 1 || f.stored[0]["status"] != "failed" || !strings.Contains(f.stored[0]["log"].(string), want) {
		t.Errorf("stored = %v, want one failed routine whose log says %q", f.stored, want)
	}
	if reason, _ := f.cleared["generateError"].(string); !strings.Contains(reason, want) {
		t.Errorf("generation request cleared with %v, want the reason %q", f.cleared, want)
	}
}

// AG-GEN-21: a system prompt without {{API_CATALOGUE}} predates the platform
// API. The AI is not asked, and the request fails saying what to apply.
func TestAPromptWithoutTheCatalogueIsRefused(t *testing.T) {
	f := &fakeAIManager{}
	srv := f.serve(t, bridgeRoutine, true)
	chat := &recordingChat{answer: bridgeRoutine}
	ag := New(aimanager.NewClient(srv.URL, "k"), chat, &config.Config{AI: config.AIConfig{
		SystemPrompt: "Write a routine reading ctx.", Model: "m", JSExecTimeoutSeconds: 5}})
	ag.SetCatalogue(thingsCatalogue(t))
	ag.HandleRuleChange(actionrulewatcher.RuleChangeEvent{Type: "updated", RuleID: 1})

	if n := chat.calls(); n != 0 {
		t.Errorf("the AI was asked %d time(s)", n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.stored) != 0 {
		t.Errorf("stored %v, want nothing", f.stored)
	}
	if reason, _ := f.cleared["generateError"].(string); !strings.Contains(reason, "predates the platform API") ||
		!strings.Contains(reason, "prompts/actiongenerator.txt") {
		t.Errorf("generation request cleared with %v", f.cleared)
	}
}

type recordingChat struct {
	mu     sync.Mutex
	n      int
	answer string
}

func (c *recordingChat) Chat(model, systemPrompt, userPrompt string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return c.answer, nil
}

func (c *recordingChat) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
