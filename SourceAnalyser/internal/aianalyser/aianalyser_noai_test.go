// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for the no-AI path: with no chat client configured, generation is
// refused with a clear error rather than panicking on a nil client, while
// already-generated routines stay loaded and keep running.

package aianalyser

import (
	"testing"
	"time"

	"tachyonik/sourceanalyser/internal/aimanager"
	"tachyonik/sourceanalyser/internal/config"
	"tachyonik/sourceanalyser/internal/jsruntime"
)

// sampleAnalysisRoutine is a minimal, hand-written analysis routine. It mirrors
// the shape produced by AI code generation (a `rules` array whose entries
// expose an `analyze(ctx)` function) but needs no AI to exist — which is the
// whole point: executing routines is pure JS.
const sampleAnalysisRoutine = `
var rules = [
  {
    name: "nmap-detector",
    ruleId: 1,
    analyze: function (ctx) {
      if (ctx.content.indexOf("<nmaprun") !== -1) {
        return { sourceType: "nmap", status: "Analysed" };
      }
      return null;
    }
  }
];
`

// TestAnalyzeSource_NoAIClient_ExecutesLoadedRoutine verifies that an
// AIAnalyser constructed without a chat client (i.e. AI unset/disabled) still
// runs an already-loaded routine and identifies the source. This is the
// regression guard for the bug where a nil chat client disabled the entire
// rule engine and every source was marked Unsupported.
func TestAnalyzeSource_NoAIClient_ExecutesLoadedRoutine(t *testing.T) {
	a := New(nil, nil, &config.Config{}) // nil chatClient == no AI configured

	exec := jsruntime.New(5 * time.Second)
	if err := exec.LoadFromString(sampleAnalysisRoutine); err != nil {
		t.Fatalf("failed to load sample routine: %v", err)
	}
	a.executors[1] = exec

	if a.RuleCount() != 1 {
		t.Fatalf("expected 1 loaded executor, got %d", a.RuleCount())
	}

	result, err := a.AnalyzeSource(jsruntime.RuleContext{
		Filename: "nmap_7.98-example-host-detection.xml",
		Content:  `<?xml version="1.0"?><nmaprun scanner="nmap"></nmaprun>`,
	})
	if err != nil {
		t.Fatalf("AnalyzeSource returned error: %v", err)
	}
	if result == nil {
		t.Fatal("expected a rule match with AI unset, got nil (routine not applied)")
	}
	if result.SourceType != "nmap" || result.Status != "Analysed" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

// TestGenerateForRule_NoAIClient_ReturnsError verifies that requesting code
// generation with no AI configured fails gracefully (the resolved client is
// nil) instead of panicking on a nil ChatClient.
func TestGenerateForRule_NoAIClient_ReturnsError(t *testing.T) {
	// A non-empty system prompt ensures codegen does not bail early on that
	// check, so the nil-client guard is the only thing preventing a panic on
	// the nil ChatClient.
	a := New(nil, nil, &config.Config{AI: config.AIConfig{SystemPrompt: "test prompt"}})

	err := a.GenerateForRule(&aimanager.AnalysisRule{ID: 1, Description: "test"})
	if err == nil {
		t.Fatal("expected an error when generating with no AI configured, got nil")
	}
}
