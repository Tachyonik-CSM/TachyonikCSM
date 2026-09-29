// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the per-option conditions: a rule may decide, per action option, whether
// that option applies to the user it just raised an action for.
//
// The failure that matters is silent in both directions. An option wrongly
// hidden is a step the user never sees; an option wrongly shown is advice that
// does not apply. Neither produces an error anywhere, so the behaviour is
// pinned here rather than left to the routine's own correctness.

package jsruntime

import (
	"testing"
	"time"
)

// A routine that offers two options, one of them conditional.
const optionRoutine = `
var rules = [
  {
    name: "missing-hosts",
    ruleId: 8,
    check: function(ctx) { return true; },
    createAction: function(ctx) {
      return { title: "Identify missing host assets", type: "Fix", description: "d",
               status: "New", priority: 91, assignedTo: "User", issuedBy: "ActionGenerator" };
    },
    options: {
      "5": function(ctx) { return ctx.capabilities.automated.indexOf("Host detection") === -1; },
      "16": function(ctx) { return ctx.capabilities.automated.indexOf("Host detection") !== -1; }
    }
  }
];
`

func executorWith(t *testing.T, code string) *JSRuleExecutor {
	t.Helper()
	e := New(5 * time.Second)
	if err := e.LoadFromString(code); err != nil {
		t.Fatalf("load: %v", err)
	}
	return e
}

// ctxWithCapabilities is the smallest context these conditions read.
func ctxWithCapabilities(automated ...string) RuleContext {
	ctx := RuleContext{
		User:         map[string]interface{}{"id": int64(1)},
		Organisation: EmptyOrganisation(),
		Settings:     EmptySettings(),
		Capabilities: CapabilitySet{Automated: automated, Manual: []string{}},
	}
	if ctx.Capabilities.Automated == nil {
		ctx.Capabilities.Automated = []string{}
	}
	return ctx
}

// The condition decides which options come back as hidden.
func TestOptionConditionsDecideWhatIsHidden(t *testing.T) {
	e := executorWith(t, optionRoutine)

	t.Run("no automated tool", func(t *testing.T) {
		actions, err := e.EvaluateRules(ctxWithCapabilities(), false)
		if err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if len(actions) != 1 {
			t.Fatalf("got %d actions, want 1", len(actions))
		}
		if actions[0].HiddenOptions == nil {
			t.Fatal("no option set evaluated at all")
		}
		hidden := *actions[0].HiddenOptions
		// Option 5 ("install a tool") applies; option 16 ("use your automated
		// tool") does not.
		if len(hidden) != 1 || hidden[0] != 16 {
			t.Errorf("hidden = %v, want [16]", hidden)
		}
	})

	t.Run("automated tool present", func(t *testing.T) {
		actions, err := e.EvaluateRules(ctxWithCapabilities("Host detection"), false)
		if err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		hidden := *actions[0].HiddenOptions
		if len(hidden) != 1 || hidden[0] != 5 {
			t.Errorf("hidden = %v, want [5]", hidden)
		}
	})
}

// A routine that declares no conditions hides nothing — which is every routine
// generated before this feature, and every rule whose options carry no prompt.
func TestRoutineWithoutConditionsHidesNothing(t *testing.T) {
	e := executorWith(t, `
var rules = [
  { name: "r", ruleId: 1,
    check: function(ctx) { return true; },
    createAction: function(ctx) {
      return { title: "T", type: "Fix", description: "d", status: "New", priority: 50,
               assignedTo: "User", issuedBy: "ActionGenerator" };
    } }
];`)

	actions, err := e.EvaluateRules(ctxWithCapabilities(), false)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if actions[0].HiddenOptions != nil {
		t.Errorf("HiddenOptions = %v, want nil so that every option is shown", *actions[0].HiddenOptions)
	}
}

