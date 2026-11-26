// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package generator is the evaluation engine. For each user it runs every
// loaded rule routine through one pass of the platform API bridge — the
// routine reads what it needs from the managers' APIs, for that user — and
// creates the actions the routines return in ActionManager.
//
// A rule that no longer triggers has its outstanding "New" actions withdrawn, so
// an action disappears once the user has done the thing it asked for. A rule
// that could not be decided — a bridge call failed — keeps them (PA-RT-03).
//
// In shared-workspace mode the rules are evaluated once, for the primary owner,
// whose answers cover the whole workspace (PA-BR-07), rather than once per user,
// so a team sees one action rather than one each.
//
// For each owner the rules go from the highest action priority to the lowest
// (AG-EVAL-28): the number of actions a user may hold is limited, and what the
// limit refuses must be the least important advice.
package generator

import (
	"fmt"
	"sort"
	"sync"

	"tachyonik/actiongenerator/internal/actionmanager"
	"tachyonik/actiongenerator/internal/jsruntime"
	"tachyonik/actiongenerator/internal/systemmanager"
	"tachyonik/lib/apibridge"
	"tachyonik/lib/apiholder"
	"tachyonik/lib/drain"
	"tachyonik/lib/logger"
)

// Generator handles the action generation logic
type Generator struct {
	systemMgr *systemmanager.Client
	actionMgr *actionmanager.Client
	executors map[int64]*jsruntime.JSRuleExecutor
	// Rules that fire only when asked for by name; see SetOnDemandRules.
	// Guarded by executorMu, which already covers the rule set it belongs to.
	onDemandRules map[int64]bool
	// The rules from the highest action priority down; see SetRuleOrder.
	// Guarded by executorMu too.
	ruleOrder  []int64
	executorMu sync.RWMutex
	// catalogue holds the API bridge the routines read through; without it
	// no rule can be decided.
	catalogue *apiholder.Holder
	// gate lets a stop wait for the unit being evaluated (shutdown.go).
	gate drain.Gate
}

// SetCatalogue gives the generator the API bridge for bridge routines.
func (g *Generator) SetCatalogue(c *apiholder.Holder) { g.catalogue = c }

// newPass starts one evaluation pass of the API bridge, or nil without one.
// One pass per evaluation pass: a user's answers are fetched once and shared
// by every rule (PA-BR-05).
func (g *Generator) newPass() *apibridge.Pass {
	if g.catalogue == nil {
		return nil
	}
	if b, _ := g.catalogue.Bridge(); b != nil {
		return b.NewPass()
	}
	return nil
}

// evaluate runs one routine for one user through the pass. unknown is true
// when the outcome could not be decided, in which case nothing is created or
// withdrawn for the rule (PA-RT-03) — also for a context routine, which
// aigenerator refuses to load and which would have nothing to read here.
func (g *Generator) evaluate(executor *jsruntime.JSRuleExecutor, pass *apibridge.Pass, userID int64,
	force bool) (actions []actionmanager.CreateActionRequest, unknown bool, err error) {
	if !executor.IsBridge() {
		logger.Warnf("A context routine cannot be evaluated for user %d: this version runs bridge routines only", userID)
		return nil, true, nil
	}
	if pass == nil {
		logger.Warnf("A routine cannot be evaluated for user %d: the platform API catalogue is not loaded", userID)
		return nil, true, nil
	}
	return executor.EvaluateBridge(pass, userID, force)
}

// New creates a new Generator instance
func New(systemMgr *systemmanager.Client, actionMgr *actionmanager.Client) *Generator {
	return &Generator{
		systemMgr: systemMgr,
		actionMgr: actionMgr,
		executors: make(map[int64]*jsruntime.JSRuleExecutor),
	}
}

// SetExecutors replaces the per-rule executor map.
func (g *Generator) SetExecutors(executors map[int64]*jsruntime.JSRuleExecutor) {
	g.executorMu.Lock()
	defer g.executorMu.Unlock()
	g.executors = executors
}

// SetOnDemandRules records which rules fire only when asked for by name — the
// ones with no trigger prompt of their own.
//
// Their actions are never withdrawn by an evaluation that found no match,
// because for these rules no evaluation ever finds one: check() is a plain
// `return false`, so the sweep would delete the action a user had just asked
// for, seconds after asking. An action raised this way stays until it is acted
// on or deleted.
func (g *Generator) SetOnDemandRules(ids map[int64]bool) {
	g.executorMu.Lock()
	defer g.executorMu.Unlock()
	g.onDemandRules = ids
}

