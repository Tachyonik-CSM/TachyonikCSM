// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for the no-AI path: with no chat client configured, an
// already-generated routine still loads and runs, while asking to generate a new
// one is refused with a clear error rather than panicking on a nil client.

package aiimporter

import (
	"testing"

	"tachyonik/sourceimporter/internal/aimanager"
	"tachyonik/sourceimporter/internal/config"
	"tachyonik/sourceimporter/internal/jsruntime"
)

// sampleImportRoutine is a minimal, hand-written import routine. It mirrors the
// shape produced by AI code generation (a global `importFunction(ctx)` that
// returns assets/vulnerabilities/detections) but needs no AI to exist —
// executing a routine is pure JS.
const sampleImportRoutine = `
var importFunction = function (ctx) {
  return {
    assets: [{ name: "host-1", type: "Host", lastSeen: "2026-05-29" }],
    vulnerabilities: [],
    detections: []
  };
};
`

// TestExecutor_NoAIClient_AvailableAndRuns verifies that an AIImporter
// constructed without a chat client (i.e. AI unset/disabled) still reports a
// loaded executor and that the executor runs. This is the regression guard for
// the bug where a nil chat client disabled the whole import subsystem and every
// source was marked "No import routine".
func TestExecutor_NoAIClient_AvailableAndRuns(t *testing.T) {
	ai := New(nil, nil, nil, nil, &config.Config{}) // nil chatClient == no AI configured

	exec := jsruntime.New()
	if err := exec.LoadFromString(sampleImportRoutine); err != nil {
		t.Fatalf("failed to load sample routine: %v", err)
	}
	ai.executors[1] = exec

	if !ai.HasExecutor(1) {
		t.Fatal("expected executor to be available with AI unset")
	}

	result, err := exec.Execute(jsruntime.ImportContext{
		FileName:    "scan.xml",
		FileContent: `<?xml version="1.0"?><nmaprun></nmaprun>`,
		SourceType:  "nmap",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result == nil || len(result.Assets) != 1 || result.Assets[0].Name != "host-1" {
		t.Fatalf("unexpected import result: %+v", result)
	}
}

// TestGenerateForRule_NoAIClient_ReturnsError verifies that requesting code
// generation with no AI configured fails gracefully (the resolved client is
// nil) instead of panicking on a nil ChatClient.
func TestGenerateForRule_NoAIClient_ReturnsError(t *testing.T) {
	// A non-empty system prompt ensures codegen does not bail early on that
	// check, so the nil-client guard is the only thing preventing a panic on
	// the nil ChatClient.
	ai := New(nil, nil, nil, nil, &config.Config{AI: config.AIConfig{SystemPrompt: "test prompt"}})

	err := ai.GenerateForRule(&aimanager.ImportRule{ID: 1, Type: "nmap"})
	if err == nil {
		t.Fatal("expected an error when generating with no AI configured, got nil")
	}
}