// A condition that throws leaves its option visible. Hiding a step the user may
// need because a generated line was wrong is the worse failure.
func TestThrowingConditionLeavesTheOptionVisible(t *testing.T) {
	e := executorWith(t, `
var rules = [
  { name: "r", ruleId: 1,
    check: function(ctx) { return true; },
    createAction: function(ctx) {
      return { title: "T", type: "Fix", description: "d", status: "New", priority: 50,
               assignedTo: "User", issuedBy: "ActionGenerator" };
    },
    options: {
      "7": function(ctx) { return ctx.nothing.here; },
      "8": function(ctx) { return false; }
    } }
];`)

	actions, err := e.EvaluateRules(ctxWithCapabilities(), false)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	hidden := *actions[0].HiddenOptions
	if len(hidden) != 1 || hidden[0] != 8 {
		t.Errorf("hidden = %v, want [8] — the throwing condition must not hide its option", hidden)
	}
}

// The order is stable, so an unchanged answer compares equal and the action is
// not rewritten and re-broadcast on every evaluation.
func TestHiddenOptionsAreSorted(t *testing.T) {
	e := executorWith(t, `
var rules = [
  { name: "r", ruleId: 1,
    check: function(ctx) { return true; },
    createAction: function(ctx) {
      return { title: "T", type: "Fix", description: "d", status: "New", priority: 50,
               assignedTo: "User", issuedBy: "ActionGenerator" };
    },
    options: {
      "30": function(ctx) { return false; },
      "4":  function(ctx) { return false; },
      "17": function(ctx) { return false; }
    } }
];`)

	for i := 0; i < 5; i++ {
		actions, err := e.EvaluateRules(ctxWithCapabilities(), false)
		if err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		hidden := *actions[0].HiddenOptions
		if len(hidden) != 3 || hidden[0] != 4 || hidden[1] != 17 || hidden[2] != 30 {
			t.Fatalf("hidden = %v, want [4 17 30] every time", hidden)
		}
	}
}

// A malformed options member is a fault in the routine, not something to shrug
// off: the effect of ignoring it would be an option silently never shown.
func TestMalformedOptionsMemberIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, code string }{
		{"key is not an id", `options: { "upload": function(ctx) { return true; } }`},
		{"value is not a function", `options: { "5": true }`},
		{"not an object", `options: 42`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := New(5 * time.Second)
			err := e.LoadFromString(`
var rules = [
  { name: "r", ruleId: 1,
    check: function(ctx) { return true; },
    createAction: function(ctx) { return {}; },
    ` + tc.code + ` }
];`)
			if err == nil {
				t.Error("loaded a routine whose options member is malformed")
			}
		})
	}
}

// A rule with no trigger of its own validates.
//
// Its check() is a plain `return false` — the system prompt says to write it
// that way — which the always-false judgement otherwise refuses as a condition
// that could never fire. For an ordinary rule that refusal is right; for this
// one it means the rule cannot be regenerated at all, which is how rule 7 came
// to be running a routine that predates the check.
func TestOnDemandRuleValidates(t *testing.T) {
	const routine = `
var rules = [
  { name: "reduce-host-assets", ruleId: 7,
    check: function(ctx) { return false; },
    createAction: function(ctx) {
      return { title: "Reduce host assets", type: "Fix", description: "d", status: "New",
               priority: 30, assignedTo: "User", issuedBy: "ActionGenerator" };
    } }
];`

	t.Run("refused for an ordinary rule", func(t *testing.T) {
		e := executorWith(t, routine)
		report := e.ValidateWithMockCtxReport()
		if len(report.Errors) == 0 {
			t.Error("a check() that can never fire was accepted for a rule that has a trigger")
		}
	})

	t.Run("accepted for an on-demand rule", func(t *testing.T) {
		e := executorWith(t, routine)
		e.SetOnDemand(true)
		report := e.ValidateWithMockCtxReport()
		if len(report.Errors) > 0 {
			t.Errorf("an on-demand rule's routine was refused: %v", report.Errors)
		}
		if len(report.Notes) == 0 {
			t.Error("no note explaining why the rule never fires in the scenarios")
		}
	})
}
