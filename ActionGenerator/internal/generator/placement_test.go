// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins how a rule's evaluation is put in place (AG-EVAL-09, -11, -26, -27),
// against a fake ActionManager that keeps its actions: a rule's action is its
// rule's and owner's whatever its title, a Done action holds the rule back
// until the rule has stopped matching, a forced request raises it anyway, and
// older open copies are consolidated to the newest.

package generator

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"tachyonik/actiongenerator/internal/actionmanager"
)

// fakeAction is one row of the fake ActionManager.
type fakeAction struct {
	ID       int64   `json:"id"`
	UserID   int64   `json:"userId"`
	Title    string  `json:"title"`
	Type     string  `json:"type"`
	Status   string  `json:"status"`
	Priority int     `json:"priority"`
	RuleID   *int64  `json:"actionRuleId"`
	Cleared  *string `json:"clearedAt"`
}

type fakeActionManager struct {
	mu      sync.Mutex
	actions []*fakeAction
	nextID  int64
	creates int
}

func (f *fakeActionManager) add(user int64, rule int64, status, title string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	a := &fakeAction{ID: f.nextID, UserID: user, Title: title, Type: "Fix", Status: status}
	if rule != 0 {
		r := rule
		a.RuleID = &r
	}
	f.actions = append(f.actions, a)
	return a.ID
}

func (f *fakeActionManager) get(id int64) *fakeAction {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.actions {
		if a.ID == id {
			return a
		}
	}
	return nil
}

func (f *fakeActionManager) ofRule(user, rule int64) []*fakeAction {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*fakeAction
	for _, a := range f.actions {
		if a.UserID == user && a.RuleID != nil && *a.RuleID == rule {
			out = append(out, a)
		}
	}
	return out
}

func (f *fakeActionManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	num := func(k string) int64 { v, _ := body[k].(float64); return int64(v) }
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/actions":
		var user int64
		fmt.Sscan(r.URL.Query().Get("userId"), &user)
		var list []*fakeAction
		for _, a := range f.actions {
			if a.UserID == user {
				list = append(list, a)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"actions": list, "total": len(list)})
	case r.Method == "POST" && r.URL.Path == "/api/actions":
		f.nextID++
		rule := num("actionRuleId")
		a := &fakeAction{ID: f.nextID, UserID: num("userId"), Title: body["title"].(string), Type: "Fix", Status: "New", RuleID: &rule}
		f.actions = append(f.actions, a)
		f.creates++
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(a)
	case strings.HasSuffix(r.URL.Path, "/refresh"):
		var id int64
		fmt.Sscanf(r.URL.Path, "/api/internal/actions/%d/refresh", &id)
		changed := false
		for _, a := range f.actions {
			if a.ID == id && a.Title != body["title"] {
				a.Title, changed = body["title"].(string), true
			}
		}
		json.NewEncoder(w).Encode(map[string]bool{"changed": changed})
	case r.URL.Path == "/api/internal/actions/delete-by-rule":
		user, rule, except := num("userId"), num("actionRuleId"), num("exceptActionId")
		var keep []*fakeAction
		n := 0
		for _, a := range f.actions {
			if a.UserID == user && a.RuleID != nil && *a.RuleID == rule && a.Status == "New" && a.ID != except {
				n++
				continue
			}
			keep = append(keep, a)
		}
		f.actions = keep
		json.NewEncoder(w).Encode(map[string]any{"deleted": n, "deletedIds": []int64{}})
	case r.URL.Path == "/api/internal/actions/delete-by-rule-shared":
		rule, keepUser := num("actionRuleId"), num("keepUserId")
		var keep []*fakeAction
		n := 0
		for _, a := range f.actions {
			if a.RuleID != nil && *a.RuleID == rule && a.Status == "New" && (keepUser == 0 || a.UserID != keepUser) {
				n++
				continue
			}
			keep = append(keep, a)
		}
		f.actions = keep
		json.NewEncoder(w).Encode(map[string]any{"deleted": n, "deletedIds": []int64{}})
	case r.URL.Path == "/api/internal/actions/clear-done-by-rule":
		user, rule := num("userId"), num("actionRuleId")
		n := 0
		now := "2026-11-18T12:00:00Z"
		for _, a := range f.actions {
			if (user == 0 || a.UserID == user) && a.RuleID != nil && *a.RuleID == rule && a.Status == "Done" && a.Cleared == nil {
				a.Cleared = &now
				n++
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"cleared": n, "clearedIds": []int64{}})
	default:
		http.NotFound(w, r)
	}
}

