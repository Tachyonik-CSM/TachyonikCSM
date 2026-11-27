// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins one action set per SaaS workspace, owned by the workspace admin
// (docs/plans/workspace-actions-once.md, AG-EVAL-06, -07, -12, -13): the pass
// evaluates a workspace once, for its admin, places the action under them and
// withdraws the members' New copies, keeping those a member took on; a rule
// that stops matching is withdrawn for every member; a member's request is
// evaluated for the admin; a user in no workspace is evaluated as before, and
// a SystemManager that does not name workspaces gives per-user evaluation.

package generator

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"tachyonik/actiongenerator/internal/actionmanager"
	"tachyonik/actiongenerator/internal/jsruntime"
	"tachyonik/actiongenerator/internal/systemmanager"
	"tachyonik/lib/apibridge"
	"tachyonik/lib/apiholder"
)

// workspaceFixture: a SaaS installation with the workspace of admin 2
// (members 5 and 6, 6 deactivated) and user 9 in none; rule 1 matches while
// things > 1.
type workspaceFixture struct {
	g  *Generator
	am *fakeActionManager

	mu        sync.Mutex
	count     int
	readFor   []string
	workspace bool // false: an old SystemManager that sends no workspaceAdminId
}

func newWorkspaceFixture(t *testing.T) *workspaceFixture {
	t.Helper()
	f := &workspaceFixture{count: 3, workspace: true, am: &fakeActionManager{}}
	things := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.readFor = append(f.readFor, r.Header.Get("X-User-ID"))
		fmt.Fprintf(w, `{"count":%d}`, f.count)
	}))
	t.Cleanup(things.Close)
	user := func(id int64, active bool, admin int64) string {
		f.mu.Lock()
		ws := f.workspace
		f.mu.Unlock()
		s := fmt.Sprintf(`{"id":%d,"username":"u%d","role":"registered","active":%v`, id, id, active)
		if ws && admin != 0 {
			s += fmt.Sprintf(`,"workspaceAdminId":%d`, admin)
		}
		return s + "}"
	}
	sm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/internal/workspace":
			w.Write([]byte(`{"instanceMode":"saas","primaryUserId":0}`))
		case "/api/internal/users":
			fmt.Fprintf(w, "[%s,%s,%s,%s]", user(2, true, 2), user(5, true, 2), user(6, false, 2), user(9, true, 0))
		case "/api/internal/users/5":
			w.Write([]byte(user(5, true, 2)))
		case "/api/internal/users/9":
			w.Write([]byte(user(9, true, 0)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(sm.Close)
	amSrv := httptest.NewServer(f.am)
	t.Cleanup(amSrv.Close)

	h := apiholder.New(map[string]apiholder.Endpoint{"assetmanager": {BaseURL: things.URL}}, apibridge.ReadOnly, apibridge.Options{})
	if err := h.Load("", []apiholder.Document{{Module: "assetmanager", Document: countDocument()}}); err != nil {
		t.Fatal(err)
	}
	e := jsruntime.New(5 * time.Second)
	if err := e.LoadFromString(countRoutine); err != nil {
		t.Fatal(err)
	}
	f.g = New(systemmanager.NewClient(sm.URL, "k"), actionmanager.NewClient(amSrv.URL, "k"))
	f.g.SetCatalogue(h)
	f.g.SetExecutors(map[int64]*jsruntime.JSRuleExecutor{1: e})
	return f
}

func (f *workspaceFixture) readers() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for _, u := range f.readFor {
		out[u]++
	}
	return out
}

func TestAWorkspaceGetsOneActionSetUnderItsAdmin(t *testing.T) {
	f := newWorkspaceFixture(t)
	membersNew := f.am.add(5, 1, "New", "Tidy your things")
	takenOn := f.am.add(5, 1, "InProgress", "Tidy your things")
	deactivatedsNew := f.am.add(6, 1, "New", "Tidy your things")

	if err := f.g.ProcessRules(); err != nil {
		t.Fatal(err)
	}
	read := f.readers()
	if read["5"] != 0 || read["6"] != 0 || read["2"] == 0 || read["9"] == 0 {
		t.Errorf("evaluated for %v; want the admin 2 and the user in no workspace 9 only", read)
	}
	if n := len(f.am.ofRule(2, 1)); n != 1 {
		t.Errorf("the admin has %d actions of the rule, want 1", n)
	}
	if n := len(f.am.ofRule(9, 1)); n != 1 {
		t.Errorf("the user in no workspace has %d actions of the rule, want 1", n)
	}
	if f.am.get(membersNew) != nil {
		t.Error("the member's New copy was not consolidated away")
	}
	if f.am.get(deactivatedsNew) != nil {
		t.Error("the deactivated member's New copy was not consolidated away (D2)")
	}
	if f.am.get(takenOn) == nil {
		t.Error("the copy the member took on was withdrawn")
	}
}

func TestARuleThatStopsMatchingIsWithdrawnForTheWholeWorkspace(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.count = 0
	admins := f.am.add(2, 1, "New", "Tidy your things")
	members := f.am.add(5, 1, "New", "Tidy your things")
	if err := f.g.ProcessRules(); err != nil {
		t.Fatal(err)
	}
	if f.am.get(admins) != nil || f.am.get(members) != nil {
		t.Error("a New action of the rule was left in the workspace although it no longer matches")
	}
}

func TestAMembersRequestIsEvaluatedForTheWorkspaceAdmin(t *testing.T) {
	f := newWorkspaceFixture(t)
	mine := f.am.add(5, 1, "New", "Tidy your things")
	created, err := f.g.ProcessRuleForUser(1, 5, false)
	if err != nil || created != 1 {
		t.Fatalf("created=%d err=%v, want one action", created, err)
	}
	if read := f.readers(); read["5"] != 0 || read["2"] == 0 {
		t.Errorf("evaluated for %v, want the admin", read)
	}
	if n := len(f.am.ofRule(2, 1)); n != 1 {
		t.Errorf("the admin has %d actions of the rule, want 1", n)
	}
	if f.am.get(mine) != nil {
		t.Error("the member's New copy was not consolidated away")
	}

	// Every rule at once (the action icon) goes the same way.
	if _, err := f.g.ProcessRulesForUser(5); err != nil {
		t.Fatal(err)
	}
	if n := len(f.am.ofRule(5, 1)); n != 0 {
		t.Errorf("the member got %d actions of the rule, want none", n)
	}
}

func TestWithoutWorkspacesInTheAnswerEveryUserIsEvaluatedOnTheirOwn(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.workspace = false
	if err := f.g.ProcessRules(); err != nil {
		t.Fatal(err)
	}
	for _, u := range []int64{2, 5, 9} {
		if n := len(f.am.ofRule(u, 1)); n != 1 {
			t.Errorf("user %d has %d actions of the rule, want 1 (per-user evaluation)", u, n)
		}
	}
	if n := len(f.am.ofRule(6, 1)); n != 0 {
		t.Errorf("the deactivated user got %d actions, want none (AG-EVAL-25)", n)
	}
}
