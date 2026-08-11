// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for rule loading and execution, and above all for the execution budget
// that contains them: that an infinite loop is interrupted whether it sits in
// top-level code, in analyze(), or in a toString/getter reached during rule
// extraction; that an interrupt does not leak into the next rule's run; and
// that an ordinary routine is left alone.

package jsruntime

import (
	"testing"
	"time"
)

// A routine whose analyze() never returns must be interrupted by the execution
// guard rather than hanging the caller forever.
func TestAnalyzeSourceInterruptsInfiniteLoop(t *testing.T) {
	const code = `var rules = [{ name: "loop", ruleId: 1, analyze: function (ctx) { while (true) {} } }];`

	e := New(100 * time.Millisecond)
	if err := e.LoadFromString(code); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}

	done := make(chan struct{})
	go func() {
		// Result is irrelevant; the point is that the call returns at all.
		_, _ = e.AnalyzeSource(RuleContext{Filename: "x.bin", Content: "data"})
		close(done)
	}()

	select {
	case <-done:
		// Interrupted and returned — guard works.
	case <-time.After(5 * time.Second):
		t.Fatal("AnalyzeSource did not return: execution guard failed to interrupt an infinite loop")
	}
}

// An infinite loop in top-level routine code must be interrupted at load time.
func TestLoadFromStringInterruptsInfiniteLoop(t *testing.T) {
	const code = `while (true) {}`

	e := New(100 * time.Millisecond)
	done := make(chan error, 1)
	go func() { done <- e.LoadFromString(code) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error from interrupted load, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("LoadFromString did not return: execution guard failed to interrupt an infinite loop")
	}
}

// A PDF-aware rule (the user's example) must match on mimeType + extracted
// text, and must NOT match a non-PDF that merely contains the same strings.
func TestAnalyzeSource_PDFRule(t *testing.T) {
	const code = `var rules = [{
		name: "OpenVAS PDF Report",
		ruleId: 42,
		analyze: function (ctx) {
			if (ctx.mimeType === "application/pdf"
				&& ctx.content.indexOf("OpenVAS Vulnerability Report") >= 0
				&& ctx.content.indexOf("This document reports on the results of an automatic security scan.") >= 0) {
				return { sourceType: "OpenVAS PDF Report", status: "Analysed" };
			}
			return null;
		}
	}];`

	e := New(2 * time.Second)
	if err := e.LoadFromString(code); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}

	pdfText := "OpenVAS Vulnerability Report\nThis document reports on the results of an automatic security scan.\n..."

	// PDF with both strings → match.
	res, err := e.AnalyzeSource(RuleContext{Filename: "r.pdf", Content: pdfText, MimeType: "application/pdf"})
	if err != nil {
		t.Fatalf("AnalyzeSource: %v", err)
	}
	if res == nil || res.SourceType != "OpenVAS PDF Report" {
		t.Fatalf("expected OpenVAS PDF Report match, got %+v", res)
	}

	// Same text but not a PDF → no match (mimeType guard).
	res, err = e.AnalyzeSource(RuleContext{Filename: "r.txt", Content: pdfText, MimeType: "text/plain; charset=utf-8"})
	if err != nil {
		t.Fatalf("AnalyzeSource (non-pdf): %v", err)
	}
	if res != nil {
		t.Fatalf("expected no match for non-PDF, got %+v", res)
	}
}

// A well-behaved routine must still run to completion under the guard (no false
// interruption) and produce its match.
func TestAnalyzeSourceNormalRoutineNotInterrupted(t *testing.T) {
	const code = `var rules = [{
		name: "always",
		ruleId: 7,
		analyze: function (ctx) { return { sourceType: "Test", status: "Analysed" }; }
	}];`

	e := New(2 * time.Second)
	if err := e.LoadFromString(code); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}

	res, err := e.AnalyzeSource(RuleContext{Filename: "x.bin", Content: "data"})
	if err != nil {
		t.Fatalf("AnalyzeSource: %v", err)
	}
	if res == nil || res.SourceType != "Test" || res.Status != "Analysed" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// The execution budget must cover rule extraction, not just top-level
// evaluation. Export, property reads, String() and ToInteger() all dispatch to
// routine-defined hooks; when extraction ran outside the guard, a looping
// toString() hung LoadFromString forever — and it runs while AIAnalyser holds
// its lock, so the daemon stopped analysing until restarted.
func TestLoadFromString_BudgetCoversExtraction(t *testing.T) {
	e := New(300 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		done <- e.LoadFromString(`
			var rules = [{
				name: { toString: function () { while (true) {} } },
				ruleId: 1,
				analyze: function (ctx) { return null; }
			}];
		`)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the budget to abort a looping toString(), got success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("LoadFromString exceeded its budget by >16x: extraction is unguarded again")
	}
}

// A getter on the rule object is the same escape hatch by another name.
func TestLoadFromString_BudgetCoversPropertyGetter(t *testing.T) {
	e := New(300 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		done <- e.LoadFromString(`
			var rules = [{}];
			Object.defineProperty(rules[0], "name", {
				get: function () { while (true) {} }
			});
		`)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the budget to abort a looping property getter, got success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a looping getter escaped the execution budget")
	}
}

// An interrupt raised for one rule must not carry over. Before the ordering
// fix, a timer firing as its rule finished could set the flag after
// ClearInterrupt, aborting the next (innocent) rule with "budget exceeded" —
// and that source would be misclassified as Unsupported.
func TestAnalyzeSource_InterruptDoesNotLeakToNextRule(t *testing.T) {
	e := New(50 * time.Millisecond)

	// A slow rule that overruns, followed by a rule that should still match.
	err := e.LoadFromString(`
		var rules = [
			{ name: "slow", ruleId: 1, analyze: function (ctx) { while (true) {} } },
			{ name: "fast", ruleId: 2, analyze: function (ctx) {
				return { sourceType: "OpenVAS", status: "Analysed" };
			} }
		];
	`)
	if err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}

	size := int64(10)
	got, err := e.AnalyzeSource(RuleContext{Filename: "x.xml", Content: "hello", FileSize: &size})
	if err != nil {
		t.Fatalf("AnalyzeSource: %v", err)
	}
	if got == nil {
		t.Fatal("the rule after the timed-out one did not run: the interrupt leaked")
	}
	if got.RuleName != "fast" || got.SourceType != "OpenVAS" {
		t.Fatalf("got %+v, want the 'fast' rule matching OpenVAS", got)
	}
}
