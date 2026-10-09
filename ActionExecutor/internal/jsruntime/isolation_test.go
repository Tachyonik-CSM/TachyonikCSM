// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that one run of a routine cannot see what another left behind.
//
// The routine used to live in one VM shared by every run of its rule, so state
// kept outside run() — a cache, a counter — carried over into the next run,
// another user's included. Each run now has a VM of its own.

package jsruntime

import (
	"sync"
	"testing"
	"time"

	"github.com/dop251/goja"
)

// A routine that remembers the last user it ran for, as a cache would.
const remembersLastUser = `var lastUser = null;
var executors = [{ name: "e", ruleId: 4, run: function (api) {
	var seen = lastUser;
	lastUser = api.user;
	return { status: "success", message: "previous: " + seen };
} }];`

func asUser(name string) func(vm *goja.Runtime) goja.Value {
	return func(vm *goja.Runtime) goja.Value {
		api := vm.NewObject()
		_ = api.Set("user", name)
		return api
	}
}

func TestARunSeesNothingOfAnotherUsersRun(t *testing.T) {
	rt := New()
	if err := rt.LoadFromString(remembersLastUser); err != nil {
		t.Fatalf("load: %v", err)
	}
	if res, _ := rt.Run(asUser("alice"), time.Second); res.Message != "previous: null" {
		t.Fatalf("first run: %q", res.Message)
	}
	res, _ := rt.Run(asUser("bob"), time.Second)
	if res.Message != "previous: null" {
		t.Errorf("bob's run saw %q — alice's run leaked into it", res.Message)
	}
}

// Runs of one rule no longer queue: with a VM each, two runs overlap.
func TestRunsOfOneRuleRunSideBySide(t *testing.T) {
	rt := New()
	code := `var executors = [{ name: "e", ruleId: 4, run: function (api) {
		var until = Date.now() + 200; while (Date.now() < until) {}
		return { status: "success", message: "done" };
	} }];`
	if err := rt.LoadFromString(code); err != nil {
		t.Fatalf("load: %v", err)
	}
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = rt.Run(nil, 5*time.Second) }()
	}
	wg.Wait()
	if took := time.Since(start); took > 350*time.Millisecond {
		t.Errorf("two 200 ms runs took %v; they still queue", took)
	}
}

// A routine whose top-level code never finishes is refused when it is loaded,
// within the load budget, instead of hanging the loader.
func TestALoopingTopLevelIsRefusedOnLoad(t *testing.T) {
	start := time.Now()
	err := New().LoadFromString(`while (true) {}
	var executors = [];`)
	if err == nil {
		t.Fatal("a routine whose top level loops was loaded")
	}
	if took := time.Since(start); took > LoadBudget+2*time.Second {
		t.Errorf("refusing it took %v", took)
	}
}
