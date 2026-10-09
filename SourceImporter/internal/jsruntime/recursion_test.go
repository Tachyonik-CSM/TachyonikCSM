// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that runaway recursion in an import routine fails the import instead of
// overflowing the Go stack, which cannot be recovered and would take the daemon
// down.

package jsruntime

import "testing"

func TestRecursionInAnImportIsAnError(t *testing.T) {
	exec := New()
	if err := exec.LoadFromString(`function down(n) { return down(n + 1); }
		var importFunction = function (ctx) { return down(0); };`); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	if _, err := exec.Execute(ImportContext{FileContent: "{}", FileName: "f.json"}); err == nil {
		t.Error("runaway recursion was not reported as a failed import")
	}
}
