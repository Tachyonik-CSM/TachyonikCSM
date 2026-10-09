// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package aimanager is the client for the AIManager internal API. It supplies
// the execution rules and their generated routines, the per-module AI assignment
// and system prompt, the AI provider entries a rule may name to override the
// module default, and the options offered for an action rule. It is also where
// the outcome of each execution is reported back.
//
// The request mechanics — joining the path, the service key, the status check,
// decoding — are TachyonikLib's restclient. What stays here is this service's
// wire types and one line per endpoint, as in ActionGenerator's client.
package aimanager

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"tachyonik/lib/restclient"
)

// AIEntry represents an AI provider entry returned by AIManager.
// Provider selects the wire protocol (anthropic | openai | google |
// mistral | ollama | manual). The chat client is chosen by it
// (aiclient.ForProvider) — without it, all keyed providers were routed
// through the Anthropic client, which broke Mistral/OpenAI/Google calls.
type AIEntry struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	URL      string `json:"url"`
	APIKey   string `json:"apiKey"`
}

// Client is an HTTP client for the AIManager internal API.
type Client struct {
	rc *restclient.Client
}

// NewClient creates a new AIManager client.
func NewClient(baseURL, serviceKey string) *Client {
	return &Client{rc: restclient.New(baseURL, serviceKey, 5*time.Second)}
}

// ExecutionRule represents an execution rule template from AIManager.
//
// AI is the optional per-rule AI override (FK to ais.id). When non-nil,
// code generation for this rule should resolve the AI by ID and use its
// chat client + model instead of the module default. The recorded
// routine.Model must reflect whichever was actually used.
type ExecutionRule struct {
	ID                  int64   `json:"id"`
	Title               string  `json:"title"`
	RulePrompt          string  `json:"rulePrompt"`
	AI                  *int64  `json:"ai"`
	ActiveRoutine       *int64  `json:"activeRoutine"`
	GenerateRequestedAt *string `json:"generateRequestedAt"`
	CreatedAt           string  `json:"createdAt"`
	ModifiedAt          string  `json:"modifiedAt"`
}

// ListExecutionRulesResponse represents the response from the execution-rules endpoint.
type ListExecutionRulesResponse struct {
	ExecutionRules []ExecutionRule `json:"executionRules"`
	Total          int             `json:"total"`
}

// ReportLastExecutionRequest is the payload for the last-execution PATCH endpoint.
type ReportLastExecutionRequest struct {
	Status  string                   `json:"status"`
	Message string                   `json:"message"`
	Changes []map[string]interface{} `json:"changes,omitempty"`
}

// ModuleAISetting represents the per-module AI configuration from AIManager.
type ModuleAISetting struct {
	ModuleName    string   `json:"moduleName"`
	DefaultAIID   *int64   `json:"defaultAiId"`
	SystemPrompt  string   `json:"systemPrompt"`
	ActiveRoutine *int64   `json:"activeRoutine"`
	AI            *AIEntry `json:"ai,omitempty"`
}