// SetRuleOrder records the order a pass evaluates the rules in for each owner:
// from the highest action priority to the lowest, equal ones by ascending id
// (AG-EVAL-28).
func (g *Generator) SetRuleOrder(ids []int64) {
	g.executorMu.Lock()
	defer g.executorMu.Unlock()
	g.ruleOrder = ids
}

// orderedRules lists the rules of executors in the order given. An executor
// whose rule the order does not name — the two are set one after the other —
// comes after them, by ascending id, so it is still evaluated.
func orderedRules(executors map[int64]*jsruntime.JSRuleExecutor, order []int64) []int64 {
	out := make([]int64, 0, len(executors))
	seen := make(map[int64]bool, len(order))
	for _, id := range order {
		if _, ok := executors[id]; ok && !seen[id] {
			out = append(out, id)
			seen[id] = true
		}
	}
	var rest []int64
	for id := range executors {
		if !seen[id] {
			rest = append(rest, id)
		}
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i] < rest[j] })
	return append(out, rest...)
}

// isOnDemand reports whether a rule fires only when forced.
func (g *Generator) isOnDemand(ruleID int64) bool {
	g.executorMu.RLock()
	defer g.executorMu.RUnlock()
	return g.onDemandRules[ruleID]
}

// shouldWithdraw decides whether an evaluation that produced no actions means
// the rule has stopped matching, and its outstanding actions should go.
//
// For an ordinary rule it does: the action was raised because the condition
// held, and it holds no longer. For an on-demand rule it does not — that rule
// produces nothing unless forced, so "no actions" is its answer every time, and
// treating it as "no longer matches" would delete the action a user had just
// asked for, seconds after asking.
func (g *Generator) shouldWithdraw(ruleID int64, produced int) bool {
	return produced == 0 && !g.isOnDemand(ruleID)
}

// ProcessRules iterates over all users and checks action rules
func (g *Generator) ProcessRules() error {
	unit := g.gate.Enter("a pass over every rule", nil)
	if unit == nil {
		logger.Debug("Not processing action rules: stopping")
		return nil
	}
	defer unit.Leave()
	logger.Info("Processing action rules...")

	// Get all users
	users, err := g.systemMgr.GetAllUsers()
	if err != nil {
		return fmt.Errorf("failed to get users: %w", err)
	}

	if len(users) == 0 {
		logger.Debug("No users found")
		return nil
	}

	logger.Infof("Checking rules for %d users", len(users))

	// Take a snapshot of current executors
	g.executorMu.RLock()
	executorSnapshot := make(map[int64]*jsruntime.JSRuleExecutor, len(g.executors))
	for k, v := range g.executors {
		executorSnapshot[k] = v
	}
	order := orderedRules(executorSnapshot, g.ruleOrder)
	g.executorMu.RUnlock()

	if len(executorSnapshot) == 0 {
		logger.Warn("No JS rules loaded, skipping rule processing")
		return nil
	}

	totalRules := 0
	for _, exec := range executorSnapshot {
		totalRules += exec.RuleCount()
	}
	logger.Infof("Using %d per-rule executors with %d total rules", len(executorSnapshot), totalRules)

	// Appliance mode is a single shared workspace: every non-anonymous user sees
	// the same data, so generating one action set per user surfaces as duplicates
	// in the shared Actions view. When the instance reports appliance mode with a
	// primary owner, evaluate each rule ONCE for that owner — the managers answer
	// with the whole workspace's data (PA-BR-07) — and create the actions under
	// them. SaaS keeps per-user
	// generation. A lookup failure falls back to per-user (safe default).
	primaryID, shared := g.workspaceOwner()

	actionsCreated := 0
	var created int
	if shared {
		logger.Infof("Appliance shared-workspace mode: generating one action set under user %d", primaryID)
		created, err = g.processSharedWorkspace(unit, users, executorSnapshot, order, primaryID)
	} else {
		created, err = g.processJSRules(unit, users, executorSnapshot, order)
	}
	if err != nil {
		logger.Errorf("Error processing JS rules: %v", err)
	}
	actionsCreated += created

	if actionsCreated > 0 {
		logger.Infof("Created %d new actions", actionsCreated)
	} else {
		logger.Info("No new actions created")
	}

	return nil
}

