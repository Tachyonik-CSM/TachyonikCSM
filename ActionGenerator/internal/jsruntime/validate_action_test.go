// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests what a routine is allowed to turn into an action.
//
// The code that fills these fields is written by a model, and nothing
// downstream constrains it: ActionManager checks that title, type and status
// are non-empty and nothing about what they contain. So this is the only place
// that decides an action is well formed before it reaches a database and every
// screen that lists actions.

package jsruntime

import (
	"strings"
	"testing"
	"time"

	"tachyonik/actiongenerator/internal/actionmanager"
)

// valid is a well-formed action; each test spoils one field of it.
func valid() actionmanager.CreateActionRequest {
	return actionmanager.CreateActionRequest{
		Title:       "Update nmap",
		Type:        "Info",
		Description: "A newer version is available.",
		Status:      "New",
		Priority:    50,
		AssignedTo:  "User",
		IssuedBy:    "ActionGenerator",
		Trigger:     "nmap is out of date",
	}
}

func TestValidActionIsAccepted(t *testing.T) {
	a := valid()
	if err := validateAction(&a, "rule"); err != nil {
		t.Fatalf("a well-formed action was rejected: %v", err)
	}
	if a != valid() {
		t.Errorf("a valid action was altered: %+v", a)
	}
}

// Every vocabulary field, and the two ways each can be wrong: a plausible value
// that is not in the set, and something arbitrary.
func TestFieldsOutsideTheirVocabularyAreRejected(t *testing.T) {
	cases := []struct {
		name   string
		spoil  func(*actionmanager.CreateActionRequest)
		expect string
	}{
		{"type not in the set", func(a *actionmanager.CreateActionRequest) { a.Type = "task" }, "type"},
		{"type arbitrary", func(a *actionmanager.CreateActionRequest) { a.Type = "<script>" }, "type"},
		{"status not in the set", func(a *actionmanager.CreateActionRequest) { a.Status = "Pending" }, "status"},
		{"assignedTo not in the set", func(a *actionmanager.CreateActionRequest) { a.AssignedTo = "root" }, "assignedTo"},
		{"issuedBy not in the set", func(a *actionmanager.CreateActionRequest) { a.IssuedBy = "Administrator" }, "issuedBy"},
		{"empty title", func(a *actionmanager.CreateActionRequest) { a.Title = "" }, "title"},
		{"whitespace title", func(a *actionmanager.CreateActionRequest) { a.Title = "   " }, "title"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := valid()
			c.spoil(&a)
			err := validateAction(&a, "rule")
			if err == nil {
				t.Fatalf("%s was accepted", c.name)
			}
			if !strings.Contains(err.Error(), c.expect) {
				t.Errorf("error %q does not name the offending field %q", err, c.expect)
			}
		})
	}
}

// The documented vocabularies, accepted in full — a rule may legitimately
// raise a Fix assigned to Support.
func TestEveryDocumentedValueIsAccepted(t *testing.T) {
	for _, typ := range []string{"Fix", "Info", "Support"} {
		a := valid()
		a.Type = typ
		if err := validateAction(&a, "rule"); err != nil {
			t.Errorf("type %q was rejected: %v", typ, err)
		}
	}
	for _, assignee := range []string{"User", "Support", "ActionExecutor"} {
		a := valid()
		a.AssignedTo = assignee
		if err := validateAction(&a, "rule"); err != nil {
			t.Errorf("assignedTo %q was rejected: %v", assignee, err)
		}
	}
	for _, issuer := range []string{"User", "Support", "ActionGenerator"} {
		a := valid()
		a.IssuedBy = issuer
		if err := validateAction(&a, "rule"); err != nil {
			t.Errorf("issuedBy %q was rejected: %v", issuer, err)
		}
	}
	for _, status := range []string{"New", "Done"} {
		a := valid()
		a.Status = status
		if err := validateAction(&a, "rule"); err != nil {
			t.Errorf("status %q was rejected: %v", status, err)
		}
	}
}

func TestPriorityRange(t *testing.T) {
	for _, p := range []int{MinActionPriority, 50, MaxActionPriority} {
		a := valid()
		a.Priority = p
		if err := validateAction(&a, "rule"); err != nil {
			t.Errorf("priority %d was rejected: %v", p, err)
		}
	}
	for _, p := range []int{-1, 101, 1 << 30} {
		a := valid()
		a.Priority = p
		if err := validateAction(&a, "rule"); err == nil {
			t.Errorf("priority %d was accepted", p)
		}
	}
}

