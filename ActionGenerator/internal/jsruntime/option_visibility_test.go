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

// A routine that offers two options, one for a user with a stale thing and
// one for a user without.
const optionRoutine = `var routineContract = 2;
function hasStale(api) {
  return api.things.list().things.filter(function (t) { return t.stale; }).length > 0;
}
var rules = [
  {
    name: "missing-hosts",
    ruleId: 8,
    check: function(api) { return true; },
    createAction: function(api) {
      return { title: "Identify missing host assets", type: "Fix", description: "d",
               status: "New", priority: 91, assignedTo: "User", issuedBy: "ActionGenerator" };
    },
    options: {
      "5": function(api) { return !hasStale(api); },
      "16": function(api) { return hasStale(api); }
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

// raised evaluates e for one user whose things.list answers things, and
// returns the one action it must raise.
func raised(t *testing.T, e *JSRuleExecutor, things string) *[]int64 {
	t.Helper()
	b, _ := thingsBridge(t, 200, `{"things":`+things+`}`)
	actions, unknown, err := e.EvaluateBridge(b.NewPass(), 1, false)
	if err != nil || unknown {
		t.Fatalf("evaluate: unknown=%v err=%v", unknown, err)
	}
	if len(actions) != 1 {
		t.Fatalf("got %d actions, want 1", len(actions))
	}
	return actions[0].HiddenOptions
}

// AG-EVAL-16: the condition decides which options come back as hidden.
func TestOptionConditionsDecideWhatIsHidden(t *testing.T) {
	e := executorWith(t, optionRoutine)

	t.Run("nothing stale", func(t *testing.T) {
		h := raised(t, e, `[]`)
		if h == nil {
			t.Fatal("no option set evaluated at all")
		}
		if hidden := *h; len(hidden) != 1 || hidden[0] != 16 {
			t.Errorf("hidden = %v, want [16]", hidden)
		}
	})

	t.Run("a stale thing", func(t *testing.T) {
		if hidden := *raised(t, e, `[{"id":1,"stale":true}]`); len(hidden) != 1 || hidden[0] != 5 {
			t.Errorf("hidden = %v, want [5]", hidden)
		}
	})
}

// AG-IF-14: a routine that declares no conditions hides nothing — every rule
// whose options carry no prompt.
func TestRoutineWithoutConditionsHidesNothing(t *testing.T) {
	e := executorWith(t, `var routineContract = 2;
var rules = [
  { name: "r", ruleId: 1,
    check: function(api) { return true; },
    createAction: function(api) {
      return { title: "T", type: "Fix", description: "d", status: "New", priority: 50,
               assignedTo: "User", issuedBy: "ActionGenerator" };
    } }
];`)

	if h := raised(t, e, `[]`); h != nil {
		t.Errorf("HiddenOptions = %v, want nil so that every option is shown", *h)
	}
}

// AG-EVAL-17: a condition that throws leaves its option visible. Hiding a step
// the user may need because a generated line was wrong is the worse failure.
func TestThrowingConditionLeavesTheOptionVisible(t *testing.T) {
	e := executorWith(t, `var routineContract = 2;
var rules = [
  { name: "r", ruleId: 1,
    check: function(api) { return true; },
    createAction: function(api) {
      return { title: "T", type: "Fix", description: "d", status: "New", priority: 50,
               assignedTo: "User", issuedBy: "ActionGenerator" };
    },
    options: {
      "7": function(api) { return api.things.nothing().here; },
      "8": function(api) { return false; }
    } }
];`)

	if hidden := *raised(t, e, `[]`); len(hidden) != 1 || hidden[0] != 8 {
		t.Errorf("hidden = %v, want [8] — the throwing condition must not hide its option", hidden)
	}
}

// AG-EVAL-18: the order is stable, so an unchanged answer compares equal and
// the action is not rewritten and re-broadcast on every evaluation.
func TestHiddenOptionsAreSorted(t *testing.T) {
	e := executorWith(t, `var routineContract = 2;
var rules = [
  { name: "r", ruleId: 1,
    check: function(api) { return true; },
    createAction: function(api) {
      return { title: "T", type: "Fix", description: "d", status: "New", priority: 50,
               assignedTo: "User", issuedBy: "ActionGenerator" };
    },
    options: {
      "30": function(api) { return false; },
      "4":  function(api) { return false; },
      "17": function(api) { return false; }
    } }
];`)

	for i := 0; i < 5; i++ {
		hidden := *raised(t, e, `[]`)
		if len(hidden) != 3 || hidden[0] != 4 || hidden[1] != 17 || hidden[2] != 30 {
			t.Fatalf("hidden = %v, want [4 17 30] every time", hidden)
		}
	}
}

// AG-IF-08: a malformed options member is a fault in the routine, not
// something to shrug off: the effect of ignoring it would be an option
// silently never shown.
func TestMalformedOptionsMemberIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, code string }{
		{"key is not an id", `options: { "upload": function(api) { return true; } }`},
		{"value is not a function", `options: { "5": true }`},
		{"not an object", `options: 42`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := New(5 * time.Second)
			err := e.LoadFromString(`var routineContract = 2;
var rules = [
  { name: "r", ruleId: 1,
    check: function(api) { return true; },
    createAction: function(api) { return {}; },
    ` + tc.code + ` }
];`)
			if err == nil {
				t.Error("loaded a routine whose options member is malformed")
			}
		})
	}
}

// AG-EVAL-15: a rule with no trigger of its own validates.
//
// Its check() is a plain `return false` — the system prompt says to write it
// that way — which the always-false judgement otherwise refuses as a condition
// that could never fire. For an ordinary rule that refusal is right; for this
// one it means the rule cannot be regenerated at all.
func TestOnDemandRuleValidates(t *testing.T) {
	const routine = `var routineContract = 2;
var rules = [
  { name: "reduce-host-assets", ruleId: 7,
    check: function(api) { return false; },
    createAction: function(api) {
      return { title: "Reduce host assets", type: "Fix", description: "d", status: "New",
               priority: 30, assignedTo: "User", issuedBy: "ActionGenerator" };
    } }
];`
	b, _ := thingsBridge(t, 200, `{}`)

	t.Run("refused for an ordinary rule", func(t *testing.T) {
		report := executorWith(t, routine).ValidateBridge(b)
		if len(report.Errors) == 0 {
			t.Error("a check() that can never fire was accepted for a rule that has a trigger")
		}
	})

	t.Run("accepted for an on-demand rule", func(t *testing.T) {
		e := executorWith(t, routine)
		e.SetOnDemand(true)
		report := e.ValidateBridge(b)
		if len(report.Errors) > 0 {
			t.Errorf("an on-demand rule's routine was refused: %v", report.Errors)
		}
		if len(report.Notes) == 0 {
			t.Error("no note explaining why the rule never fires in the scenarios")
		}
	})
}
