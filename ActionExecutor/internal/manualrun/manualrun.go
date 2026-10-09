// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package manualrun carries out a user's request to run an execution rule now.
//
// The user presses Execute in the WebUI; AIManager checks them, stores the
// request and announces it. This package claims it — only one claim succeeds,
// so a request runs once even if it is announced twice or two instances hear
// it — runs the rule's routine on behalf of the user and role AIManager
// recorded, and reports the outcome back to the request, from where it reaches
// the user who asked. The run itself, and its last-execution report and audit
// entry, are package run's, exactly as for an automatic run.
//
// It replaced POST /api/execute, so that ActionExecutor receives its work as
// every other daemon does and serves no HTTP.
package manualrun

import (
	"errors"
	"fmt"

	"tachyonik/actionexecutor/internal/aimanager"
	"tachyonik/actionexecutor/internal/apibridge"
	"tachyonik/actionexecutor/internal/executionrulewatcher"
	"tachyonik/actionexecutor/internal/jsruntime"
	"tachyonik/actionexecutor/internal/runaudit"
	"tachyonik/lib/logger"
)

// Requests is AIManager's side of a request: list, claim, report.
type Requests interface {
	ListPendingExecutionRequests() ([]aimanager.ExecutionRequest, error)
	ClaimExecutionRequest(id int64) (*aimanager.ExecutionRequest, error)
	ReportExecutionRequest(id int64, done bool, outcome aimanager.ExecutionOutcome) error
}

// Runner runs one routine for one user and records it (package run).
type Runner interface {
	Execute(ruleID int64, rt *jsruntime.JSExecutorRuntime, auth apibridge.AuthContext, subject string) (*jsruntime.ExecResult, error)
}

// Rules are the loaded execution rules and their routines (package aiexecutor).
type Rules interface {
	GetRule(ruleID int64) *aimanager.ExecutionRule
	GetRuntime(ruleID int64) *jsruntime.JSExecutorRuntime
}

// Handler carries out run requests.
type Handler struct {
	requests Requests
	rules    Rules
	runner   Runner
}

// New creates a Handler.
func New(requests Requests, rules Rules, runner Runner) *Handler {
	return &Handler{requests: requests, rules: rules, runner: runner}
}

// OnRequested handles one announced request.
func (h *Handler) OnRequested(req executionrulewatcher.RunRequest) {
	h.handle(req.ID)
}

// PickUpPending runs every request still waiting — those made while this
// daemon was down or disconnected. Called on each (re)connect.
func (h *Handler) PickUpPending() {
	pending, err := h.requests.ListPendingExecutionRequests()
	if err != nil {
		logger.Warnf("manualrun: failed to list pending execution requests: %v", err)
		return
	}
	if len(pending) > 0 {
		logger.Infof("manualrun: %d execution request(s) waiting", len(pending))
	}
	for _, req := range pending {
		h.handle(req.ID)
	}
}

func (h *Handler) handle(id int64) {
	req, err := h.requests.ClaimExecutionRequest(id)
	if errors.Is(err, aimanager.ErrNotClaimable) {
		logger.Debugf("manualrun: request %d is no longer pending; skipped", id)
		return
	}
	if err != nil {
		logger.Errorf("manualrun: failed to claim execution request %d: %v", id, err)
		return
	}

	done, outcome := h.run(req)
	if err := h.requests.ReportExecutionRequest(req.ID, done, outcome); err != nil {
		logger.Errorf("manualrun: failed to report the outcome of request %d: %v", req.ID, err)
	}
}

// run carries out a claimed request and words its outcome: done when the
// routine produced a result — whatever status it reported — and not done when
// there was nothing to run or the run itself failed.
func (h *Handler) run(req *aimanager.ExecutionRequest) (bool, aimanager.ExecutionOutcome) {
	rule := h.rules.GetRule(req.ExecutionRuleID)
	if rule == nil {
		return false, failure(fmt.Sprintf("execution rule %d not found", req.ExecutionRuleID))
	}
	rt := h.rules.GetRuntime(req.ExecutionRuleID)
	if rt == nil {
		return false, failure(fmt.Sprintf("execution rule %d (%s) has no active routine", rule.ID, rule.Title))
	}

	logger.Infof("manualrun: running rule %d for user %d (request %d)", rule.ID, req.UserID, req.ID)
	auth := apibridge.AuthContext{UserID: req.UserID, UserRole: req.UserRole}
	result, err := h.runner.Execute(rule.ID, rt, auth, runaudit.ManualSubject(rule.Title, rule.ID))
	if err != nil {
		logger.Errorf("manualrun: rule %d for user %d failed: %v", rule.ID, req.UserID, err)
		return false, failure(err.Error())
	}

	outcome := aimanager.ExecutionOutcome{
		Success: result.Status != "error",
		Status:  result.Status,
		Message: result.Message,
		Changes: result.Changes,
	}
	if result.Status == "error" {
		outcome.Error = result.Message
		logger.Warnf("manualrun: rule %d for user %d returned status=error: %s", rule.ID, req.UserID, result.Message)
	}
	return true, outcome
}

func failure(reason string) aimanager.ExecutionOutcome {
	return aimanager.ExecutionOutcome{Success: false, Status: "error", Error: reason}
}
