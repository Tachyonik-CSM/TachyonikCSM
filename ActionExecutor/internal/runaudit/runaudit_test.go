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
	level, text := Entry(subject, nil, "error", why, nil)
	if level != "Warning" {
		t.Errorf("level %q, want Warning", level)
	}
	if !strings.Contains(text, "failed") || !strings.Contains(text, why) {
		t.Errorf("entry %q does not say it failed and why", text)
	}
}

func TestAThrownErrorIsAWarningWithTheReason(t *testing.T) {
	level, text := Entry(subject, errors.New("ReferenceError: x is not defined"), "", "", nil)
	if level != "Warning" || !strings.Contains(text, "ReferenceError: x is not defined") {
		t.Errorf("got %s %q", level, text)
	}
}

func TestASuccessStaysInfo(t *testing.T) {
	level, text := Entry(subject, nil, "success", "21 assets", nil)
	if level != "Info" || text != subject+": success — 21 assets" {
		t.Errorf("got %s %q", level, text)
	}
}

func TestAnErrorWithoutAMessageSaysSo(t *testing.T) {
	if _, text := Entry(subject, nil, "error", "", nil); !strings.Contains(text, "no reason given") {
		t.Errorf("entry %q", text)
	}
}

func TestALongReasonIsCutOnACharacter(t *testing.T) {
	long := strings.Repeat("ä", MaxReason) // two bytes each: the cap falls mid-rune
	_, text := Entry(subject, nil, "error", long, nil)
	if !utf8.ValidString(text) {
		t.Error("cut mid-character")
	}
	if len(text) > len(subject)+MaxReason+20 {
		t.Errorf("reason not capped: %d bytes", len(text))
	}
}

func TestTheToolsARunStartedAreListed(t *testing.T) {
	_, text := Entry(subject, nil, "success", "", []ToolCall{
		{Tool: "gmp-get-results", Proxy: "office", OK: true},
		{Tool: "gmp-get-assets", Proxy: "office", Reason: "Proxy is not connected\n(status: offline)."},
	})
	want := subject + ": success. Tools: gmp-get-results @ office (ok), gmp-get-assets @ office (failed: Proxy is not connected (status: offline).)"
	if text != want {
		t.Errorf("got  %q\nwant %q", text, want)
	}
}

func TestAFailedRunStillListsItsTools(t *testing.T) {
	level, text := Entry(subject, nil, "error", "no results", []ToolCall{{Tool: "gmp-get-results", Proxy: "office", OK: true}})
	if level != "Warning" || !strings.HasSuffix(text, "failed (no results). Tools: gmp-get-results @ office (ok)") {
		t.Errorf("got %s %q", level, text)
	}
}

func TestManyToolCallsAreCounted(t *testing.T) {
	calls := make([]ToolCall, MaxToolCalls+3)
	for i := range calls {
		calls[i] = ToolCall{Tool: "t", Proxy: "p", OK: true}
	}
	_, text := Entry(subject, nil, "success", "", calls)
	if strings.Count(text, "t @ p (ok)") != MaxToolCalls || !strings.HasSuffix(text, "and 3 more") {
		t.Errorf("entry %q", text)
	}
}

func TestARunFromAnActionNamesBoth(t *testing.T) {
	got := ManualActionSubject("Retrieve OpenVAS results", 812, "OpenVAS results", 4)
	if got != `Action "Retrieve OpenVAS results" (id 812) executed manually by execution rule "OpenVAS results" (id 4)` {
		t.Errorf("subject %q", got)
	}
}
