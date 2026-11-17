// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins how the engine treats a routine's outcome (PA-RT-03): a rule that
// evaluated to false has its New actions withdrawn; one whose outcome is
// unknown — a bridge call failed — keeps them, and so does a context routine,
// which is not run at all. A forced evaluation request raises the action
// without the condition (AG-EVAL-13).

package generator

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tachyonik/actiongenerator/internal/actionmanager"
	"tachyonik/actiongenerator/internal/jsruntime"
	"tachyonik/actiongenerator/internal/systemmanager"
	"tachyonik/lib/apibridge"
	"tachyonik/lib/apicatalogue"
	"tachyonik/lib/apiholder"
)

const countRoutine = `var routineContract = 2;
var rules = [{
  name: "many-things", ruleId: 1,
  check: function (api) { return api.things.count().count > 1; },
  createAction: function (api) { return { title: "Tidy your things", type: "Info", priority: 40, description: "" }; }
}];`

func countDocument() string {
	var ex strings.Builder
	for _, sc := range apicatalogue.Scenarios {
		ex.WriteString("                " + sc + ": { value: { count: 0 } }\n")
	}
	return `openapi: 3.1.0
info: { title: Things, version: "1" }
paths:
  /api/things/count:
    get:
      operationId: things.count
      x-bridge: read
      x-invalidated-by: [THING_CREATED]
      parameters:
        - { name: X-User-ID, in: header, x-bridge-bound: user, schema: { type: integer } }
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: { type: object, properties: { count: { type: integer } } }
              examples:
` + ex.String()
}

// outcome is what one evaluation request did to ActionManager.
type outcome struct{ withdrawn, created int32 }

// withdrawals runs the count rule for user 42 with the things manager
// answering status/body, and returns how often the rule's actions were
// withdrawn.
func withdrawals(t *testing.T, status int, body string) int32 {
	t.Helper()
	return evaluateFor42(t, countRoutine, status, body, false).withdrawn
}

func evaluateFor42(t *testing.T, routine string, status int, body string, force bool) outcome {
	t.Helper()
	things := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(things.Close)
	sm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":42,"username":"u","role":"registered"}`))
	}))
	t.Cleanup(sm.Close)
	var withdrawn, created atomic.Int32
	am := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/delete-by-rule"):
			withdrawn.Add(1)
			w.Write([]byte(`{"deleted":0,"deletedIds":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/actions":
			created.Add(1)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":7,"title":"Tidy your things"}`))
		default:
			w.Write([]byte(`{"actions":[]}`))
		}
	}))
	t.Cleanup(am.Close)

	h := apiholder.New(map[string]apiholder.Endpoint{"assetmanager": {BaseURL: things.URL}}, apibridge.ReadOnly, apibridge.Options{})
	if err := h.Load("", []apiholder.Document{{Module: "assetmanager", Document: countDocument()}}); err != nil {
		t.Fatal(err)
	}
	e := jsruntime.New(5 * time.Second)
	if err := e.LoadFromString(routine); err != nil {
		t.Fatal(err)
	}
	g := New(systemmanager.NewClient(sm.URL, "k"), actionmanager.NewClient(am.URL, "k"))
	g.SetCatalogue(h)
	g.SetExecutors(map[int64]*jsruntime.JSRuleExecutor{1: e})
	if _, err := g.ProcessRuleForUser(1, 42, force); err != nil {
		t.Fatal(err)
	}
	return outcome{withdrawn.Load(), created.Load()}
}

// AG-EVAL-11.
func TestAFalseBridgeRuleWithdraws(t *testing.T) {
	if n := withdrawals(t, 200, `{"count":0}`); n != 1 {
		t.Errorf("withdrawals = %d, want 1", n)
	}
}

// AG-EVAL-22, PA-RT-03.
func TestAnUndecidedBridgeRuleDoesNotWithdraw(t *testing.T) {
	if n := withdrawals(t, 503, `{"error":"down"}`); n != 0 {
		t.Errorf("withdrawals = %d, want 0: the rule could not see the data", n)
	}
}

// AG-EVAL-13: a forced request raises the action whatever check() says.
func TestAForcedRequestRaisesTheAction(t *testing.T) {
	if o := evaluateFor42(t, countRoutine, 200, `{"count":0}`, true); o.created != 1 || o.withdrawn != 0 {
		t.Errorf("created %d, withdrew %d; want 1 and 0", o.created, o.withdrawn)
	}
}

// AG-EVAL-24: a context routine that reached the engine anyway is not run,
// and its rule's actions are left as they are.
func TestAContextRoutineNeitherCreatesNorWithdraws(t *testing.T) {
	const contextRoutine = `var rules = [{ name: "old", ruleId: 1,
  check: function (ctx) { return ctx.assetCount > 0; },
  createAction: function (ctx) { return { title: "Old", type: "Info", priority: 10 }; } }];`
	for _, force := range []bool{false, true} {
		if o := evaluateFor42(t, contextRoutine, 200, `{"count":5}`, force); o.created != 0 || o.withdrawn != 0 {
			t.Errorf("force=%v: created %d, withdrew %d; want neither", force, o.created, o.withdrawn)
		}
	}
}
