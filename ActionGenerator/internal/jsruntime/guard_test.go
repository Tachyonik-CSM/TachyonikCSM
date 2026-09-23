// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the execution budget around routine code.
//
// This module runs JavaScript an AI wrote from a prompt. goja cannot be
// preempted, so without a budget a rule containing `while (true) {}` hangs its
// goroutine for the life of the process — and the first place such a rule runs
// is the validation that is supposed to reject it. Every test here would hang
// rather than fail if the guard were removed, so each runs on its own timer.

package jsruntime

import (
	"strings"
	"testing"
	"time"
)

// runsWithin fails if fn has not returned by limit. Guards the guard: a
// regression here would otherwise show up as a test run that never finishes.
func runsWithin(t *testing.T, limit time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("did not return within %s — the execution budget did not interrupt the routine", limit)
	}
}

// A routine that loops forever at the top level must be rejected, not hang.
func TestLoadFromStringInterruptsARunawayRoutine(t *testing.T) {
	e := New(200 * time.Millisecond)
	runsWithin(t, 5*time.Second, func() {
		err := e.LoadFromString(`while (true) {} var rules = [];`)
		if err == nil {
			t.Error("a routine that never finishes loading was accepted")
			return
		}
		if !strings.Contains(err.Error(), "budget") && !strings.Contains(err.Error(), "aborted") {
			t.Errorf("error = %q, want it to name the execution budget", err)
		}
	})
}

// A rule whose check() loops forever is reported and skipped; the rules that
// come after it still run.
func TestEvaluateRulesInterruptsARunawayCheck(t *testing.T) {
	e := New(200 * time.Millisecond)
	if err := e.LoadFromString(`
		var rules = [
			{ name: "runaway", ruleId: 1,
			  check: function (ctx) { while (true) {} },
			  createAction: function (ctx) { return {title: "never"}; } },
			{ name: "well behaved", ruleId: 2,
			  check: function (ctx) { return true; },
			  createAction: function (ctx) { return {title: "ok", type: "Info", priority: 50}; } }
		];`); err != nil {
		t.Fatalf("load: %v", err)
	}

	runsWithin(t, 5*time.Second, func() {
		actions, err := e.EvaluateRules(RuleContext{}, false)
		if err != nil {
			t.Fatalf("EvaluateRules: %v", err)
		}
		if len(actions) != 1 || actions[0].Title != "ok" {
			t.Errorf("actions = %+v, want only the well-behaved rule's action", actions)
		}
	})
}

// The validator is where a newly generated routine runs for the first time, so
// it is the first place a runaway rule lands.
func TestValidationInterruptsARunawayRule(t *testing.T) {
	e := New(200 * time.Millisecond)
	if err := e.LoadFromString(`
		var rules = [{ name: "runaway", ruleId: 1,
			check: function (ctx) { while (true) {} },
			createAction: function (ctx) { return {title: "never"}; } }];`); err != nil {
		t.Fatalf("load: %v", err)
	}

	runsWithin(t, 15*time.Second, func() {
		report := e.ValidateWithMockCtxReport()
		if len(report.Errors) == 0 {
			t.Error("a rule that never terminates passed validation")
		}
	})
}

// The interrupt must not survive into the next execution on the same VM: a slow
// rule that overran its budget would otherwise poison the rule after it, which
// would read as an unrelated rule mysteriously failing.
func TestInterruptDoesNotLeakIntoTheNextExecution(t *testing.T) {
	e := New(200 * time.Millisecond)
	if err := e.LoadFromString(`
		var rules = [
			{ name: "runaway", ruleId: 1,
			  check: function (ctx) { while (true) {} },
			  createAction: function (ctx) { return {title: "never"}; } },
			{ name: "after", ruleId: 2,
			  check: function (ctx) { return true; },
			  createAction: function (ctx) { return {title: "after", type: "Info", priority: 10}; } }
		];`); err != nil {
		t.Fatalf("load: %v", err)
	}

	runsWithin(t, 10*time.Second, func() {
		for i := 0; i < 3; i++ {
			actions, err := e.EvaluateRules(RuleContext{}, false)
			if err != nil {
				t.Fatalf("pass %d: %v", i, err)
			}
			if len(actions) != 1 {
				t.Fatalf("pass %d produced %d action(s), want 1 — the interrupt leaked into the next run", i, len(actions))
			}
		}
	})
}

// A non-positive budget disables the guard. Only a test wants this, but it must
// not be mistaken for "interrupt immediately".
func TestZeroBudgetRunsUnguarded(t *testing.T) {
	e := New(0)
	runsWithin(t, 5*time.Second, func() {
		if err := e.LoadFromString(`var rules = [{ name: "fine", ruleId: 1,
			check: function (ctx) { return false; },
			createAction: function (ctx) { return {}; } }];`); err != nil {
			t.Errorf("a terminating routine was rejected with the guard off: %v", err)
		}
	})
}
