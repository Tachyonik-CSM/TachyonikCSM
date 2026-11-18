// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Putting a rule's evaluation in place in ActionManager.
//
// A rule's action is identified by its rule and its owner, not by its title
// (AG-EVAL-09). A rule that matches updates its open action — the title may
// carry a number that changed — or raises one when there is none, unless a
// Done action whose situation has not ended holds it back (AG-EVAL-26). A rule
// that evaluates to no action withdraws its New actions and marks its Done
// ones cleared, so the next match raises a new action. Several open actions of
// one rule, left by data from before ActionManager refused them, are
// consolidated to the newest (AG-EVAL-27).
//
// The per-user pass, the shared workspace and evaluation requests all go
// through here, so they cannot drift apart.

package generator

import (
	"fmt"

	"tachyonik/actiongenerator/internal/actionmanager"
	"tachyonik/lib/logger"
)

// firstAction keeps the first action a rule's evaluation returned. One rule
// has at most one open action per owner (AG-EVAL-09); a routine returning
// more for the same rule is told so in the log.
func firstAction(ruleID int64, actions []actionmanager.CreateActionRequest) []actionmanager.CreateActionRequest {
	if len(actions) > 1 {
		logger.Warnf("Rule %d returned %d actions; a rule raises one action per owner, so only the first (%q) is used", ruleID, len(actions), actions[0].Title)
		return actions[:1]
	}
	return actions
}

// placeAction puts the action a matching rule returned in place for owner:
// it updates the rule's open action, or creates the action when there is none
// and no uncleared Done action holds the rule back. force (a forced evaluation
// request, AG-EVAL-13) creates despite an uncleared Done action. Reports
// whether an action was created.
func (g *Generator) placeAction(owner, ruleID int64, req actionmanager.CreateActionRequest, force bool) (bool, error) {
	existing, err := g.actionMgr.GetRuleActions(owner, ruleID)
	if err != nil {
		return false, fmt.Errorf("cannot read the actions of rule %d for user %d: %w", ruleID, owner, err)
	}

	if len(existing.Open) > 0 {
		current := existing.Open[0]
		changed, err := g.actionMgr.RefreshAction(current.ID, req)
		if err != nil {
			logger.Warnf("Could not update action %d of rule %d for user %d: %v", current.ID, ruleID, owner, err)
		} else if changed {
			logger.Infof("Updated action %d of rule %d for user %d: %q", current.ID, ruleID, owner, req.Title)
		} else {
			logger.Debugf("Action %d of rule %d for user %d is up to date", current.ID, ruleID, owner)
		}
		if len(existing.Open) > 1 {
			if n, derr := g.actionMgr.DeleteNewActionsByRule(owner, ruleID, current.ID); derr != nil {
				logger.Warnf("Could not consolidate the actions of rule %d for user %d: %v", ruleID, owner, derr)
			} else if n > 0 {
				logger.Infof("Consolidated %d older action(s) of rule %d for user %d into action %d", n, ruleID, owner, current.ID)
			}
		}
		return false, nil
	}

	if existing.DoneUncleared && !force {
		logger.Debugf("Rule %d matches for user %d, but its Done action has not been cleared; nothing raised", ruleID, owner)
		return false, nil
	}

	req.UserID = owner
	ruleIDCopy := ruleID
	req.ActionRuleID = &ruleIDCopy
	action, err := g.actionMgr.CreateAction(req)
	if err != nil {
		return false, fmt.Errorf("failed to create the action of rule %d for user %d: %w", ruleID, owner, err)
	}
	logger.Infof("Created action %d %q of rule %d for user %d", action.ID, req.Title, ruleID, owner)
	return true, nil
}

// withdraw handles a rule that evaluated to no action for owner: its New
// actions are withdrawn (AG-EVAL-11) and its Done ones marked cleared
// (AG-EVAL-26).
func (g *Generator) withdraw(owner, ruleID int64) {
	if n, err := g.actionMgr.DeleteNewActionsByRule(owner, ruleID, 0); err != nil {
		logger.Warnf("Stale-action cleanup failed for user %d rule %d: %v", owner, ruleID, err)
	} else if n > 0 {
		logger.Infof("Deleted %d stale action(s) for user %d rule %d (rule no longer matches)", n, owner, ruleID)
	}
	g.clearDone(ruleID, owner)
}

// withdrawShared is withdraw for a shared workspace: every owner's
// (AG-EVAL-12).
func (g *Generator) withdrawShared(ruleID int64) {
	if n, err := g.actionMgr.DeleteNewActionsByRuleExceptUser(ruleID, 0); err != nil {
		logger.Warnf("Shared stale-action cleanup failed for rule %d: %v", ruleID, err)
	} else if n > 0 {
		logger.Infof("Deleted %d shared stale action(s) for rule %d (rule no longer matches)", n, ruleID)
	}
	g.clearDone(ruleID, 0)
}

// clearDone marks the rule's Done actions of owner (0: every owner) cleared.
func (g *Generator) clearDone(ruleID, owner int64) {
	if n, err := g.actionMgr.ClearDoneActionsByRule(ruleID, owner); err != nil {
		logger.Warnf("Could not mark the Done actions of rule %d cleared (user %d; 0 = every owner): %v", ruleID, owner, err)
	} else if n > 0 {
		logger.Infof("Rule %d no longer matches: %d Done action(s) cleared (user %d; 0 = every owner); it raises a new action when it matches again", ruleID, n, owner)
	}
}
