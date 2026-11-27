// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Whom a rule is evaluated for, and whose copies of its action are
// consolidated.
//
// A user in no workspace is evaluated on their own (AG-EVAL-06). A workspace —
// a SaaS group, whose members the managers answer with the workspace's data
// (PA-BR-07) — is evaluated once, for its workspace admin, and its actions are
// placed under them; the other members' New copies of a rule are withdrawn
// (AG-EVAL-07, -12; docs/plans/workspace-actions-once.md). An appliance is one
// workspace for the whole instance, evaluated for its primary owner, its
// copies consolidated instance-wide.

package generator

import (
	"fmt"
	"sort"

	"tachyonik/actiongenerator/internal/systemmanager"
	"tachyonik/lib/logger"
)

// target is whom a rule is evaluated for. owner is evaluated and gets the
// action; others are the workspace's other members, whose copies are
// consolidated; instance marks the appliance's instance-wide workspace.
type target struct {
	owner    int64
	others   []int64
	instance bool
}

func (t target) String() string {
	switch {
	case t.instance:
		return fmt.Sprintf("the workspace (owner %d)", t.owner)
	case len(t.others) > 0:
		return fmt.Sprintf("the workspace of admin %d", t.owner)
	default:
		return fmt.Sprintf("user %d", t.owner)
	}
}

// passTargets is a SaaS pass's targets: one per workspace, one per user in none.
// A deactivated user in no workspace is not evaluated (AG-EVAL-25). A
// workspace is evaluated for its admin even when the admin is deactivated,
// and its deactivated members' copies are consolidated like the others'
// (decided 2026-11-27, D1 and D2): the actions are the workspace's. A
// workspace whose members are all deactivated has no one to read them, and is
// not evaluated, as a deactivated user is not.
func passTargets(users []systemmanager.User) []target {
	var out []target
	workspaces := map[int64][]int64{}
	someoneActive := map[int64]bool{}
	for _, u := range users {
		if u.WorkspaceAdminID != 0 {
			workspaces[u.WorkspaceAdminID] = append(workspaces[u.WorkspaceAdminID], u.ID)
			if !u.Deactivated() {
				someoneActive[u.WorkspaceAdminID] = true
			}
			continue
		}
		if u.Deactivated() {
			continue
		}
		out = append(out, target{owner: u.ID})
	}
	admins := make([]int64, 0, len(workspaces))
	for a := range workspaces {
		if someoneActive[a] {
			admins = append(admins, a)
		}
	}
	sort.Slice(admins, func(i, j int) bool { return admins[i] < admins[j] })
	for _, a := range admins {
		out = append(out, workspaceTarget(a, workspaces[a]))
	}
	return out
}

// workspaceTarget is the workspace of admin with members (the admin among
// them or not).
func workspaceTarget(admin int64, members []int64) target {
	t := target{owner: admin}
	for _, m := range members {
		if m != admin {
			t.others = append(t.others, m)
		}
	}
	return t
}

// requestTarget is whom a request by user is evaluated for (AG-EVAL-13,
// -29): the appliance's primary owner, the user's workspace admin, or the
// user.
func (g *Generator) requestTarget(user *systemmanager.User) (target, error) {
	if primaryID, shared := g.workspaceOwner(); shared {
		return target{owner: primaryID, instance: true}, nil
	}
	if user.WorkspaceAdminID == 0 {
		return target{owner: user.ID}, nil
	}
	users, err := g.systemMgr.GetAllUsers()
	if err != nil {
		return target{}, fmt.Errorf("failed to read the members of user %d's workspace: %w", user.ID, err)
	}
	var members []int64
	for _, u := range users {
		if u.WorkspaceAdminID == user.WorkspaceAdminID {
			members = append(members, u.ID)
		}
	}
	return workspaceTarget(user.WorkspaceAdminID, members), nil
}

// withdrawFor handles a rule that evaluated to no action for t: its New
// actions are withdrawn and its Done ones cleared, for the owner and every
// member of a workspace (AG-EVAL-11, -12).
func (g *Generator) withdrawFor(t target, ruleID int64) {
	if t.instance {
		g.withdrawShared(ruleID)
		return
	}
	g.withdraw(t.owner, ruleID)
	for _, m := range t.others {
		g.withdraw(m, ruleID)
	}
}

// consolidateFor withdraws the other members' New copies of a rule whose
// action is now the owner's (AG-EVAL-12). Copies a member took on stay: the
// server withdraws New ones only.
func (g *Generator) consolidateFor(t target, ruleID int64) {
	if t.instance {
		g.consolidateShared(ruleID, t.owner)
		return
	}
	for _, m := range t.others {
		if n, err := g.actionMgr.DeleteNewActionsByRule(m, ruleID, 0); err != nil {
			logger.Warnf("Workspace consolidation failed for rule %d, member %d: %v", ruleID, m, err)
		} else if n > 0 {
			logger.Infof("Consolidated %d duplicate action(s) of rule %d from member %d into %s", n, ruleID, m, t)
		}
	}
}
