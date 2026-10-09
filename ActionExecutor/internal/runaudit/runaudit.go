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
// why it failed.
//
// It also names the run: a manual run by its execution rule, an automatic one
// by the action it handled and the execution rule that ran for it.
package runaudit

import (
	"fmt"
	"unicode/utf8"
)

// MaxReason caps the reason quoted in an entry: enough for an HTTP status and
// the service's message, short enough for the audit list.
const MaxReason = 500

// Entry returns the level and text of the audit entry for a run of subject
// (for example `Execution rule "X" (id 4) run manually`). runErr is what Run
// returned; status and message are the routine's result when it returned one.
func Entry(subject string, runErr error, status, message string) (level, text string) {
	switch {
	case runErr != nil:
		return "Warning", fmt.Sprintf("%s: failed (%s)", subject, reason(runErr.Error()))
	case status == "error":
		return "Warning", fmt.Sprintf("%s: failed (%s)", subject, reason(message))
	default:
		return "Info", fmt.Sprintf("%s (status %s)", subject, status)
	}
}

func reason(s string) string {
	if s == "" {
		return "no reason given"
	}
	if len(s) <= MaxReason {
		return s
	}
	cut := MaxReason
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// ManualSubject names a manual run. The id is the execution rule's: a manual
// run is started on a rule, not on an action.
func ManualSubject(ruleTitle string, ruleID int64) string {
	return fmt.Sprintf("Execution rule %q (id %d) run manually", ruleTitle, ruleID)
}

// AutoSubject names an automatic run: the action it handled, and the
// execution rule that ran for it.
func AutoSubject(actionTitle string, actionID, ruleID int64) string {
	return fmt.Sprintf("Action %q (id %d) executed automatically by execution rule %d", actionTitle, actionID, ruleID)
}
