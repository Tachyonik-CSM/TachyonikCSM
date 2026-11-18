// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins that a scan routine runs only when its code matches the checksum
// ToolManager sends with it: one without a checksum, or whose code does not
// match, is refused with an error result and not run. The other tests scan
// through scanChecked, which records the checksum as ToolManager would.

package toolscan

import (
	"strings"
	"testing"

	"tachyonik/lib/aicode"
)

// scanChecked scans routines as ToolManager sends them: each with the
// checksum of its code, unless the test set one.
func (s *Scanner) scanChecked(routines []RoutineInput) []ToolResult {
	for i := range routines {
		if routines[i].SHA256 == "" {
			routines[i].SHA256 = aicode.Checksum(routines[i].Code)
		}
	}
	return s.Scan(routines)
}

func TestScanner_RefusesARoutineWithoutItsChecksum(t *testing.T) {
	code := `var rules = [{ name: "x", detect: function() { return { version: "1" }; } }];`
	for _, c := range []struct{ name, sha, want string }{
		{"missing", "", "no SHA256"},
		{"altered", aicode.Checksum(code + " "), "does not match"},
	} {
		t.Run(c.name, func(t *testing.T) {
			results := New().Scan([]RoutineInput{{Name: "x", Code: code, SHA256: c.sha}})
			if len(results) != 1 || !strings.Contains(results[0].Error, c.want) {
				t.Errorf("results = %+v, want one refusal naming %q", results, c.want)
			}
		})
	}
	results := New().Scan([]RoutineInput{{Name: "x", Code: code, SHA256: aicode.Checksum(code)}})
	if len(results) != 1 || results[0].Error != "" {
		t.Errorf("a matching routine: %+v", results)
	}
}
