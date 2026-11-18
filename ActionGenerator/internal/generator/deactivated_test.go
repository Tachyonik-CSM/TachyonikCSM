// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins that a deactivated account is not evaluated (AG-EVAL-25): a pass
// neither creates nor withdraws its actions, and an evaluation request for it
// is refused. An account whose SystemManager sends no `active` field counts as
// active.

package generator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// usersFixture: 1 active, 2 deactivated, 3 from a SystemManager without the
// field.
const usersFixture = `[{"id":1,"username":"a","role":"registered","active":true},` +
	`{"id":2,"username":"b","role":"registered","active":false},` +
	`{"id":3,"username":"c","role":"registered"}]`

// deactivatedFixture is a per-user generator over usersFixture whose rule
// never matches, so every evaluated user gets a withdrawal; withdrawnFor
// lists them.
type deactivatedFixture struct {
	g  *Generator
	mu sync.Mutex
	// withdrawnFor holds the users a withdrawal was sent for.
	withdrawnFor []int64
}

func newDeactivatedFixture(t *testing.T) *deactivatedFixture {
	t.Helper()
	f := &deactivatedFixture{}
	things := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"count":0}`))
	}))
	t.Cleanup(things.Close)
	sm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/internal/users":
			w.Write([]byte(usersFixture))
		case "/api/internal/users/2":
			w.Write([]byte(`{"id":2,"username":"b","role":"registered","active":false}`))
		case "/api/internal/users/3":
			w.Write([]byte(`{"id":3,"username":"c","role":"registered"}`))
		default:
			http.NotFound(w, r) // no workspace answer: per-user evaluation
		}
	}))
	t.Cleanup(sm.Close)
	am := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/delete-by-rule") {
			var body struct {
				UserID int64 `json:"userId"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.withdrawnFor = append(f.withdrawnFor, body.UserID)
			f.mu.Unlock()
			w.Write([]byte(`{"deleted":0,"deletedIds":[]}`))
			return
		}
		w.Write([]byte(`{"actions":[]}`))
	}))
	t.Cleanup(am.Close)

	h := apiholder.New(map[string]apiholder.Endpoint{"assetmanager": {BaseURL: things.URL}}, apibridge.ReadOnly, apibridge.Options{})
	if err := h.Load("", []apiholder.Document{{Module: "assetmanager", Document: countDocument()}}); err != nil {
		t.Fatal(err)
	}
	e := jsruntime.New(5 * time.Second)
	if err := e.LoadFromString(countRoutine); err != nil {
		t.Fatal(err)
	}
	f.g = New(systemmanager.NewClient(sm.URL, "k"), actionmanager.NewClient(am.URL, "k"))
	f.g.SetCatalogue(h)
	f.g.SetExecutors(map[int64]*jsruntime.JSRuleExecutor{1: e})
	return f
}

func TestAPassLeavesADeactivatedAccountAlone(t *testing.T) {
	f := newDeactivatedFixture(t)
	if err := f.g.ProcessRules(); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	got := map[int64]bool{}
	for _, id := range f.withdrawnFor {
		got[id] = true
	}
	if got[2] {
		t.Errorf("withdrawn for %v: the deactivated user 2 was evaluated", f.withdrawnFor)
	}
	if !got[1] || !got[3] {
		t.Errorf("withdrawn for %v, want users 1 and 3: an active account, and one without the field, are evaluated", f.withdrawnFor)
	}
}

func TestARequestForADeactivatedAccountIsRefused(t *testing.T) {
	f := newDeactivatedFixture(t)
	if _, err := f.g.ProcessRuleForUser(1, 2, false); err == nil || !strings.Contains(err.Error(), "deactivated") {
		t.Errorf("request for deactivated user 2: err = %v, want a refusal naming it", err)
	}
	if _, err := f.g.ProcessRuleForUser(1, 3, false); err != nil {
		t.Errorf("request for user 3 (no active field): %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.withdrawnFor {
		if id == 2 {
			t.Errorf("withdrawn for %v: the deactivated user 2 was evaluated", f.withdrawnFor)
		}
	}
}
