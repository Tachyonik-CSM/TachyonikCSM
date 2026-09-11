// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for the two properties that contain an import routine rather than
// trusting it.
//
// Routines are AI-generated and run over files uploaded by users, so neither
// the code nor its input is something this daemon wrote. Two things follow, and
// both are invisible in normal operation — which is why they are pinned here
// rather than left to review:
//
//   - A routine must not be able to run forever. goja has no preemption, and
//     this daemon polls on a single goroutine, so one non-terminating routine
//     stops every import and re-import silently until the process is restarted.
//
//   - A routine must not carry state from one file to the next. The executor
//     used to hold a live VM per rule and reuse it for every file of that type
//     across every user, so a global set while parsing one tenant's upload was
//     still there while parsing another's.

package jsruntime

import (
	"strings"
	"testing"
	"time"
)

// The interrupt either fires or it does not; whether the leash is 30s or 200ms
// is decided in the first instruction, so the tests use a short one and keep
// the suite quick. budgetSlack is scheduling noise, not part of what is tested.
const (
	testBudget  = 200 * time.Millisecond
	budgetSlack = 10 * time.Second
)

// newWithBudget is New with a shorter leash. It lives here rather than beside
// New because nothing in the daemon may construct an executor with a budget
// other than the real one.
func newWithBudget(d time.Duration) *JSImportExecutor {
	return &JSImportExecutor{budget: d}
}

// A routine that never returns must be stopped, not waited for.
func TestExecuteInterruptsRunawayRoutine(t *testing.T) {
	e := newWithBudget(testBudget)
	if err := e.LoadFromString(`var importFunction = function (ctx) { while (true) {} };`); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}

	start := time.Now()
	_, err := e.Execute(ImportContext{FileContent: "x", FileName: "f", SourceType: "t"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a non-terminating routine returned without error")
	}
	if elapsed > testBudget+budgetSlack {
		t.Errorf("budget did not fire: took %s", elapsed)
	}
}

// The budget must also cover top-level code: a routine whose module body loops
// never reaches its importFunction, and loading happens while the AI importer
// holds its lock.
func TestLoadInterruptsRunawayTopLevel(t *testing.T) {
	e := newWithBudget(testBudget)
	start := time.Now()
	err := e.LoadFromString(`while (true) {} var importFunction = function () { return {}; };`)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a routine looping at load time was accepted")
	}
	if elapsed > testBudget+budgetSlack {
		t.Errorf("budget did not fire during load: took %s", elapsed)
	}
}

// An interrupt must not leak into the next run. The timer is stopped and the
// flag cleared per VM; a fresh VM per execution makes that doubly true, and
// this catches a regression in either mechanism.
func TestBudgetDoesNotPoisonTheNextExecution(t *testing.T) {
	e := newWithBudget(testBudget)
	code := `
		var importFunction = function (ctx) {
			if (ctx.fileName === "slow") { while (true) {} }
			return { assets: [{ name: "ok", type: "Host" }] };
		};
	`
	if err := e.LoadFromString(code); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}

	if _, err := e.Execute(ImportContext{FileContent: "x", FileName: "slow", SourceType: "t"}); err == nil {
		t.Fatal("the runaway run was not interrupted")
	}
	res, err := e.Execute(ImportContext{FileContent: "x", FileName: "fine", SourceType: "t"})
	if err != nil {
		t.Fatalf("the run after an interrupt failed: %v", err)
	}
	if len(res.Assets) != 1 || res.Assets[0].Name != "ok" {
		t.Errorf("unexpected result after an interrupt: %+v", res.Assets)
	}
}

// Nothing a routine leaves behind may reach the next file. The counter here is
// the shape of the bug: a shared VM returned seen-1, seen-2, seen-3.
func TestNoStateCarriesBetweenExecutions(t *testing.T) {
	e := New()
	code := `
		var seen = 0;
		var importFunction = function (ctx) {
			seen = seen + 1;
			return { assets: [{ name: "seen-" + seen, type: "Host" }] };
		};
	`
	if err := e.LoadFromString(code); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	for i := 0; i < 3; i++ {
		res, err := e.Execute(ImportContext{FileContent: "x", FileName: "f", SourceType: "t"})
		if err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
		if res.Assets[0].Name != "seen-1" {
			t.Fatalf("run %d saw state from an earlier file: %q", i+1, res.Assets[0].Name)
		}
	}
}

// A routine cannot stash data on a global object either — the whole VM goes,
// not just its variable bindings.
func TestNoGlobalObjectCarriesBetweenExecutions(t *testing.T) {
	e := New()
	code := `
		var importFunction = function (ctx) {
			var leaked = (typeof globalThis.stash === "undefined") ? "clean" : globalThis.stash;
			globalThis.stash = ctx.fileContent;
			return { assets: [{ name: leaked, type: "Host" }] };
		};
	`
	if err := e.LoadFromString(code); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	if _, err := e.Execute(ImportContext{FileContent: "tenant-a-secret", FileName: "a", SourceType: "t"}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	res, err := e.Execute(ImportContext{FileContent: "tenant-b-file", FileName: "b", SourceType: "t"})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res.Assets[0].Name != "clean" {
		t.Errorf("one file's content reached the next run: %q", res.Assets[0].Name)
	}
}

// The sandbox grants no I/O. eval and Function are inherent to any JS engine
// and confer none; require, process, fetch and timers would.
func TestSandboxExposesNoIO(t *testing.T) {
	e := New()
	code := `
		var importFunction = function (ctx) {
			var found = [];
			["require","process","fetch","XMLHttpRequest","setTimeout","setInterval","Buffer"]
				.forEach(function (n) { if (typeof this[n] !== "undefined") { found.push(n); } }, this);
			return { assets: [{ name: found.join(",") || "none", type: "Host" }] };
		};
	`
	if err := e.LoadFromString(code); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	res, err := e.Execute(ImportContext{FileContent: "x", FileName: "f", SourceType: "t"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Assets[0].Name != "none" {
		t.Errorf("the sandbox exposes I/O globals: %s", res.Assets[0].Name)
	}
}

// A getter that never returns is still routine code, and result extraction
// reads properties the routine defines — so extraction is inside the budget
// too, not only the call.
func TestBudgetCoversResultExtraction(t *testing.T) {
	e := newWithBudget(testBudget)
	code := `
		var importFunction = function (ctx) {
			return { get assets() { while (true) {} } };
		};
	`
	if err := e.LoadFromString(code); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	start := time.Now()
	_, err := e.Execute(ImportContext{FileContent: "x", FileName: "f", SourceType: "t"})
	if err == nil {
		t.Fatal("a looping getter in the result was not interrupted")
	}
	if elapsed := time.Since(start); elapsed > testBudget+budgetSlack {
		t.Errorf("budget did not cover extraction: took %s", elapsed)
	}
	if !strings.Contains(err.Error(), "aborted") && !strings.Contains(err.Error(), "budget") {
		t.Logf("interrupted with: %v", err)
	}
}
