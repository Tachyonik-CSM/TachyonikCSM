// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for the no-AI path: with no chat client configured, an
// already-generated routine still loads and evaluates, while asking to generate
// a new one is refused with a clear error rather than panicking on a nil client.

package aigenerator

import (
	"testing"
	"time"

	"tachyonik/actiongenerator/internal/aimanager"
	"tachyonik/actiongenerator/internal/config"
	"tachyonik/actiongenerator/internal/jsruntime"
)

// sampleActionRoutine is a minimal, hand-written action routine. It mirrors the
// shape produced by AI code generation (a `rules` array whose entries expose
// `check(ctx)` and `createAction(ctx)` functions) but needs no AI to exist —
// evaluating a routine is pure JS.
//
// The field values are the real vocabulary — type "Info", not the "task" this
// fixture used to carry. Nothing checked it before, so the fixture described an
// action the platform has no way to render; the output validation in jsruntime
// is what noticed.
const sampleActionRoutine = `
var rules = [
  {
    name: "always-fires",
    ruleId: 1,
    check: function (ctx) { return true; },
    createAction: function (ctx) {
      return { title: "Investigate", type: "Info", description: "auto", priority: 50 };
    }
  }
];
`

// TestEvaluateRules_NoAIClient_ExecutesLoadedRoutine verifies that an
// AIActionGenerator constructed without a chat client (i.e. AI unset/disabled)
// still exposes its loaded executor and that the executor evaluates rules. This
// guards the documented contract that rule evaluation works with AI off; only
// code generation requires a chat client.
func TestEvaluateRules_NoAIClient_ExecutesLoadedRoutine(t *testing.T) {
	ag := New(nil, nil, &config.Config{}) // nil chatClient == no AI configured

	exec := jsruntime.New(5 * time.Second)
	if err := exec.LoadFromString(sampleActionRoutine); err != nil {
		t.Fatalf("failed to load sample routine: %v", err)
	}
	ag.executors[1] = exec

	execs := ag.Executors()
	if len(execs) != 1 || execs[1] == nil {
		t.Fatalf("expected 1 executor available with AI unset, got %d", len(execs))
	}

	actions, err := execs[1].EvaluateRules(jsruntime.RuleContext{}, false)
	if err != nil {
		t.Fatalf("EvaluateRules returned error: %v", err)
	}
	if len(actions) != 1 || actions[0].Title != "Investigate" {
		t.Fatalf("unexpected actions: %+v", actions)
	}
}

// TestGenerateForRule_NoAIClient_ReturnsError verifies that requesting code
// generation with no AI configured fails gracefully (the resolved client is
// nil) instead of panicking on a nil ChatClient.
func TestGenerateForRule_NoAIClient_ReturnsError(t *testing.T) {
	// A non-empty system prompt ensures codegen does not bail early on that
	// check, so the nil-client guard is the only thing preventing a panic on
	// the nil ChatClient.
	ag := New(nil, nil, &config.Config{AI: config.AIConfig{SystemPrompt: "test prompt"}})

	err := ag.GenerateForRule(&aimanager.ActionRule{ID: 1, Title: "test"})
	if err == nil {
		t.Fatal("expected an error when generating with no AI configured, got nil")
	}
}
