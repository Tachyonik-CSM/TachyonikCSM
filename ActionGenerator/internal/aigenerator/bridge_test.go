// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins how bridge routines are generated and loaded: a system prompt with
// {{API_CATALOGUE}} gets the catalogue's operations (PA-RT-05); the routine is
// validated against the mock answers and stored with the operations it calls
// and the catalogue fingerprint (PA-RT-04); and an active routine whose
// operation the catalogue no longer has is held back (PA-RT-06).

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
	"tachyonik/actiongenerator/internal/catalogue"
	"tachyonik/actiongenerator/internal/config"
	"tachyonik/lib/apibridge"
	"tachyonik/lib/apicatalogue"
)

func thingsCatalogue(t *testing.T) *catalogue.Holder {
	t.Helper()
	var ex strings.Builder
	for _, sc := range apicatalogue.Scenarios {
		v := "{ count: 2 }"
		if strings.HasPrefix(sc, "anonymous") || sc == "admin-empty" {
			v = "{ count: 0 }"
		}
		ex.WriteString("                " + sc + ": { value: " + v + " }\n")
	}
	doc := `openapi: 3.1.0
info: { title: Things, version: "1" }
paths:
  /api/things/count:
    get:
      operationId: things.count
      summary: How many things the user has
      x-bridge: read
      x-invalidated-by: [THING_CREATED]
      parameters:
        - { name: X-User-ID, in: header, x-bridge-bound: user, schema: { type: integer } }
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: { type: object, properties: { count: { type: integer, description: The number of things. } } }
              examples:
` + ex.String()
	h := catalogue.New(map[string]catalogue.Endpoint{"assetmanager": {BaseURL: "http://127.0.0.1:1"}}, apibridge.Options{})
	if err := h.Load(&aimanager.APICatalogue{Fingerprint: "fp1", Documents: []aimanager.APIDocument{{Module: "assetmanager", Document: doc}}}); err != nil {
		t.Fatal(err)
	}
	return h
}

const bridgeRoutine = `var routineContract = 2;
var rules = [{
  name: "many-things", ruleId: 1,
  check: function (api) { return api.things.count().count > 1; },
  createAction: function (api) { return { title: "Tidy your things", type: "Info", priority: 40, description: "" }; }
}];`

type promptChat struct {
	mu     sync.Mutex
	system string
}

func (c *promptChat) Chat(model, systemPrompt, userPrompt string) (string, error) {
	c.mu.Lock()
	c.system = systemPrompt
	c.mu.Unlock()
	return bridgeRoutine, nil
}

// PA-RT-04, -05.
func TestABridgeRoutineIsGeneratedFromTheCatalogue(t *testing.T) {
	var mu sync.Mutex
	var stored map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/internal/action-rules":
			_ = json.NewEncoder(w).Encode(map[string]any{"actionRules": []map[string]any{{
				"id": 1, "title": "Tidy your things", "triggerPrompt": "The user has more than one thing.", "actionType": "Info",
				"actionPriority": 40, "generateRequestedAt": "2026-11-16T12:00:00Z",
			}}})
		case r.Method == "GET" && r.URL.Path == "/api/internal/action-options":
			_, _ = w.Write([]byte(`{"actionOptions": []}`))
		case r.Method == "POST" && r.URL.Path == "/api/internal/routines":
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			_ = json.Unmarshal(body, &stored)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id": 99, "rule": 1, "type": "ActionGenerator", "status": "passed"}`))
		case r.Method == "PATCH":
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	chat := &promptChat{}
	ag := New(aimanager.NewClient(srv.URL, "k"), chat, &config.Config{AI: config.AIConfig{
		SystemPrompt: "Write a bridge routine.\nOperations:\n{{API_CATALOGUE}}", Model: "m", JSExecTimeoutSeconds: 5}})
	ag.SetCatalogue(thingsCatalogue(t))
	ag.HandleRuleChange(actionrulewatcher.RuleChangeEvent{Type: "updated", RuleID: 1})

	chat.mu.Lock()
	system := chat.system
	chat.mu.Unlock()
	if strings.Contains(system, APICataloguePlaceholder) || !strings.Contains(system, "api.things.count() — How many things the user has") ||
		!strings.Contains(system, "count: integer — The number of things.") {
		t.Errorf("system prompt:\n%s", system)
	}
	mu.Lock()
	defer mu.Unlock()
	if stored == nil {
		t.Fatal("no routine stored")
	}
	if stored["status"] != "passed" || stored["apiFingerprint"] != "fp1" {
		t.Errorf("stored = %v", stored)
	}
	if ops, _ := stored["apiOperations"].([]any); len(ops) != 1 || ops[0] != "things.count" {
		t.Errorf("apiOperations = %v", stored["apiOperations"])
	}
}

// PA-RT-06.
func TestARoutineWhoseOperationIsGoneIsHeldBack(t *testing.T) {
	sum := sha256.Sum256([]byte(bridgeRoutine))
	for _, c := range []struct {
		ops  string
		runs bool
	}{{`["things.count"]`, true}, {`["things.count","things.vanished"]`, false}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 5, "rule": 1, "code": bridgeRoutine, "status": "passed",
				"sha256": hex.EncodeToString(sum[:]), "apiOperations": c.ops})
		}))
		ag := New(aimanager.NewClient(srv.URL, "k"), nil, &config.Config{AI: config.AIConfig{JSExecTimeoutSeconds: 5}})
		ag.SetCatalogue(thingsCatalogue(t))
		active := int64(5)
		ag.loadActiveRoutine(&aimanager.ActionRule{ID: 1, Title: "Tidy", ActiveRoutine: &active})
		srv.Close()
		e, runs := ag.executors[1]
		if runs != c.runs {
			t.Errorf("operations %s: runs = %v, want %v", c.ops, runs, c.runs)
		}
		if runs && (len(ag.Triggers()) != 1 || !ag.Triggers()["THING_CREATED"] || !e.IsBridge()) {
			t.Errorf("triggers = %v", ag.Triggers())
		}
	}
}
