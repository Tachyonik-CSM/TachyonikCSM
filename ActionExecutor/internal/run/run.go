// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package run carries out one run of an execution routine for one user, the
// same way whoever asked for it.
//
// A run is asked for manually (a request from AIManager, when a user presses
// Execute) or automatically (when a new action qualifies). Both used to do the same
// steps in their own copy — build the api object, run within the time limit,
// report the result to AIManager, write the audit entry — and a change to what
// surrounds a run had to be made twice. Now each caller keeps
// only what is its own: reporting to the request, or deleting the action.
package run

import (
	"context"
	"errors"
	"github.com/dop251/goja"

	"tachyonik/actionexecutor/internal/aimanager"
	"tachyonik/actionexecutor/internal/apibridge"
	"tachyonik/actionexecutor/internal/config"
	"tachyonik/actionexecutor/internal/jsruntime"
	"tachyonik/actionexecutor/internal/runaudit"
	libbridge "tachyonik/lib/apibridge"
	"tachyonik/lib/apiholder"
	"tachyonik/lib/logger"
)

// Reporter receives each run's result, which the WebUI shows as the rule's
// last execution. AIManager's client is one.
type Reporter interface {
	ReportLastExecution(ruleID int64, payload aimanager.ReportLastExecutionRequest) error
}

// AuditEmitter writes the audit-trail entry of a run. SystemManager's client
// is one.
type AuditEmitter interface {
	CreateAuditEvent(userID int64, level, module, message string) error
}

// Runner holds what every run needs. One is created at startup and shared.
type Runner struct {
	cfg      *config.Config
	bridge   *apibridge.Bridge
	reporter Reporter
	audit    AuditEmitter
	// catalogue is the platform API bridge bridge routines run on; nil:
	// hand-written-bridge routines only.
	catalogue *apiholder.Holder
}

// SetCatalogue gives the runner the platform API bridge.
func (r *Runner) SetCatalogue(c *apiholder.Holder) { r.catalogue = c }

// New creates a Runner. reporter and audit may be nil: the run still happens,
// only that part of its bookkeeping is skipped.
func New(cfg *config.Config, bridge *apibridge.Bridge, reporter *aimanager.Client, audit AuditEmitter) *Runner {
	r := &Runner{cfg: cfg, bridge: bridge, audit: audit}
	// A nil *Client in the interface would not compare equal to nil.
	if reporter != nil {
		r.reporter = reporter
	}
	return r
}

// Execute runs rt for the user auth names, under the configured time limit,
// and records the outcome: the result goes to AIManager and one audit entry
// names subject (see runaudit.ManualSubject / AutoSubject).
//
// The error is the run's own failure (Run could not produce a result); a
// routine that reports status "error" returns that result and no error.
func (r *Runner) Execute(ruleID int64, rt *jsruntime.JSExecutorRuntime, auth apibridge.AuthContext, subject string) (*jsruntime.ExecResult, error) {
	// The routine runs in a VM of its own; its api object is made for that VM.
	// The tool calls it makes are collected for the audit entry.
	var buildAPI func(vm *goja.Runtime) goja.Value
	var toolCalls func() []runaudit.ToolCall
	if rt.IsBridge() {
		// A bridge routine runs on the platform API bridge, read-write, for
		// the run's user and role (PA-BR-09). Its calls end with the run.
		var b *libbridge.Bridge
		if r.catalogue != nil {
			b, _ = r.catalogue.Bridge()
		}
		if b == nil {
			err := errors.New("the platform API catalogue is not loaded, so a bridge routine cannot run")
			r.report(ruleID, "error", err.Error(), nil)
			r.record(auth.UserID, subject, err, "", "", nil)
			return nil, err
		}
		pass := b.NewPass()
		if limit := r.cfg.ExecutionTimeout(); limit > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), limit)
			defer cancel()
			pass.SetContext(ctx)
		}
		buildAPI = func(vm *goja.Runtime) goja.Value {
			return pass.ObjectFor(vm, libbridge.Identity{UserID: auth.UserID, Role: auth.UserRole})
		}
		toolCalls = func() []runaudit.ToolCall { return apibridge.ToolCallsOf(pass.Calls()) }
	} else {
		tools := &apibridge.ToolCallLog{}
		buildAPI = func(vm *goja.Runtime) goja.Value { return r.bridge.BuildRecording(vm, auth, tools) }
		toolCalls = tools.Calls
	}

	result, err := rt.Run(buildAPI, r.cfg.ExecutionTimeout())
	if err != nil {
		r.report(ruleID, "error", err.Error(), nil)
		r.record(auth.UserID, subject, err, "", "", toolCalls())
		return nil, err
	}
	r.report(ruleID, result.Status, result.Message, result.Changes)
	r.record(auth.UserID, subject, nil, result.Status, result.Message, toolCalls())
	return result, nil
}

// RecordNotRun audits a run that could not start — no such rule, no active
// routine — so a request that came to nothing is on record too.
func (r *Runner) RecordNotRun(userID int64, subject, why string) {
	r.record(userID, subject, errors.New(why), "", "", nil)
}

// report is best-effort: the run has happened whether or not AIManager hears
// of it.
func (r *Runner) report(ruleID int64, status, message string, changes []map[string]interface{}) {
	if r.reporter == nil {
		return
	}
	if err := r.reporter.ReportLastExecution(ruleID, aimanager.ReportLastExecutionRequest{
		Status: status, Message: message, Changes: changes,
	}); err != nil {
		logger.Warnf("Failed to report the execution of rule %d to AIManager: %v", ruleID, err)
	}
}

func (r *Runner) record(userID int64, subject string, runErr error, status, message string, tools []runaudit.ToolCall) {
	if r.audit == nil || userID == 0 {
		return
	}
	level, text := runaudit.Entry(subject, runErr, status, message, tools)
	if err := r.audit.CreateAuditEvent(userID, level, "ActionExecutor", text); err != nil {
		logger.Warnf("audit event emit failed: %v", err)
	}
}
