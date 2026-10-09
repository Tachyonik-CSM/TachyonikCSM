// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the wording and level of the audit entry for a routine run.

package runaudit

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

const subject = `Execution rule "Retrieve the host assets" (id 4) run manually`

// The run that prompted this: the routine caught the failure and reported it
// as its result. The audit entry must say it failed, and why.
func TestAReportedErrorIsAWarningWithTheReason(t *testing.T) {
	why := "Failed to list tools: enrich tools (capabilities): GET http://localhost:8085/api/internal/tool-capabilities: status 401: Internal service authentication required"
	level, text := Entry(subject, nil, "error", why)
	if level != "Warning" {
		t.Errorf("level %q, want Warning", level)
	}
	if !strings.Contains(text, "failed") || !strings.Contains(text, why) {
		t.Errorf("entry %q does not say it failed and why", text)
	}
}

func TestAThrownErrorIsAWarningWithTheReason(t *testing.T) {
	level, text := Entry(subject, errors.New("ReferenceError: x is not defined"), "", "")
	if level != "Warning" || !strings.Contains(text, "ReferenceError: x is not defined") {
		t.Errorf("got %s %q", level, text)
	}
}

func TestASuccessStaysInfo(t *testing.T) {
	level, text := Entry(subject, nil, "success", "21 assets")
	if level != "Info" || text != subject+" (status success)" {
		t.Errorf("got %s %q", level, text)
	}
}

func TestAnErrorWithoutAMessageSaysSo(t *testing.T) {
	if _, text := Entry(subject, nil, "error", ""); !strings.Contains(text, "no reason given") {
		t.Errorf("entry %q", text)
	}
}

func TestALongReasonIsCutOnACharacter(t *testing.T) {
	long := strings.Repeat("ä", MaxReason) // two bytes each: the cap falls mid-rune
	_, text := Entry(subject, nil, "error", long)
	if !utf8.ValidString(text) {
		t.Error("cut mid-character")
	}
	if len(text) > len(subject)+MaxReason+20 {
		t.Errorf("reason not capped: %d bytes", len(text))
	}
}