// workspaceOwner reports whether the instance is a shared workspace and, if
// so, its primary owner, for whom it is evaluated (AG-EVAL-07). A mode that
// cannot be read, or an appliance without a primary owner, is evaluated per
// user, and the log says so (AG-EVAL-08).
func (g *Generator) workspaceOwner() (primaryID int64, shared bool) {
	ws, err := g.systemMgr.GetWorkspace()
	if err != nil {
		logger.Warnf("Could not determine workspace mode, defaulting to per-user generation: %v", err)
		return 0, false
	}
	if !ws.IsAppliance() {
		return 0, false
	}
	if ws.PrimaryUserID <= 0 {
		logger.Warn("Appliance mode but no primary user reported; falling back to per-user generation")
		return 0, false
	}
	return ws.PrimaryUserID, true
}

// ProcessRuleForUser evaluates a single action rule for a single user and
// creates actions if the rule's JS routine produces any. Returns the number
// of actions created.
func (g *Generator) ProcessRuleForUser(ruleID int64, userID int64, force bool) (int, error) {
	unit := g.gate.Enter(fmt.Sprintf("rule %d for user %d", ruleID, userID), nil)
	if unit == nil {
		return 0, ErrStopping
	}
	defer unit.Leave()
	g.executorMu.RLock()
	executor, ok := g.executors[ruleID]
	g.executorMu.RUnlock()
	if !ok {
		return 0, fmt.Errorf("no executor loaded for rule %d", ruleID)
	}

	// The user must exist: the request names one, and an action for an
	// account SystemManager does not know would belong to nobody. A
	// deactivated one is not evaluated (AG-EVAL-25).
	user, err := g.systemMgr.GetUser(userID)
	if err != nil {
		return 0, fmt.Errorf("failed to get user %d: %w", userID, err)
	}
	if user.Deactivated() {
		return 0, fmt.Errorf("user %d is deactivated; its actions are left as they are", userID)
	}

	// In a shared workspace the request is evaluated for the primary owner
	// and its action placed under them, as the shared pass would; the
	// requester only asked (AG-EVAL-13).
	owner, who := userID, fmt.Sprintf("user %d", userID)
	primaryID, shared := g.workspaceOwner()
	if shared {
		owner, who = primaryID, fmt.Sprintf("the workspace (owner %d), asked by user %d", primaryID, userID)
		unit.Rename(fmt.Sprintf("rule %d for %s", ruleID, who))
	}

	actions, unknown, err := g.evaluate(executor, g.newPass(), owner, force)
	if err != nil {
		return 0, fmt.Errorf("error evaluating rule %d for %s: %w", ruleID, who, err)
	}
	if unknown {
		logger.Infof("Rule %d could not be decided for %s; its actions are left as they are", ruleID, who)
		return 0, nil
	}

	logger.Infof("Rule %d produced %d candidate actions for %s (force=%v)", ruleID, len(actions), who, force)

	// The rule no longer matches: collect the stale action it left behind,
	// exactly as the full sweep does. Without this the single-rule path could
	// only ever ADD — asking for a re-evaluation of a rule that had stopped
	// matching did nothing at all, which is the opposite of what asking for it
	// implies. Actions already acted on (Acknowledged/InProgress/Done) are
	// preserved by the server-side filter.
	if len(actions) == 0 {
		if !g.shouldWithdraw(ruleID, len(actions)) {
			logger.Debugf("Rule %d is on-demand; leaving its actions for %s alone", ruleID, who)
			return 0, nil
		}
		if shared {
			g.withdrawShared(ruleID)
		} else {
			g.withdraw(owner, ruleID)
		}
		return 0, nil
	}

	// The rule's open action is updated, or the action raised; a forced
	// request raises it even while a Done one holds the rule back
	// (AG-EVAL-26).
	created, err := g.placeAction(owner, ruleID, firstAction(ruleID, actions)[0], force)
	if shared {
		g.consolidateShared(ruleID, primaryID)
	}
	if err != nil {
		return 0, err
	}
	if created {
		return 1, nil
	}
	return 0, nil
}

// countDeactivated counts the deactivated accounts among users.
func countDeactivated(users []systemmanager.User) int {
	n := 0
	for _, u := range users {
		if u.Deactivated() {
			n++
		}
	}
	return n
}

