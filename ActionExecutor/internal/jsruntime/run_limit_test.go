// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the time limit of a real routine run.
//
// Without one, a routine stuck in a loop held its runtime's lock for good:
// every later run of the same rule — manual or automatic — queued behind it
// until the daemon restarted.

package jsruntime

import (
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
)

func loaded(t *testing.T, run string) *JSExecutorRuntime {
	t.Helper()
	rt := New()
	code := `var executors = [{ name: "e", ruleId: 4, run: function (api) { ` + run + ` } }];`
	if err := rt.LoadFromString(code); err != nil {
		t.Fatalf("load: %v", err)
	}
	return rt
}

func TestARunawayRoutineIsStoppedAtItsLimit(t *testing.T) {
	rt := loaded(t, `while (true) {}`)

	start := time.Now()
	res, err := rt.Run(nil, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the run took %v; the limit did not stop it", took)
	}
	if res.Status != "error" || !strings.Contains(res.Message, "exceeded its time limit of 100ms") {
		t.Errorf("got %+v, want a failure naming the time limit", res)
	}
}

// After a timeout the runtime is usable again: the lock is released and the
// interrupt does not linger to stop the next run on its first statement.
func TestTheNextRunAfterATimeoutWorks(t *testing.T) {
	rt := New()
	// The api object decides whether this run loops, so one routine can time
	// out once and then run normally.
	code := `var executors = [{ name: "e", ruleId: 4, run: function (api) {
		if (api && api.loop) { while (true) {} }
		return { status: "success", message: "ran" };
	} }];`
	if err := rt.LoadFromString(code); err != nil {
		t.Fatalf("load: %v", err)
	}
	looping := func(vm *goja.Runtime) goja.Value {
		api := vm.NewObject()
		_ = api.Set("loop", true)
		return api
	}
	if res, _ := rt.Run(looping, 50*time.Millisecond); res.Status != "error" {
		t.Fatalf("first run: %+v, want a timeout", res)
	}
	res, err := rt.Run(nil, time.Second)
	if err != nil || res.Status != "success" {
		t.Errorf("the run after a timeout: %+v, %v", res, err)
	}
}

func TestAFastRoutineIsUnaffected(t *testing.T) {
	rt := loaded(t, `return { status: "success", message: "done" };`)
	for i := 0; i < 3; i++ {
		res, err := rt.Run(nil, time.Second)
		if err != nil || res.Status != "success" {
			t.Fatalf("run %d: %+v, %v", i, res, err)
		}
	}
}

// A limit that fires right as a run ends cannot touch the next run, which has
// a VM of its own.
func TestALimitThatFiresAtTheEndDoesNotCarryOver(t *testing.T) {
	rt := loaded(t, `return { status: "success", message: "done" };`)
	rt.Run(nil, time.Nanosecond) // may or may not be interrupted
	res, err := rt.Run(nil, 0)
	if err != nil || res.Status != "success" {
		t.Errorf("a run after a near-instant limit: %+v, %v", res, err)
	}
}

// Runaway recursion fails the run with an error instead of overflowing the Go
// stack, which would take the whole daemon down.
func TestRunawayRecursionFailsTheRun(t *testing.T) {
	rt := New()
	if err := rt.LoadFromString(`function down(n) { return down(n + 1); }
		var executors = [{ name: "e", ruleId: 4, run: function (api) { return down(0); } }];`); err != nil {
		t.Fatalf("load: %v", err)
	}
	res, err := rt.Run(nil, 5*time.Second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "error" || !strings.Contains(res.Message, "maximum call depth") {
		t.Errorf("got %+v, want a failed run naming the call depth", res)
	}
}
