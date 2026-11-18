// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins AG-OPS-09: after Stop, the rule and user being evaluated finish —
// their withdrawal included — the pass goes no further, nothing new starts,
// and Wait returns; or, when the time runs out, names the unit still running.

package generator

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tachyonik/actiongenerator/internal/actionmanager"
	"tachyonik/actiongenerator/internal/jsruntime"
	"tachyonik/actiongenerator/internal/systemmanager"
	"tachyonik/lib/apibridge"
	"tachyonik/lib/apiholder"
)

// blockedPass is a generator for users 1 and 2 whose first bridge call waits
// until release is closed; started is closed when it arrives.
type blockedPass struct {
	g                *Generator
	started, release chan struct{}
	calls, withdrawn atomic.Int32
}

func newBlockedPass(t *testing.T) *blockedPass {
	t.Helper()
	b := &blockedPass{started: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	things := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b.calls.Add(1) == 1 {
			once.Do(func() { close(b.started) })
			<-b.release
		}
		w.Write([]byte(`{"count":0}`))
	}))
	t.Cleanup(things.Close)
	sm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/internal/users" {
			w.Write([]byte(`[{"id":1,"username":"a","role":"registered"},{"id":2,"username":"b","role":"registered"}]`))
			return
		}
		http.NotFound(w, r) // no workspace answer: per-user evaluation
	}))
	t.Cleanup(sm.Close)
	am := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/delete-by-rule") {
			b.withdrawn.Add(1)
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
	b.g = New(systemmanager.NewClient(sm.URL, "k"), actionmanager.NewClient(am.URL, "k"))
	b.g.SetCatalogue(h)
	b.g.SetExecutors(map[int64]*jsruntime.JSRuleExecutor{1: e})
	return b
}

func TestAStopLetsTheUnitInProgressFinish(t *testing.T) {
	b := newBlockedPass(t)
	done := make(chan error, 1)
	go func() { done <- b.g.ProcessRules() }()
	<-b.started

	b.g.Stop()
	if _, err := b.g.ProcessRuleForUser(1, 2, false); !errors.Is(err, ErrStopping) {
		t.Errorf("an evaluation asked for after Stop: %v", err)
	}
	close(b.release)
	if unit := b.g.Wait(5 * time.Second); unit != "" {
		t.Fatalf("still running: %s", unit)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if b.withdrawn.Load() != 1 {
		t.Errorf("withdrawals = %d: the unit in progress must finish, withdrawal included", b.withdrawn.Load())
	}
	if b.calls.Load() != 1 {
		t.Errorf("bridge calls = %d: the pass must stop after the unit in progress", b.calls.Load())
	}
	if err := b.g.ProcessRules(); err != nil || b.calls.Load() != 1 {
		t.Errorf("a pass after Stop: err %v, calls %d", err, b.calls.Load())
	}
}

func TestAStopThatRunsOutOfTimeNamesTheUnit(t *testing.T) {
	b := newBlockedPass(t)
	go func() { _ = b.g.ProcessRules() }()
	<-b.started
	b.g.Stop()
	if unit := b.g.Wait(50 * time.Millisecond); unit != "rule 1 for user 1" {
		t.Errorf("unit = %q", unit)
	}
	close(b.release)
	if unit := b.g.Wait(5 * time.Second); unit != "" {
		t.Errorf("still running: %s", unit)
	}
}
