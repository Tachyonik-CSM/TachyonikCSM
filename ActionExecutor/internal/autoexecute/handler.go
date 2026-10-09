// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package autoexecute runs an action the moment it is created, without waiting
// for the user to press anything.
//
// When the action watcher reports a new action, this decides whether it
// qualifies: its action rule has exactly one option, of type Execution, and the
// user has set that execution rule to Automatic. If so it hands the run to
// package run — which reports the outcome to AIManager and audits it either
// way — and deletes the action when the run succeeded, so the user never sees a
// task they had already chosen to have handled for them.
package autoexecute

import (
	"strconv"

	"tachyonik/actionexecutor/internal/actionwatcher"
	"tachyonik/actionexecutor/internal/aiexecutor"
	"tachyonik/actionexecutor/internal/aimanager"
	"tachyonik/actionexecutor/internal/apibridge"
	"tachyonik/actionexecutor/internal/config"
	"tachyonik/actionexecutor/internal/run"
	"tachyonik/actionexecutor/internal/runaudit"
	"tachyonik/lib/logger"
)

// Handler decides whether a newly created action should be auto-executed
// and performs the execution + deletion when conditions are met.
type Handler struct {
	aiMgrClient *aimanager.Client
	executor    *aiexecutor.AIActionExecutor
	runner      *run.Runner
	// bridge carries the handler's own two requests (the automation setting,
	// deleting the action), authenticated like every other call.
	bridge *apibridge.Bridge
	cfg    *config.Config
}

// New creates a new auto-execute handler.
func New(
	aiMgrClient *aimanager.Client,
	executor *aiexecutor.AIActionExecutor,
	runner *run.Runner,
	bridge *apibridge.Bridge,
	cfg *config.Config,
) *Handler {
	return &Handler{
		aiMgrClient: aiMgrClient,
		executor:    executor,
		runner:      runner,
		bridge:      bridge,
		cfg:         cfg,
	}
}

// OnActionCreated is called when an ACTION_CREATED event is received.
// It checks if the action qualifies for auto-execution and runs it if so.
// All errors are logged and cause early return — the action persists for manual handling.
func (h *Handler) OnActionCreated(action actionwatcher.Action) {
	if action.ActionRuleID == nil {
		return
	}

	ruleID := *action.ActionRuleID

	// Fetch action options for this rule from AIManager
	options, err := h.aiMgrClient.GetActionOptionsByRuleID(ruleID)
	if err != nil {
		logger.Errorf("autoexecute: failed to fetch action options for rule %d: %v", ruleID, err)
		return
	}

	// Must have exactly 1 option of type "Execution"
	if len(options) != 1 || options[0].Type != "Execution" {
		return
	}

	// Parse ExecutionRule ID from TypeContext
	executionRuleID, err := strconv.ParseInt(options[0].TypeContext, 10, 64)
	if err != nil {
		logger.Errorf("autoexecute: invalid typeContext %q for option %d: %v",
			options[0].TypeContext, options[0].ID, err)
		return
	}

	// Check user's automation setting from SystemManager
	isAutomatic, err := h.checkAutomationStatus(action.UserID, executionRuleID)
	if err != nil {
		logger.Errorf("autoexecute: failed to check automation status for user %d, rule %d: %v",
			action.UserID, executionRuleID, err)
		return
	}
	if !isAutomatic {
		return
	}

	// Get rule and runtime from executor
	rule := h.executor.GetRule(executionRuleID)
	if rule == nil {
		logger.Warnf("autoexecute: execution rule %d not loaded in executor", executionRuleID)
		return
	}

	runtime := h.executor.GetRuntime(executionRuleID)
	if runtime == nil {
		logger.Warnf("autoexecute: execution rule %d has no loaded runtime", executionRuleID)
		return
	}

	auth := h.asUser(action.UserID)

	logger.Infof("autoexecute: auto-executing rule %d for action %d (user %d)",
		executionRuleID, action.ID, action.UserID)

	result, err := h.runner.Execute(executionRuleID, runtime, auth, runaudit.AutoSubject(action.Title, action.ID, executionRuleID))
	if err != nil {
		logger.Errorf("autoexecute: execution failed for rule %d, action %d: %v",
			executionRuleID, action.ID, err)
		return
	}

	if result.Status == "error" {
		logger.Warnf("autoexecute: execution returned error for rule %d, action %d: %s",
			executionRuleID, action.ID, result.Message)
		return
	}

	// Delete the action via ActionManager API
	if err := h.deleteAction(action.ID, action.UserID); err != nil {
		logger.Errorf("autoexecute: failed to delete action %d after execution: %v",
			action.ID, err)
		return
	}

	logger.Infof("autoexecute: successfully auto-executed rule %d and deleted action %d",
		executionRuleID, action.ID)
}

// userExecutionRuleSetting mirrors SystemManager's model for JSON decoding.
type userExecutionRuleSetting struct {
	ExecutionRuleID int64  `json:"executionRuleId"`
	Status          string `json:"status"`
}

// checkAutomationStatus queries SystemManager for the user's automation setting
// for a specific execution rule. Returns true if status is "automatic".
// Missing rows default to "manual" per SystemManager convention.
func (h *Handler) checkAutomationStatus(userID, executionRuleID int64) (bool, error) {
	var result struct {
		Settings []userExecutionRuleSetting `json:"settings"`
	}
	if err := h.bridge.Call("GET", h.cfg.SystemManager.URL, "/api/user-execution-rule-settings",
		h.cfg.SystemManager.InternalServiceKey, h.asUser(userID), nil, &result); err != nil {
		return false, err
	}
	for _, s := range result.Settings {
		if s.ExecutionRuleID == executionRuleID {
			return s.Status == "automatic", nil
		}
	}
	// No setting found — defaults to "manual"
	return false, nil
}

// deleteAction calls ActionManager's DELETE /api/actions endpoint.
func (h *Handler) deleteAction(actionID, userID int64) error {
	return h.bridge.Call("DELETE", h.cfg.ActionManager.URL, "/api/actions",
		h.cfg.ActionManager.InternalServiceKey, h.asUser(userID),
		map[string]interface{}{"ids": []int64{actionID}}, nil)
}

// asUser is the auth of an automatic run: the service key, on behalf of the
// action's owner. There is no user token, nobody having pressed anything.
func (h *Handler) asUser(userID int64) apibridge.AuthContext {
	return apibridge.AuthContext{UserID: userID}
}