func newPlacementFixture(t *testing.T) (*Generator, *fakeActionManager) {
	t.Helper()
	f := &fakeActionManager{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return New(nil, actionmanager.NewClient(srv.URL, "k")), f
}

func titled(title string) actionmanager.CreateActionRequest {
	return actionmanager.CreateActionRequest{Title: title, Type: "Fix", Status: "New", Priority: 50}
}

func TestAChangedTitleUpdatesTheOpenAction(t *testing.T) {
	g, f := newPlacementFixture(t)
	id := f.add(1, 7, "Acknowledged", "3 hosts not seen")

	if created, err := g.placeAction(1, 7, titled("5 hosts not seen"), false); err != nil || created {
		t.Fatalf("created=%v err=%v, want the open action updated", created, err)
	}
	if a := f.get(id); a.Title != "5 hosts not seen" || a.Status != "Acknowledged" {
		t.Errorf("action = %+v, want the new title and the user's status", a)
	}
	if f.creates != 0 {
		t.Errorf("%d actions created", f.creates)
	}
}

func TestADoneActionHoldsTheRuleBackUntilItStopsMatching(t *testing.T) {
	g, f := newPlacementFixture(t)
	f.add(1, 7, "Done", "3 hosts not seen")

	// Still matching: the user has dealt with it.
	if created, _ := g.placeAction(1, 7, titled("3 hosts not seen"), false); created {
		t.Fatal("raised again while the situation goes on")
	}
	// The rule stops matching: the Done action is cleared ...
	g.withdraw(1, 7)
	// ... and the next match raises a new action.
	if created, err := g.placeAction(1, 7, titled("4 hosts not seen"), false); err != nil || !created {
		t.Fatalf("created=%v err=%v, want a new action once the situation came back", created, err)
	}
	// The new action is open; the next pass updates it, nothing more.
	if created, _ := g.placeAction(1, 7, titled("4 hosts not seen"), false); created {
		t.Error("a second action was raised")
	}
}

func TestAForcedRequestRaisesDespiteADoneAction(t *testing.T) {
	g, f := newPlacementFixture(t)
	f.add(1, 7, "Done", "Install the scanner")
	if created, err := g.placeAction(1, 7, titled("Install the scanner"), true); err != nil || !created {
		t.Fatalf("created=%v err=%v, want the forced action raised", created, err)
	}
}

func TestOlderOpenCopiesAreConsolidated(t *testing.T) {
	g, f := newPlacementFixture(t)
	older := f.add(1, 7, "New", "3 hosts not seen")
	taken := f.add(1, 7, "InProgress", "3 hosts not seen")
	newest := f.add(1, 7, "New", "4 hosts not seen")

	if _, err := g.placeAction(1, 7, titled("5 hosts not seen"), false); err != nil {
		t.Fatal(err)
	}
	if f.get(older) != nil {
		t.Error("the older New copy was kept")
	}
	if f.get(taken) == nil {
		t.Error("the copy the user took on was withdrawn")
	}
	if a := f.get(newest); a == nil || a.Title != "5 hosts not seen" {
		t.Errorf("newest = %+v, want it kept and updated", a)
	}
}

func TestAUsersOwnActionWithTheSameTitleIsNotTheRules(t *testing.T) {
	g, f := newPlacementFixture(t)
	mine := f.add(1, 0, "New", "3 hosts not seen")
	if created, err := g.placeAction(1, 7, titled("3 hosts not seen"), false); err != nil || !created {
		t.Fatalf("created=%v err=%v, want the rule's own action raised", created, err)
	}
	if a := f.get(mine); a == nil || a.Title != "3 hosts not seen" {
		t.Error("the user's own action was touched")
	}
	g.withdraw(1, 7)
	if f.get(mine) == nil {
		t.Error("the user's own action was withdrawn with the rule's")
	}
	if n := len(f.ofRule(1, 7)); n != 0 {
		t.Errorf("%d actions of the rule left after the withdrawal", n)
	}
}

func TestSeveralActionsFromOneEvaluationKeepTheFirst(t *testing.T) {
	got := firstAction(7, []actionmanager.CreateActionRequest{titled("a"), titled("b")})
	if len(got) != 1 || got[0].Title != "a" {
		t.Errorf("firstAction = %v, want only the first", got)
	}
}