// Long text is truncated rather than rejected: the length comes from the data
// the rule interpolated, and losing a needed action because an asset had a long
// name would be the worse failure.
func TestOverlongTextIsTruncatedNotRejected(t *testing.T) {
	a := valid()
	a.Title = strings.Repeat("t", MaxActionTitleLen*3)
	a.Description = strings.Repeat("d", MaxActionDescriptionLen*3)
	a.Trigger = strings.Repeat("g", MaxActionTriggerLen*3)

	if err := validateAction(&a, "rule"); err != nil {
		t.Fatalf("an action with long text was rejected: %v", err)
	}
	if len([]rune(a.Title)) != MaxActionTitleLen {
		t.Errorf("title is %d runes, want %d", len([]rune(a.Title)), MaxActionTitleLen)
	}
	if len([]rune(a.Description)) != MaxActionDescriptionLen {
		t.Errorf("description is %d runes, want %d", len([]rune(a.Description)), MaxActionDescriptionLen)
	}
	if len([]rune(a.Trigger)) != MaxActionTriggerLen {
		t.Errorf("trigger is %d runes, want %d", len([]rune(a.Trigger)), MaxActionTriggerLen)
	}
}

// Truncation counts runes, not bytes: cutting UTF-8 at a byte offset would
// split a character and put an invalid sequence into the database.
func TestTruncationDoesNotSplitCharacters(t *testing.T) {
	a := valid()
	a.Title = strings.Repeat("ü", MaxActionTitleLen+50)

	if err := validateAction(&a, "rule"); err != nil {
		t.Fatalf("rejected: %v", err)
	}
	if len([]rune(a.Title)) != MaxActionTitleLen {
		t.Errorf("title is %d runes, want %d", len([]rune(a.Title)), MaxActionTitleLen)
	}
	if !strings.HasSuffix(a.Title, "ü") {
		t.Errorf("truncation split a multi-byte character: %q", a.Title[len(a.Title)-4:])
	}
}

// An invalid action is skipped; the rules around it still produce theirs.
func TestEvaluateRulesSkipsAnInvalidAction(t *testing.T) {
	e := New(5 * time.Second)
	if err := e.LoadFromString(`
		var rules = [
			{ name: "bad-type", ruleId: 1,
			  check: function (ctx) { return true; },
			  createAction: function (ctx) { return {title: "x", type: "task", status: "New", priority: 10}; } },
			{ name: "good", ruleId: 2,
			  check: function (ctx) { return true; },
			  createAction: function (ctx) { return {title: "Fine", type: "Info", status: "New", priority: 20}; } }
		];`); err != nil {
		t.Fatalf("load: %v", err)
	}

	actions, err := e.EvaluateRules(RuleContext{}, false)
	if err != nil {
		t.Fatalf("EvaluateRules: %v", err)
	}
	if len(actions) != 1 || actions[0].Title != "Fine" {
		t.Errorf("actions = %+v, want only the valid one", actions)
	}
}

// Unbounded recursion is an error, not an out-of-memory kill.
//
// goja's own default call-stack limit is math.MaxInt32 — no practical bound —
// so without SetMaxCallStackSize a recursing rule grows the stack until the
// process runs out of memory. The time budget here is deliberately long: it
// would otherwise be what stops the rule, and this test would pass whether or
// not the stack bound existed. A rule that is rejected in well under a second
// was rejected by the stack bound.
func TestRunawayRecursionIsRejected(t *testing.T) {
	e := New(60 * time.Second)
	err := e.LoadFromString(`
		function boom(n) { return boom(n + 1); }
		var rules = [{ name: "deep", ruleId: 1,
			check: function (ctx) { return boom(0); },
			createAction: function (ctx) { return {}; } }];`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	started := time.Now()
	actions, err := e.EvaluateRules(RuleContext{}, false)
	elapsed := time.Since(started)

	if err != nil {
		t.Fatalf("EvaluateRules: %v", err)
	}
	if len(actions) != 0 {
		t.Errorf("a rule that recurses forever produced %d action(s)", len(actions))
	}
	if elapsed > 5*time.Second {
		t.Errorf("took %s to reject a recursing rule — the call-stack bound is not what stopped it", elapsed)
	}
}