// Routine represents a generated routine stored in AIManager.
type Routine struct {
	ID          int64  `json:"id"`
	Code        string `json:"code"`
	Rule        int64  `json:"rule"`
	Type        string `json:"type"`
	Version     string `json:"version"`
	Model       string `json:"model"`
	SHA256      string `json:"sha256"`
	GeneratedAt string `json:"generatedAt"`
	Status      string `json:"status"`
	Log         string `json:"log"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// CreateRoutineRequest is the payload for creating a routine.
type CreateRoutineRequest struct {
	Code        string `json:"code"`
	Rule        int64  `json:"rule"`
	Type        string `json:"type"`
	Version     string `json:"version"`
	Model       string `json:"model"`
	SHA256      string `json:"sha256"`
	GeneratedAt string `json:"generatedAt"`
	Status      string `json:"status"`
	Log         string `json:"log"`
}

// ActionOption represents an action option from AIManager.
type ActionOption struct {
	ID           int64  `json:"id"`
	ActionRuleID int64  `json:"actionRuleId"`
	Title        string `json:"title"`
	Type         string `json:"type"`        // "Execution", "Dialog", "Action", "View"
	TypeContext  string `json:"typeContext"` // ExecutionRule ID for type="Execution"
}

// listActionOptionsResponse is the response shape from the internal action-options endpoint.
type listActionOptionsResponse struct {
	ActionOptions []ActionOption `json:"actionOptions"`
	Total         int            `json:"total"`
}

// GetExecutionRules fetches all execution rules from AIManager via the internal endpoint.
func (c *Client) GetExecutionRules() ([]ExecutionRule, error) {
	var result ListExecutionRulesResponse
	if err := c.rc.JSON("GET", "/api/internal/execution-rules", nil, &result, http.StatusOK); err != nil {
		return nil, err
	}
	return result.ExecutionRules, nil
}

// ReportLastExecution updates the last execution result for an execution rule.
// Failures here are non-fatal — the execution itself has already completed by
// the time this is called.
func (c *Client) ReportLastExecution(ruleID int64, payload ReportLastExecutionRequest) error {
	return c.rc.JSON("PATCH", fmt.Sprintf("/api/internal/execution-rules/%d/last-execution", ruleID), payload, nil, http.StatusOK)
}

// GetModuleAISetting fetches the AI setting for a given module.
func (c *Client) GetModuleAISetting(moduleName string) (*ModuleAISetting, error) {
	var setting ModuleAISetting
	if err := c.rc.JSON("GET", "/api/internal/module-ai-settings/"+moduleName, nil, &setting, http.StatusOK); err != nil {
		return nil, err
	}
	return &setting, nil
}

// CreateRoutine stores a generated routine in AIManager.
func (c *Client) CreateRoutine(payload CreateRoutineRequest) (*Routine, error) {
	var routine Routine
	if err := c.rc.JSON("POST", "/api/internal/routines", payload, &routine, http.StatusCreated); err != nil {
		return nil, err
	}
	return &routine, nil
}

// GetRoutine fetches a single routine by ID.
func (c *Client) GetRoutine(id int64) (*Routine, error) {
	var routine Routine
	if err := c.rc.JSON("GET", fmt.Sprintf("/api/internal/routines/%d", id), nil, &routine, http.StatusOK); err != nil {
		return nil, err
	}
	return &routine, nil
}

// getAI fetches an AI entry, replacing a 404 with notFoundMsg so each caller's
// "which AI was missing" wording survives the shared request path.
func (c *Client) getAI(path, notFoundMsg string) (*AIEntry, error) {
	var entry AIEntry
	if err := c.rc.JSON("GET", path, nil, &entry, http.StatusOK); err != nil {
		var statusErr *restclient.StatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusNotFound {
			return nil, errors.New(notFoundMsg)
		}
		return nil, err
	}
	return &entry, nil
}

// GetAIByID fetches an AI entry by ID.
func (c *Client) GetAIByID(id int64) (*AIEntry, error) {
	return c.getAI(fmt.Sprintf("/api/internal/ais/%d", id),
		fmt.Sprintf("AI with ID %d not found in AIManager", id))
}

// GetAIByName fetches an AI entry by exact name.
func (c *Client) GetAIByName(name string) (*AIEntry, error) {
	// Escaped: a name is free text, and one holding "/" or "?" would
	// otherwise change which route — or which query — the request reaches.
	return c.getAI("/api/internal/ais/by-name/"+url.PathEscape(name),
		fmt.Sprintf("AI '%s' not found in AIManager", name))
}

// GetActionOptionsByRuleID fetches the action options of one action rule.
func (c *Client) GetActionOptionsByRuleID(actionRuleID int64) ([]ActionOption, error) {
	var result listActionOptionsResponse
	if err := c.rc.JSON("GET", fmt.Sprintf("/api/internal/action-options?actionRuleId=%d", actionRuleID), nil, &result, http.StatusOK); err != nil {
		return nil, err
	}
	return result.ActionOptions, nil
}

// ClearGenerateRequest clears the pending flag and records WHY the
// generation failed — an empty reason meaning it succeeded.
//
// The reason used to go only to this daemon's log, where the person who
// pressed the button never saw it: a failed generation and a successful
// one looked identical in the UI.
func (c *Client) ClearGenerateRequest(ruleID int64, reason string) error {
	payload := map[string]interface{}{
		"clearGenerateRequest": true,
		"generateError":        reason,
	}
	return c.rc.JSON("PATCH", fmt.Sprintf("/api/internal/execution-rules/%d", ruleID), payload, nil, http.StatusOK)
}

// ExecutionRequest is a user's request to run an execution rule now, as
// AIManager stores it. ActionExecutor claims it, runs the rule's routine on
// behalf of UserID with UserRole (which AIManager checked when the user asked),
// and reports the outcome.
type ExecutionRequest struct {
	ID              int64  `json:"id"`
	ExecutionRuleID int64  `json:"executionRuleId"`
	UserID          int64  `json:"userId"`
	UserRole        string `json:"userRole"`
	Status          string `json:"status"`
}

// ExecutionOutcome is a finished run as the WebUI shows it.
type ExecutionOutcome struct {
	Success bool                     `json:"success"`
	Status  string                   `json:"status,omitempty"`
	Message string                   `json:"message,omitempty"`
	Changes []map[string]interface{} `json:"changes,omitempty"`
	Error   string                   `json:"error,omitempty"`
}

// ErrNotClaimable is ClaimExecutionRequest's answer for a request that is no
// longer pending: another instance took it, or it finished or expired.
var ErrNotClaimable = errors.New("execution request is no longer pending")

// ListPendingExecutionRequests returns the requests still waiting to be run.
func (c *Client) ListPendingExecutionRequests() ([]ExecutionRequest, error) {
	var result struct {
		ExecutionRequests []ExecutionRequest `json:"executionRequests"`
	}
	if err := c.rc.JSON("GET", "/api/internal/execution-requests", nil, &result, http.StatusOK); err != nil {
		return nil, err
	}
	return result.ExecutionRequests, nil
}

// ClaimExecutionRequest takes a pending request for running. ErrNotClaimable
// when it is no longer pending, so a request is never run twice.
func (c *Client) ClaimExecutionRequest(id int64) (*ExecutionRequest, error) {
	var req ExecutionRequest
	if err := c.rc.JSON("POST", fmt.Sprintf("/api/internal/execution-requests/%d/claim", id), nil, &req, http.StatusOK); err != nil {
		var statusErr *restclient.StatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusConflict {
			return nil, ErrNotClaimable
		}
		return nil, err
	}
	return &req, nil
}

// ReportExecutionRequest stores a claimed request's outcome: done when the
// routine produced a result (whatever its own status), failed when the run
// itself could not.
func (c *Client) ReportExecutionRequest(id int64, done bool, outcome ExecutionOutcome) error {
	status := "failed"
	if done {
		status = "done"
	}
	body := map[string]interface{}{"status": status, "result": outcome}
	return c.rc.JSON("POST", fmt.Sprintf("/api/internal/execution-requests/%d/result", id), body, nil, http.StatusOK)
}
