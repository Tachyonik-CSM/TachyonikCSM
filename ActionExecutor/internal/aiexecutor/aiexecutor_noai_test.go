// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for the no-AI path: with no chat client configured, an
// already-generated routine still loads and runs, while asking to generate a new
// one is refused with a clear error rather than panicking on a nil client.

package aiexecutor

import (
	"testing"

	"tachyonik/actionexecutor/internal/aimanager"
	"tachyonik/actionexecutor/internal/config"
	"tachyonik/actionexecutor/internal/jsruntime"
)

// sampleExecRoutine is a minimal, hand-written execution routine. It mirrors
// the shape produced by AI code generation (an `executors` array whose entries
// expose a `run(api)` function) but needs no AI to exist — running a
// routine is pure JS.
const sampleExecRoutine = `
var executors = [
  {
    name: "noop-executor",
    ruleId: 1,
    run: function (api) {
      return { status: "success", message: "ran without AI" };
    }
  }
];
`

// TestRuntime_NoAIClient_AvailableAndRuns verifies that an AIActionExecutor
// constructed without a chat client (i.e. AI unset/disabled) still exposes its
// loaded runtime via GetRuntime and that the runtime executes. Executing a
// routine is pure JS; AI is only needed to generate it.
func TestRuntime_NoAIClient_AvailableAndRuns(t *testing.T) {
	ag := New(nil, nil, &config.Config{}) // nil chatClient == no AI configured

	rt := jsruntime.New()
	if err := rt.LoadFromString(sampleExecRoutine); err != nil {
		t.Fatalf("failed to load sample routine: %v", err)
	}
	ag.runtimes[1] = rt

	got := ag.GetRuntime(1)
	if got == nil {
		t.Fatal("expected runtime to be available with AI unset, got nil")
	}

	// apiVal is unused by this routine, so undefined is fine.
	result, err := got.Run(nil, 0)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result == nil || result.Status != "success" {
		t.Fatalf("unexpected exec result: %+v", result)
	}
}

// TestGenerateForRule_NoAIClient_ReturnsError verifies that requesting code
// generation with no AI configured fails gracefully (the resolved client is
// nil) instead of panicking on a nil ChatClient. A non-empty system prompt
// ensures codegen does not bail early on that check, so the nil-client guard
// is the only thing preventing a panic.
func TestGenerateForRule_NoAIClient_ReturnsError(t *testing.T) {
	ag := New(nil, nil, &config.Config{AI: config.AIConfig{SystemPrompt: "test prompt"}})

	err := ag.GenerateForRule(&aimanager.ExecutionRule{ID: 1, Title: "test"})
	if err == nil {
		t.Fatal("expected an error when generating with no AI configured, got nil")
	}
}
