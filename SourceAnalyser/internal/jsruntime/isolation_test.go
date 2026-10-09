// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that analysing one file cannot see what analysing another left behind,
// and that runaway recursion in a rule is an error, not a crashed daemon.
//
// A routine used to live in one VM shared by every file it analysed, so a
// global a rule set while reading one user's file was still there for the next
// file — another user's included. Each analysis now has a VM of its own.

package jsruntime

import (
	"strings"
	"testing"
	"time"
)

func TestAnAnalysisSeesNothingOfAnotherFile(t *testing.T) {
	e := New(5 * time.Second)
	code := `var lastFile = "none";
	var rules = [{ name: "r", ruleId: 1, analyze: function (ctx) {
		var seen = lastFile;
		lastFile = ctx.filename;
		return { sourceType: "previous " + seen, status: "Analysed" };
	} }];`
	if err := e.LoadFromString(code); err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, file := range []string{"alice-secret-network.xml", "bob.xml"} {
		res, err := e.AnalyzeSource(RuleContext{Filename: file})
		if err != nil || res == nil {
			t.Fatalf("%s: %+v, %v", file, res, err)
		}
		if strings.Contains(res.SourceType, "alice") {
			t.Errorf("analysing %s gave %q — the previous file leaked into it", file, res.SourceType)
		}
	}
}

func TestRecursionInARuleDoesNotCrash(t *testing.T) {
	e := New(5 * time.Second)
	code := `function down(n) { return down(n + 1); }
	var rules = [{ name: "r", ruleId: 1, analyze: function (ctx) { return down(0); } }];`
	if err := e.LoadFromString(code); err != nil {
		t.Fatalf("load: %v", err)
	}
	// Getting here at all is the point: the overflow became a JavaScript
	// exception instead of a Go stack overflow, which would have killed the
	// process. A failing rule is no match, as for any other error in a rule.
	res, err := e.AnalyzeSource(RuleContext{Filename: "x"})
	if err != nil || res != nil {
		t.Errorf("got %+v, %v; want no match", res, err)
	}
}
