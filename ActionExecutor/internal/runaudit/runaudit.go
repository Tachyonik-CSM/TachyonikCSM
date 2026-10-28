// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package runaudit words the audit-trail entry for one routine run.
//
// A run fails in one of two ways: the routine throws (Run returns an error),
// or it finishes and reports status "error" with a message saying why. Both
// are failures to the user reading the audit trail, so both become a Warning
// that carries the reason. The second used to be logged as Info and showed only
// "(status error)", so the audit trail recorded that a run happened and never
// why it failed. A success carries the routine's message too: what the run
// achieved is what the entry is there to document.
//
// It names the run: a manual run by its execution rule, or by the action it was
// started from and the rule; an automatic one by the action it handled and the
// execution rule that ran for it. And it lists the tools the run started, each
// with its proxy and how the call went, so the entry says what was done where.
package runaudit

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxReason caps the reason quoted in an entry, and the routine's message on a
// success: enough for an HTTP status and the service's message, short enough
// for the audit list.
const MaxReason = 500

// MaxToolCalls is how many tool calls an entry lists; the rest are counted.
const MaxToolCalls = 10

// maxToolReason caps a failed tool call's reason, which shares the entry with
// the others.
const maxToolReason = 120

// ToolCall is one tool a run started: by name and proxy when ToolManager
// answered, and whether the call succeeded.
type ToolCall struct {
	Tool   string
	Proxy  string
	OK     bool
	Reason string // why it failed; empty when OK
}

// Entry returns the level and text of the audit entry for a run of subject
// (for example `Execution rule "X" (id 4) run manually`). runErr is what Run
// returned; status and message are the routine's result when it returned one;
// tools are the tool calls it made, in order.
func Entry(subject string, runErr error, status, message string, tools []ToolCall) (level, text string) {
	switch {
	case runErr != nil:
		level, text = "Warning", fmt.Sprintf("%s: failed (%s)", subject, reason(runErr.Error(), MaxReason))
	case status == "error":
		level, text = "Warning", fmt.Sprintf("%s: failed (%s)", subject, reason(message, MaxReason))
	default:
		level, text = "Info", fmt.Sprintf("%s: %s", subject, status)
		if m := oneLine(message); m != "" {
			text += " — " + reason(m, MaxReason)
		}
	}
	return level, text + toolList(tools)
}

// toolList is ". Tools: a @ p (ok), b @ p (failed: why)", or nothing for a run
// that started none.
func toolList(tools []ToolCall) string {
	if len(tools) == 0 {
		return ""
	}
	parts := make([]string, 0, MaxToolCalls+1)
	for i, c := range tools {
		if i == MaxToolCalls {
			parts = append(parts, fmt.Sprintf("and %d more", len(tools)-MaxToolCalls))
			break
		}
		name := c.Tool
		if c.Proxy != "" {
			name += " @ " + c.Proxy
		}
		if c.OK {
			parts = append(parts, name+" (ok)")
		} else {
			parts = append(parts, fmt.Sprintf("%s (failed: %s)", name, reason(c.Reason, maxToolReason)))
		}
	}
	return ". Tools: " + strings.Join(parts, ", ")
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func reason(s string, max int) string {
	s = oneLine(s)
	if s == "" {
		return "no reason given"
	}
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// ManualSubject names a manual run started on the execution rule itself.
func ManualSubject(ruleTitle string, ruleID int64) string {
	return fmt.Sprintf("Execution rule %q (id %d) run manually", ruleTitle, ruleID)
}

// UnknownRuleSubject names a manual run of a rule that is not loaded, so only
// its id is known.
func UnknownRuleSubject(ruleID int64) string {
	return fmt.Sprintf("Execution rule %d run manually", ruleID)
}

// ManualActionSubject names a manual run started from an action (Execute in
// its dialog): the action, and the execution rule that ran for it.
func ManualActionSubject(actionTitle string, actionID int64, ruleTitle string, ruleID int64) string {
	return fmt.Sprintf("Action %q (id %d) executed manually by execution rule %q (id %d)", actionTitle, actionID, ruleTitle, ruleID)
}

// AutoSubject names an automatic run: the action it handled, and the
// execution rule that ran for it.
func AutoSubject(actionTitle string, actionID, ruleID int64) string {
	return fmt.Sprintf("Action %q (id %d) executed automatically by execution rule %d", actionTitle, actionID, ruleID)
}
