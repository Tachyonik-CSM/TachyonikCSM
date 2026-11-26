// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins that a pass evaluates an owner's rules from the highest action priority
// to the lowest (AG-EVAL-28): with an ActionManager that refuses actions beyond
// a limit, the actions created are those of the most important rules, per user
// and in a shared workspace alike.

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

// limitedPass runs one pass over four always-matching rules, with priorities
// 30 (rule 1), 100 (rule 2), 90 (rule 3) and 100 (rule 4), against an
// ActionManager that holds at most two actions per owner, and returns the
// rules whose actions were created, per owner.
func limitedPass(t *testing.T, appliance bool) map[int64][]int64 {
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
	g := New(systemmanager.NewClient(sm.URL, "k"), actionmanager.NewClient(am.URL, "k"))
	g.SetCatalogue(h)
	g.SetExecutors(executors)
	// As aigenerator.RuleOrder sorts them: 100 (2), 100 (4), 90 (3), 30 (1).
	g.SetRuleOrder([]int64{2, 4, 3, 1})
	if err := g.ProcessRules(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	return created
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