// processJSRules evaluates JS rules from all executors for all users, each
// user's in the order given (AG-EVAL-28).
func (g *Generator) processJSRules(unit *drain.Unit, users []systemmanager.User, executors map[int64]*jsruntime.JSRuleExecutor, order []int64) (int, error) {
	actionsCreated := 0
	pass := g.newPass()
	if n := countDeactivated(users); n > 0 {
		logger.Infof("Not evaluating %d deactivated users: their actions are left as they are (AG-EVAL-25)", n)
	}

	for i := range users {
		user := users[i]
		// A deactivated account is not evaluated: its actions stay as they
		// were until it is reactivated (AG-EVAL-25).
		if user.Deactivated() {
			logger.Debugf("Skipping deactivated user %d", user.ID)
			continue
		}

		// Evaluate each per-rule executor, from the highest priority down, so
		// that the action limit refuses the least important ones.
		for _, ruleID := range order {
			executor := executors[ruleID]
			if g.stopping() {
				logger.Info("Stopping: the rest of this pass is evaluated at the next start")
				return actionsCreated, nil
			}
			unit.Rename(fmt.Sprintf("rule %d for user %d", ruleID, user.ID))
			actions, unknown, err := g.evaluate(executor, pass, user.ID, false)
			if err != nil {
				logger.Errorf("Error evaluating JS rules for user %d (rule executor %d): %v", user.ID, ruleID, err)
				continue
			}
			if unknown {
				continue
			}

			// Rule did not match the user's current data. Garbage-collect any
			// New-status action this rule had previously created for the user —
			// it's stale. Actions the user already acted on (Acknowledged,
			// InProgress, Done) are preserved by the server-side filter.
			if len(actions) == 0 {
				// Except for an on-demand rule, whose normal answer this is.
				if !g.shouldWithdraw(ruleID, len(actions)) {
					continue
				}
				g.withdraw(user.ID, ruleID)
				continue
			}

			created, err := g.placeAction(user.ID, ruleID, firstAction(ruleID, actions)[0], false)
			if err != nil {
				logger.Errorf("%v", err)
				continue
			}
			if created {
				actionsCreated++
			}
		}
	}

	return actionsCreated, nil
}

// processSharedWorkspace evaluates every rule ONCE, for the primary owner, whose
// answers the managers give for the whole workspace (PA-BR-07), and creates the
// resulting actions under that owner. This is the Appliance-mode counterpart of processJSRules: the
// whole instance is one shared workspace, so one action set — not one per user —
// is correct. Owning everything under the primary user makes the existing
// per-(owner,rule) dedup collapse to one-per-rule automatically, and lets us
// consolidate away the per-user duplicates that earlier per-user generation left.
func (g *Generator) processSharedWorkspace(unit *drain.Unit, users []systemmanager.User, executors map[int64]*jsruntime.JSRuleExecutor, order []int64, primaryID int64) (int, error) {
	found := false
	for i := range users {
		if users[i].ID == primaryID {
			found = true
			break
		}
	}
	if !found {
		return 0, fmt.Errorf("primary user %d not found among users", primaryID)
	}

	// Evaluated for the primary owner; the managers answer with the
	// workspace's data (PA-BR-07).
	pass := g.newPass()
	actionsCreated := 0

	// From the highest priority down (AG-EVAL-28): the workspace shares one
	// action budget.
	for _, ruleID := range order {
		executor := executors[ruleID]
		if g.stopping() {
			logger.Info("Stopping: the rest of this pass is evaluated at the next start")
			return actionsCreated, nil
		}
		unit.Rename(fmt.Sprintf("rule %d for the workspace (owner %d)", ruleID, primaryID))
		actions, unknown, err := g.evaluate(executor, pass, primaryID, false)
		if err != nil {
			logger.Errorf("Error evaluating shared JS rule %d: %v", ruleID, err)
			continue
		}
		if unknown {
			continue
		}

		// Rule no longer matches the workspace's data — garbage-collect every
		// New-status action it previously created, for any owner.
		if len(actions) == 0 {
			// As in the per-user path: an on-demand rule answering "no actions"
			// is not the rule ceasing to match, it is the rule doing nothing
			// until asked.
			if !g.shouldWithdraw(ruleID, len(actions)) {
				continue
			}
			g.withdrawShared(ruleID)
			continue
		}

		// Placed under the primary owner, as one action for the workspace.
		if created, err := g.placeAction(primaryID, ruleID, firstAction(ruleID, actions)[0], false); err != nil {
			logger.Errorf("Shared workspace: %v", err)
		} else if created {
			actionsCreated++
		}

		g.consolidateShared(ruleID, primaryID)
	}

	return actionsCreated, nil
}
