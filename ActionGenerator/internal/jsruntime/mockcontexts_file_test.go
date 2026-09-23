// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Keeps WebUI/src/generated/actionRuleMockContexts.json identical to the mock
// scenarios this package validates routines against.
//
// The WebUI's routine Test and Context tabs show those scenarios. They used to
// be a hand-kept TypeScript copy, and every change to the rule context had to be
// made there too — which is exactly the kind of copy that drifts. Now the file
// is generated from MockScenarios, and this test fails when it is stale.
//
// To regenerate after changing the scenarios or the context shape:
//
//	UPDATE_MOCK_CONTEXTS=1 go test ./internal/jsruntime/ -run TestMockContextsFileIsCurrent

package jsruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const mockContextsFile = "../../../WebUI/src/generated/actionRuleMockContexts.json"

func renderMockContextsFile(t *testing.T) []byte {
	t.Helper()
	type scenario struct {
		Label string                 `json:"label"`
		Ctx   map[string]interface{} `json:"ctx"`
	}
	file := struct {
		Generated string     `json:"_generated"`
		Scenarios []scenario `json:"scenarios"`
	}{
		Generated: "GENERATED from ActionGenerator's jsruntime.MockScenarios — do not edit. " +
			"Regenerate: UPDATE_MOCK_CONTEXTS=1 go test ./internal/jsruntime/ -run TestMockContextsFileIsCurrent",
	}
	for _, sc := range MockScenarios() {
		file.Scenarios = append(file.Scenarios, scenario{Label: sc.Label, Ctx: sc.Ctx.ToMap()})
	}
	out, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatalf("marshal mock contexts: %v", err)
	}
	return append(out, '\n')
}

func TestMockContextsFileIsCurrent(t *testing.T) {
	want := renderMockContextsFile(t)
	path := filepath.FromSlash(mockContextsFile)

	if os.Getenv("UPDATE_MOCK_CONTEXTS") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("wrote %s", path)
		return
	}

	got, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// A checkout without the WebUI (a module-only build context) has
		// nothing to keep in step.
		if _, statErr := os.Stat(filepath.Dir(filepath.Dir(path))); os.IsNotExist(statErr) {
			t.Skip("WebUI not present")
		}
	}
	if err != nil {
		t.Fatalf("read %s: %v — regenerate with UPDATE_MOCK_CONTEXTS=1", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s is stale — regenerate with UPDATE_MOCK_CONTEXTS=1 go test ./internal/jsruntime/ -run TestMockContextsFileIsCurrent", path)
	}
}
