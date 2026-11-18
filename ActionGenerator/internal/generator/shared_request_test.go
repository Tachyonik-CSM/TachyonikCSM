// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins that in a shared workspace an evaluation request is evaluated for the
// primary owner and its action placed under them, as the shared pass would
// (AG-EVAL-13): a member's request reads the workspace's data, raises no copy
// of their own, and withdraws for every owner when the rule does not match.
// When the workspace mode cannot be read, the request is evaluated for the
// requester (AG-EVAL-08).

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

// sharedFixture: an appliance with primary owner 2 and member 5, a fake
// ActionManager keeping its actions, and rule 1 matching while things > 1.
type sharedFixture struct {
	g  *Generator
	am *fakeActionManager

	mu        sync.Mutex
	count     int
	readFor   []string // the X-User-ID of every bridge call
	workspace bool     // false: SystemManager cannot tell the mode
}

func newSharedFixture(t *testing.T) *sharedFixture {
	t.Helper()
	f := &sharedFixture{count: 3, workspace: true, am: &fakeActionManager{}}
	things := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.readFor = append(f.readFor, r.Header.Get("X-User-ID"))
		fmt.Fprintf(w, `{"count":%d}`, f.count)
	}))
	t.Cleanup(things.Close)
	sm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		ws := f.workspace
		f.mu.Unlock()
		switch r.URL.Path {
		case "/api/internal/workspace":
			if !ws {
				http.Error(w, "down", http.StatusInternalServerError)
				return
			}
			w.Write([]byte(`{"instanceMode":"appliance","primaryUserId":2}`))
		case "/api/internal/users/5":
			w.Write([]byte(`{"id":5,"username":"member","role":"registered","active":true}`))
		case "/api/internal/users/6":
			w.Write([]byte(`{"id":6,"username":"gone","role":"registered","active":false}`))
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

func (f *sharedFixture) readers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.readFor...)
}

func TestAMembersRequestIsEvaluatedForThePrimaryOwner(t *testing.T) {
	f := newSharedFixture(t)
	mine := f.am.add(5, 1, "New", "Tidy your things") // a copy from per-user days

	created, err := f.g.ProcessRuleForUser(1, 5, false)
	if err != nil || created != 1 {
		t.Fatalf("created=%d err=%v, want one action", created, err)
	}
	for _, u := range f.readers() {
		if u != "2" {
			t.Errorf("the bridge read for user %q, want the primary owner 2", u)
		}
	}
	if n := len(f.am.ofRule(2, 1)); n != 1 {
		t.Errorf("the primary owner has %d actions of the rule, want 1", n)
	}
	if f.am.get(mine) != nil {
		t.Error("the member's New copy was not consolidated away")
	}
}

func TestAMembersRequestWithdrawsForEveryOwner(t *testing.T) {
	f := newSharedFixture(t)
	f.count = 0
	owners := f.am.add(2, 1, "New", "Tidy your things")
	members := f.am.add(5, 1, "New", "Tidy your things")
	if _, err := f.g.ProcessRuleForUser(1, 5, false); err != nil {
		t.Fatal(err)
	}
	if f.am.get(owners) != nil || f.am.get(members) != nil {
		t.Error("a New action of the rule was left although it no longer matches")
	}
}

func TestAForcedRequestInASharedWorkspaceRaisesUnderTheOwner(t *testing.T) {
	f := newSharedFixture(t)
	f.count = 0 // forced: the condition is skipped
	f.am.add(2, 1, "Done", "Tidy your things")
	created, err := f.g.ProcessRuleForUser(1, 5, true)
	if err != nil || created != 1 {
		t.Fatalf("created=%d err=%v, want the forced action raised", created, err)
	}
	if n := len(f.am.ofRule(5, 1)); n != 0 {
		t.Errorf("the member got %d actions of the rule, want none", n)
	}
}

func TestAnUnreadableWorkspaceEvaluatesForTheRequester(t *testing.T) {
	f := newSharedFixture(t)
	f.workspace = false
	if _, err := f.g.ProcessRuleForUser(1, 5, false); err != nil {
		t.Fatal(err)
	}
	for _, u := range f.readers() {
		if u != "5" {
			t.Errorf("the bridge read for user %q, want the requester 5", u)
		}
	}
	if n := len(f.am.ofRule(5, 1)); n != 1 {
		t.Errorf("the requester has %d actions of the rule, want 1", n)
	}
}

func TestADeactivatedRequesterIsRefusedInASharedWorkspace(t *testing.T) {
	f := newSharedFixture(t)
	if _, err := f.g.ProcessRuleForUser(1, 6, false); err == nil {
		t.Error("a deactivated requester's request was evaluated")
	}
	if len(f.readers()) != 0 {
		t.Error("the bridge was called for a refused request")
	}
}
