// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins what validating a generated routine checks besides its not throwing
// (SA-GEN-10, SA-GEN-11; decided 2026-11-26, GEN question 2): its rules
// declare the analysis rule's id and no other, and none of them recognises
// every mock context.

package jsruntime

import (
	"strings"
	"testing"
	"time"
)

func validate(t *testing.T, code string, ruleID int64) []error {
	t.Helper()
	e := New(5 * time.Second)
	if err := e.LoadFromString(code); err != nil {
		t.Fatalf("load: %v", err)
	}
	return e.ValidateWithMockCtx(ruleID)
}

func joined(errs []error) string {
	var b strings.Builder
	for _, e := range errs {
		b.WriteString(e.Error())
		b.WriteString("\n")
	}
	return b.String()
}

func TestAProperRoutinePasses(t *testing.T) {
	code := `var rules = [{ name: "nessus", ruleId: 7, analyze: function (ctx) {
		return ctx.content.indexOf("NessusClientData_v2") >= 0 ? { sourceType: "Nessus XML" } : null;
	} }];`
	if errs := validate(t, code, 7); len(errs) > 0 {
		t.Errorf("errors = %s", joined(errs))
	}
}

func TestARoutineMustDeclareTheRulesIDAndNoOther(t *testing.T) {
	code := `var rules = [
		{ name: "right", ruleId: 7, analyze: function () { return null; } },
		{ name: "other", ruleId: 8, analyze: function () { return null; } }
	];`
	got := joined(validate(t, code, 7))
	if !strings.Contains(got, "'other' declares ruleId 8") {
		t.Errorf("errors = %s, want the rule declaring another id named", got)
	}
	if strings.Contains(got, "'right'") {
		t.Errorf("errors = %s, the rule with the right id is named", got)
	}
}

func TestARuleRecognisingEveryFileFails(t *testing.T) {
	code := `var rules = [{ name: "everything", ruleId: 7, analyze: function () {
		return { sourceType: "Anything" };
	} }];`
	got := joined(validate(t, code, 7))
	if !strings.Contains(got, "'everything' recognises every mock context") {
		t.Errorf("errors = %s, want the rule matching everything refused", got)
	}
}

// An object without a type is no match, as in the analysis, so a rule that
// returns one for everything is not refused for matching everything.
func TestAResultWithoutATypeIsNoMatch(t *testing.T) {
	code := `var rules = [{ name: "typeless", ruleId: 7, analyze: function () {
		return { sourceType: "" };
	} }];`
	if errs := validate(t, code, 7); len(errs) > 0 {
		t.Errorf("errors = %s", joined(errs))
	}
}
