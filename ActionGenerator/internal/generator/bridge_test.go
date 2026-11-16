// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins how the engine treats a bridge routine's outcome (PA-RT-03): a rule
// that evaluated to false has its New actions withdrawn, as any rule; one
// whose outcome is unknown — a bridge call failed — keeps them.

package generator

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tachyonik/actiongenerator/internal/actionmanager"
	"tachyonik/actiongenerator/internal/aimanager"
	"tachyonik/actiongenerator/internal/catalogue"
	"tachyonik/actiongenerator/internal/jsruntime"
	"tachyonik/actiongenerator/internal/systemmanager"
	"tachyonik/lib/apibridge"
	"tachyonik/lib/apicatalogue"
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

// withdrawals runs the count rule for user 42 with the things manager
// answering status/body, and returns how often the rule's actions were
// withdrawn.
func withdrawals(t *testing.T, status int, body string) int32 {
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
	var withdrawn atomic.Int32
	am := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/delete-by-rule") {
			withdrawn.Add(1)
		}
		w.Write([]byte(`{"deleted":0,"deletedIds":[]}`))
	}))
	t.Cleanup(am.Close)

	h := catalogue.New(map[string]catalogue.Endpoint{"assetmanager": {BaseURL: things.URL}}, apibridge.Options{})
	if err := h.Load(&aimanager.APICatalogue{Documents: []aimanager.APIDocument{{Module: "assetmanager", Document: countDocument()}}}); err != nil {
		t.Fatal(err)
	}
	e := jsruntime.New(5 * time.Second)
	if err := e.LoadFromString(countRoutine); err != nil {
		t.Fatal(err)
	}
	g := New(systemmanager.NewClient(sm.URL, "k"), nil, nil, actionmanager.NewClient(am.URL, "k"), nil)
	g.SetCatalogue(h)
	g.SetExecutors(map[int64]*jsruntime.JSRuleExecutor{1: e})
	if _, err := g.ProcessRuleForUser(1, 42, false); err != nil {
		t.Fatal(err)
	}
	return withdrawn.Load()
}

func TestAFalseBridgeRuleWithdraws(t *testing.T) {
	if n := withdrawals(t, 200, `{"count":0}`); n != 1 {
		t.Errorf("withdrawals = %d, want 1", n)
	}
}

func TestAnUndecidedBridgeRuleDoesNotWithdraw(t *testing.T) {
	if n := withdrawals(t, 503, `{"error":"down"}`); n != 0 {
		t.Errorf("withdrawals = %d, want 0: the rule could not see the data", n)
	}
}
