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
package generator

import (
	"fmt"
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
	executorMu    sync.RWMutex
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
	shared := false
	var primaryID int64
	if ws, werr := g.systemMgr.GetWorkspace(); werr != nil {
		logger.Warnf("Could not determine workspace mode, defaulting to per-user generation: %v", werr)
	} else if ws.IsAppliance() {
		if ws.PrimaryUserID > 0 {
			shared, primaryID = true, ws.PrimaryUserID
		} else {
			logger.Warn("Appliance mode but no primary user reported; falling back to per-user generation")
		}
	}

	actionsCreated := 0
	var created int
	if shared {
		logger.Infof("Appliance shared-workspace mode: generating one action set under user %d", primaryID)
		created, err = g.processSharedWorkspace(unit, users, executorSnapshot, primaryID)
	} else {
		created, err = g.processJSRules(unit, users, executorSnapshot)
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

// refreshHiddenOptions writes the freshly evaluated option set onto an action
// that already exists.
//
// Best-effort: the action is correct either way, and failing an evaluation pass
// over an option list would be a poor trade. ActionManager skips the write and
// the broadcast when the set is unchanged, which is the usual case.
func (g *Generator) refreshHiddenOptions(actionID int64, req actionmanager.CreateActionRequest) {
	var hidden []int64
	if req.HiddenOptions != nil {
		hidden = *req.HiddenOptions
	}
	if err := g.actionMgr.SetHiddenOptions(actionID, hidden); err != nil {
		logger.Warnf("Could not refresh the option set of action %d: %v", actionID, err)
	}
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
	// account SystemManager does not know would belong to nobody.
	if _, err := g.systemMgr.GetUser(userID); err != nil {
		return 0, fmt.Errorf("failed to get user %d: %w", userID, err)
	}

	actions, unknown, err := g.evaluate(executor, g.newPass(), userID, force)
	if err != nil {
		return 0, fmt.Errorf("error evaluating rule %d for user %d: %w", ruleID, userID, err)
	}
	if unknown {
		logger.Infof("Rule %d could not be decided for user %d; its actions are left as they are", ruleID, userID)
		return 0, nil
	}

	logger.Infof("Rule %d produced %d candidate actions for user %d (force=%v)", ruleID, len(actions), userID, force)

	// The rule no longer matches: collect the stale action it left behind,
	// exactly as the full sweep does. Without this the single-rule path could
	// only ever ADD — asking for a re-evaluation of a rule that had stopped
	// matching did nothing at all, which is the opposite of what asking for it
	// implies. Actions already acted on (Acknowledged/InProgress/Done) are
	// preserved by the server-side filter.
	if len(actions) == 0 {
		if !g.shouldWithdraw(ruleID, len(actions)) {
			logger.Debugf("Rule %d is on-demand; leaving its actions for user %d alone", ruleID, userID)
			return 0, nil
		}
		if n, derr := g.actionMgr.DeleteNewActionsByRule(userID, ruleID); derr != nil {
			logger.Warnf("Stale-action cleanup failed for user %d rule %d: %v", userID, ruleID, derr)
		} else if n > 0 {
			logger.Infof("Deleted %d stale action(s) for user %d rule %d (rule no longer matches)", n, userID, ruleID)
		}
		return 0, nil
	}

	actionsCreated := 0
	for _, actionReq := range actions {
		existingID, err := g.actionMgr.ExistingActionID(userID, actionReq.Title)
		if err != nil {
			logger.Errorf("Error checking if action exists for user %d: %v", userID, err)
			continue
		}
		if existingID != 0 {
			// The action stays as it is, but which of its options apply may
			// have changed since it was raised — the user may have installed
			// the tool the condition asks about. This is the refresh.
			g.refreshHiddenOptions(existingID, actionReq)
			logger.Infof("Skipping existing action '%s' for user %d (rule %d)", actionReq.Title, userID, ruleID)
			continue
		}

		actionReq.UserID = userID
		ruleIDCopy := ruleID
		actionReq.ActionRuleID = &ruleIDCopy

		action, err := g.actionMgr.CreateAction(actionReq)
		if err != nil {
			return actionsCreated, fmt.Errorf("failed to create action for user %d: %w", userID, err)
		}

		logger.Infof("Successfully created action ID %d for user %d via trigger", action.ID, userID)
		actionsCreated++
	}

	return actionsCreated, nil
}

// processJSRules evaluates JS rules from all executors for all users
func (g *Generator) processJSRules(unit *drain.Unit, users []systemmanager.User, executors map[int64]*jsruntime.JSRuleExecutor) (int, error) {
	actionsCreated := 0
	pass := g.newPass()

	for i := range users {
		user := users[i]

		// Evaluate each per-rule executor
		for ruleID, executor := range executors {
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
				if n, derr := g.actionMgr.DeleteNewActionsByRule(user.ID, ruleID); derr != nil {
					logger.Warnf("Stale-action cleanup failed for user %d rule %d: %v", user.ID, ruleID, derr)
				} else if n > 0 {
					logger.Infof("Deleted %d stale action(s) for user %d rule %d (rule no longer matches)", n, user.ID, ruleID)
				}
				continue
			}

			for _, actionReq := range actions {
				// Check if action already exists for this user
				existingID, err := g.actionMgr.ExistingActionID(user.ID, actionReq.Title)
				if err != nil {
					logger.Errorf("Error checking if action exists for user %d: %v", user.ID, err)
					continue
				}

				if existingID != 0 {
					g.refreshHiddenOptions(existingID, actionReq)
					logger.Debugf("Action '%s' already exists for user %d (%s), skipping",
						actionReq.Title, user.ID, user.Username)
					continue
				}

				logger.Infof("Creating action '%s' for user %d (%s) via JS rule",
					actionReq.Title, user.ID, user.Username)

				actionReq.UserID = user.ID
				ruleIDCopy := ruleID
				actionReq.ActionRuleID = &ruleIDCopy

				action, err := g.actionMgr.CreateAction(actionReq)
				if err != nil {
					logger.Errorf("Failed to create action for user %d: %v", user.ID, err)
					continue
				}

				logger.Infof("Successfully created action ID %d for user %d", action.ID, user.ID)
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
func (g *Generator) processSharedWorkspace(unit *drain.Unit, users []systemmanager.User, executors map[int64]*jsruntime.JSRuleExecutor, primaryID int64) (int, error) {
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

	for ruleID, executor := range executors {
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
			if n, derr := g.actionMgr.DeleteNewActionsByRuleExceptUser(ruleID, 0); derr != nil {
				logger.Warnf("Shared stale-action cleanup failed for rule %d: %v", ruleID, derr)
			} else if n > 0 {
				logger.Infof("Deleted %d shared stale action(s) for rule %d (rule no longer matches)", n, ruleID)
			}
			continue
		}

		for _, actionReq := range actions {
			existingID, err := g.actionMgr.ExistingActionID(primaryID, actionReq.Title)
			if err != nil {
				logger.Errorf("Error checking if shared action exists (rule %d): %v", ruleID, err)
				continue
			}
			if existingID != 0 {
				g.refreshHiddenOptions(existingID, actionReq)
				logger.Debugf("Shared action '%s' already exists under owner %d (rule %d), skipping", actionReq.Title, primaryID, ruleID)
				continue
			}
			actionReq.UserID = primaryID
			ruleIDCopy := ruleID
			actionReq.ActionRuleID = &ruleIDCopy
			action, err := g.actionMgr.CreateAction(actionReq)
			if err != nil {
				logger.Errorf("Failed to create shared action (rule %d): %v", ruleID, err)
				continue
			}
			logger.Infof("Created shared action ID %d (rule %d, owner %d)", action.ID, ruleID, primaryID)
			actionsCreated++
		}

		// Consolidate: drop any New-status copies of this rule owned by other
		// users (duplicates from prior per-user generation, or from a manual
		// single-user trigger), keeping only the primary owner's.
		if n, derr := g.actionMgr.DeleteNewActionsByRuleExceptUser(ruleID, primaryID); derr != nil {
			logger.Warnf("Shared consolidation failed for rule %d: %v", ruleID, derr)
		} else if n > 0 {
			logger.Infof("Consolidated %d duplicate action(s) for rule %d under owner %d", n, ruleID, primaryID)
		}
	}

	return actionsCreated, nil
}
