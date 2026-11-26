// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins that a pass evaluates an owner's rules from the highest action priority
// to the lowest (AG-EVAL-28): with an ActionManager that refuses actions beyond
// a limit, the actions created are those of the most important rules, per user
// and in a shared workspace alike. The same for a request for every rule of
// one user (AG-EVAL-29), which also withdraws for a rule that does not match
// and leaves an on-demand rule alone.

package generator

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"tachyonik/actiongenerator/internal/actionmanager"
	"tachyonik/actiongenerator/internal/jsruntime"
	"tachyonik/actiongenerator/internal/systemmanager"
	"tachyonik/lib/apibridge"
	"tachyonik/lib/apiholder"
)

func TestOrderedRulesFollowsTheOrderAndKeepsTheRest(t *testing.T) {
	ex := map[int64]*jsruntime.JSRuleExecutor{1: nil, 2: nil, 3: nil, 4: nil, 9: nil, 7: nil}
	// 5 has no executor (no active routine); 7 and 9 are not in the order.
	got := orderedRules(ex, []int64{3, 5, 1, 4, 2})
	want := []int64{3, 1, 4, 2, 7, 9}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// matchingRoutine always raises an action for its rule, with its priority.
func matchingRoutine(ruleID int64, priority int) string {
	return fmt.Sprintf(`var routineContract = 2;
var rules = [{
  name: "rule-%[1]d", ruleId: %[1]d,
  check: function (api) { return api.things.count().count > 1; },
  createAction: function (api) { return { title: "Rule %[1]d", type: "Info", priority: %[2]d, description: "" }; }
}];`, ruleID, priority)
}

// neverRoutine never raises an action for its rule.
func neverRoutine(ruleID int64) string {
	return fmt.Sprintf(`var routineContract = 2;
var rules = [{
  name: "rule-%[1]d", ruleId: %[1]d,
  check: function (api) { return api.things.count().count > 100; },
  createAction: function (api) { return { title: "Rule %[1]d", type: "Info", priority: 50, description: "" }; }
}];`, ruleID)
}

// limitedResult is what a run did to ActionManager: the rules whose actions
// were created and those whose actions were withdrawn, per owner.
type limitedResult struct {
	created   map[int64][]int64
	withdrawn map[int64][]int64
}

// limitedPass runs one pass over four always-matching rules, with priorities
// 30 (rule 1), 100 (rule 2), 90 (rule 3) and 100 (rule 4), against an
// ActionManager that holds at most two actions per owner, and returns the
// rules whose actions were created, per owner.
func limitedPass(t *testing.T, appliance bool) map[int64][]int64 {
	t.Helper()
	return limitedRun(t, appliance, func(g *Generator) {
		if err := g.ProcessRules(); err != nil {
			t.Fatal(err)
		}
	}).created
}

// limitedRun sets the four rules of limitedPass up, plus rule 5, which never
// matches, and rule 6, on-demand and never matching, and calls run.
func limitedRun(t *testing.T, appliance bool, run func(g *Generator)) limitedResult {
	t.Helper()
	things := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"count":5}`))
	}))
	t.Cleanup(things.Close)
	sm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/internal/users":
			w.Write([]byte(`[{"id":1,"username":"a","role":"registered","active":true},` +
				`{"id":2,"username":"b","role":"registered","active":true}]`))
		case "/api/internal/users/1":
			w.Write([]byte(`{"id":1,"username":"a","role":"registered","active":true}`))
		case "/api/internal/users/2":
			w.Write([]byte(`{"id":2,"username":"b","role":"registered","active":true}`))
		case "/api/internal/workspace":
			if !appliance {
				http.NotFound(w, r)
				return
			}
			w.Write([]byte(`{"instanceMode":"appliance","primaryUserId":2}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(sm.Close)

	var mu sync.Mutex
	created := map[int64][]int64{}
	withdrawn := map[int64][]int64{}
	am := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/actions":
			var req struct {
				UserID       int64  `json:"userId"`
				ActionRuleID *int64 `json:"actionRuleId"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			defer mu.Unlock()
			if len(created[req.UserID]) >= 2 {
				http.Error(w, "Action limit reached (2/2)", http.StatusForbidden)
				return
			}
			created[req.UserID] = append(created[req.UserID], *req.ActionRuleID)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":7}`))
		case strings.Contains(r.URL.Path, "delete-by-rule"):
			var req struct {
				UserID       int64 `json:"userId"`
				ActionRuleID int64 `json:"actionRuleId"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			withdrawn[req.UserID] = append(withdrawn[req.UserID], req.ActionRuleID)
			mu.Unlock()
			w.Write([]byte(`{"deleted":0,"deletedIds":[]}`))
		default:
			w.Write([]byte(`{"actions":[]}`))
		}
	}))
	t.Cleanup(am.Close)

	h := apiholder.New(map[string]apiholder.Endpoint{"assetmanager": {BaseURL: things.URL}}, apibridge.ReadOnly, apibridge.Options{})
	if err := h.Load("", []apiholder.Document{{Module: "assetmanager", Document: countDocument()}}); err != nil {
		t.Fatal(err)
	}
	priorities := map[int64]int{1: 30, 2: 100, 3: 90, 4: 100}
	executors := map[int64]*jsruntime.JSRuleExecutor{}
	for id, p := range priorities {
		e := jsruntime.New(5 * time.Second)
		if err := e.LoadFromString(matchingRoutine(id, p)); err != nil {
			t.Fatal(err)
		}
		executors[id] = e
	}
	for _, id := range []int64{5, 6} {
		e := jsruntime.New(5 * time.Second)
		if err := e.LoadFromString(neverRoutine(id)); err != nil {
			t.Fatal(err)
		}
		executors[id] = e
	}
	g := New(systemmanager.NewClient(sm.URL, "k"), actionmanager.NewClient(am.URL, "k"))
	g.SetCatalogue(h)
	g.SetExecutors(executors)
	g.SetOnDemandRules(map[int64]bool{6: true})
	// As aigenerator.RuleOrder sorts them: 100 (2), 100 (4), 90 (3), 50 (5,
	// 6), 30 (1).
	g.SetRuleOrder([]int64{2, 4, 3, 5, 6, 1})
	run(g)
	mu.Lock()
	defer mu.Unlock()
	return limitedResult{created, withdrawn}
}

func TestAPassCreatesTheHighestPriorityActionsFirst(t *testing.T) {
	got := limitedPass(t, false)
	want := map[int64][]int64{1: {2, 4}, 2: {2, 4}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("created per user = %v, want %v: the two rules of priority 100", got, want)
	}
}

func TestASharedWorkspacePassCreatesTheHighestPriorityActionsFirst(t *testing.T) {
	got := limitedPass(t, true)
	want := map[int64][]int64{2: {2, 4}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("created per owner = %v, want %v: the two rules of priority 100, under the primary owner", got, want)
	}
}

func TestARequestForEveryRuleEvaluatesThemAllForTheUser(t *testing.T) {
	got := limitedRun(t, false, func(g *Generator) {
		n, err := g.ProcessRulesForUser(1)
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Errorf("created = %d, want 2", n)
		}
	})
	if want := map[int64][]int64{1: {2, 4}}; !reflect.DeepEqual(got.created, want) {
		t.Errorf("created per user = %v, want %v: the two rules of priority 100, for user 1 only", got.created, want)
	}
	if want := map[int64][]int64{1: {5}}; !reflect.DeepEqual(got.withdrawn, want) {
		t.Errorf("withdrawn per user = %v, want %v: rule 5, which does not match, not the on-demand rule 6", got.withdrawn, want)
	}
}

func TestARequestForEveryRuleInASharedWorkspaceGoesToTheOwner(t *testing.T) {
	got := limitedRun(t, true, func(g *Generator) {
		if _, err := g.ProcessRulesForUser(1); err != nil {
			t.Fatal(err)
		}
	})
	if want := map[int64][]int64{2: {2, 4}}; !reflect.DeepEqual(got.created, want) {
		t.Errorf("created per owner = %v, want %v: under the primary owner 2, not the requester 1", got.created, want)
	}
}
