// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins bridge routines (PA-RT-01 to -03, PA-VAL-02): how one is recognised,
// that it reads through the API bridge for the user evaluated, that a failed
// call leaves the outcome unknown rather than "does not match", and how it is
// validated against the managers' mock answers.

package jsruntime

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tachyonik/lib/apibridge"
	"tachyonik/lib/apicatalogue"
)

// thingsDocument has one read operation whose mock answer differs between
// scenarios: no things for the anonymous and the empty one, two otherwise.
func thingsDocument(t *testing.T) *apicatalogue.Document {
	t.Helper()
	var ex strings.Builder
	for _, sc := range apicatalogue.Scenarios {
		v := "{ things: [ { id: 1, stale: true }, { id: 2, stale: false } ] }"
		if sc == "anonymous-at-source-limit" || sc == "admin-empty" {
			v = "{ things: [] }"
		}
		ex.WriteString("                " + sc + ": { value: " + v + " }\n")
	}
	doc, err := apicatalogue.Parse([]byte(`openapi: 3.1.0
info: { title: Things, version: "1" }
paths:
  /api/things:
    get:
      operationId: things.list
      x-bridge: read
      x-invalidated-by: [THING_CREATED]
      parameters:
        - { name: X-User-ID, in: header, x-bridge-bound: user, schema: { type: integer } }
        - { name: filter, in: query, content: { application/json: { schema: { type: object } } } }
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  things: { type: array, items: { type: object, properties: { id: { type: integer }, stale: { type: boolean } } } }
              examples:
` + ex.String()))
	if err != nil {
		t.Fatal(err)
	}
	if errs := doc.Check(); len(errs) > 0 {
		t.Fatal(errs)
	}
	return doc
}

func thingsBridge(t *testing.T, status int, answer string) (*apibridge.Bridge, *[]string) {
	t.Helper()
	var users []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		users = append(users, r.Header.Get("X-User-ID"))
		w.WriteHeader(status)
		w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	b, err := apibridge.New([]apibridge.Module{{Name: "things", Document: thingsDocument(t), BaseURL: srv.URL, ServiceKey: "k"}},
		apibridge.ReadOnly, apibridge.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return b, &users
}

const staleThingsRoutine = `var routineContract = 2;
function stale(api) {
  return api.things.list().things.filter(function (t) { return t.stale; }).length;
}
var rules = [{
  name: "stale-things", ruleId: 7,
  check: function (api) { return stale(api) > 0; },
  createAction: function (api) {
    return { title: "Clean up " + stale(api) + " stale things", type: "Fix", description: "", priority: 50, trigger: "stale" };
  },
  options: { "3": function (api) { return api.things.list().things.length > 1; } }
}];`

func loadBridgeRoutine(t *testing.T, code string) *JSRuleExecutor {
	t.Helper()
	e := New(5 * time.Second)
	if err := e.LoadFromString(code); err != nil {
		t.Fatalf("load: %v", err)
	}
	return e
}

// PA-RT-01.
func TestABridgeRoutineIsRecognisedByItsContract(t *testing.T) {
	if !loadBridgeRoutine(t, staleThingsRoutine).IsBridge() {
		t.Error("routineContract = 2 is a bridge routine")
	}
	ctxRoutine := `var rules = [{name: "r", ruleId: 1, check: function (ctx) { return false; }, createAction: function () { return {}; }}];`
	if loadBridgeRoutine(t, ctxRoutine).IsBridge() {
		t.Error("a routine without routineContract is a context routine")
	}
}

// PA-RT-02, PA-BR-03.
func TestABridgeRoutineReadsForTheUserEvaluated(t *testing.T) {
	b, users := thingsBridge(t, 200, `{"things":[{"id":1,"stale":true},{"id":2,"stale":false}]}`)
	actions, unknown, err := loadBridgeRoutine(t, staleThingsRoutine).EvaluateBridge(b.NewPass(), 42, false)
	if err != nil || unknown {
		t.Fatalf("err=%v unknown=%v", err, unknown)
	}
	if len(actions) != 1 || actions[0].Title != "Clean up 1 stale things" {
		t.Fatalf("actions = %+v", actions)
	}
	if actions[0].HiddenOptions == nil || len(*actions[0].HiddenOptions) != 0 {
		t.Errorf("option 3 applies (two things): hidden = %v", actions[0].HiddenOptions)
	}
	if len(*users) != 1 || (*users)[0] != "42" {
		t.Errorf("calls made for %v; want one, for user 42 — the pass answers repeats", *users)
	}
}

// PA-RT-03: a failed call is not "does not match".
func TestAFailedCallLeavesTheOutcomeUnknown(t *testing.T) {
	b, _ := thingsBridge(t, 503, `{"error":"down"}`)
	actions, unknown, err := loadBridgeRoutine(t, staleThingsRoutine).EvaluateBridge(b.NewPass(), 42, false)
	if err != nil {
		t.Fatal(err)
	}
	if !unknown || len(actions) != 0 {
		t.Errorf("unknown=%v actions=%v, want unknown and none", unknown, actions)
	}
}

// PA-VAL-02, PA-RT-04.
func TestBridgeValidation(t *testing.T) {
	b, _ := thingsBridge(t, 200, `{}`)

	report := loadBridgeRoutine(t, staleThingsRoutine).ValidateBridge(b)
	if len(report.Errors) != 0 {
		t.Fatalf("a correct routine: %v", report.Errors)
	}
	if strings.Join(report.Operations, ",") != "things.list" {
		t.Errorf("operations = %v", report.Operations)
	}
	if !strings.Contains(report.Log(), "API operations called: things.list") || !strings.Contains(report.Log(), "things.list:things[].stale") {
		t.Errorf("log:\n%s", report.Log())
	}

	for name, c := range map[string]struct{ code, want string }{
		"invented field": {strings.Replace(staleThingsRoutine, "t.stale; })", "t.stael; })", 1), "reads things[].stael"},
		"unknown argument": {strings.Replace(staleThingsRoutine, "api.things.list().things.filter", "api.things.list({colour: 1}).things.filter", 1), `no parameter "colour"`},
		"always true":      {strings.Replace(staleThingsRoutine, "return stale(api) > 0;", "return api.things.list() !== null;", 1), "true in every mock scenario"},
		"reads nothing":    {strings.Replace(staleThingsRoutine, "return stale(api) > 0;", "return false;", 1), "would never fire"},
	} {
		report := loadBridgeRoutine(t, c.code).ValidateBridge(b)
		found := false
		for _, e := range report.Errors {
			found = found || strings.Contains(e.Error(), c.want)
		}
		if !found {
			t.Errorf("%s: errors %v, want one with %q", name, report.Errors, c.want)
		}
	}

	if r := loadBridgeRoutine(t, staleThingsRoutine).ValidateBridge(nil); len(r.Errors) != 1 {
		t.Errorf("without a catalogue: %v", r.Errors)
	}
}
