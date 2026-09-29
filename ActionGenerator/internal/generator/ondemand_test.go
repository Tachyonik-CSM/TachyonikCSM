// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins that an on-demand rule's actions survive an evaluation that produced
// nothing.
//
// A rule with no trigger of its own never matches: its check() is a plain
// `return false`, and it raises an action only when something forces it — an
// action option of type "Action" pointing at it. The withdrawal that follows an
// empty evaluation would therefore delete that action on the very next pass,
// which is what happened: the log showed one created at 10:22:01 and deleted at
// 10:22:05, every time.
//
// The clients a Generator holds are concrete types, so the branch itself is not
// reachable from a test; the decision it turns on is, and that is what these
// pin.

package generator

import "testing"

func TestOnDemandRuleActionsAreNotWithdrawn(t *testing.T) {
	g := &Generator{}
	g.SetOnDemandRules(map[int64]bool{7: true})

	// The case from the log: rule 7 evaluated unforced, produces nothing.
	if g.shouldWithdraw(7, 0) {
		t.Error("an on-demand rule producing no actions was treated as no longer matching")
	}
}

func TestOrdinaryRuleStillWithdraws(t *testing.T) {
	g := &Generator{}
	g.SetOnDemandRules(map[int64]bool{7: true})

	// The behaviour that must not regress: a rule with a real condition that
	// has stopped matching still has its outstanding actions cleared.
	if !g.shouldWithdraw(8, 0) {
		t.Error("an ordinary rule that produced no actions did not withdraw them")
	}
}

func TestWithdrawalNeedsAnEmptyResult(t *testing.T) {
	g := &Generator{}

	// A rule that produced actions is matching; nothing is withdrawn, and the
	// on-demand flag is irrelevant.
	if g.shouldWithdraw(8, 1) {
		t.Error("a rule that produced an action was treated as no longer matching")
	}
	g.SetOnDemandRules(map[int64]bool{8: true})
	if g.shouldWithdraw(8, 1) {
		t.Error("a rule that produced an action was treated as no longer matching")
	}
}

// No rules marked at all — the state before the on-demand set has been
// published, and every installation that has no such rule.
func TestWithoutOnDemandRulesEverythingWithdraws(t *testing.T) {
	g := &Generator{}
	if !g.shouldWithdraw(1, 0) {
		t.Error("withdrawal stopped working when no rule is marked on-demand")
	}
}
