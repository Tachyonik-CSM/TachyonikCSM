// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package aimanager is the client for the AIManager internal API. It supplies
// the action rules and their generated routines, the per-module AI assignment
// and system prompt, the AI provider entries a rule may name to override the
// module default, and the enabled tool rules and capabilities that the rule
// context exposes.
//
// The request mechanics live in TachyonikLib's restclient; what stays here is
// this service's wire types, its paths, and the status each call treats as
// success — creating a routine answers 201, not 200.
package aimanager

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"tachyonik/lib/restclient"
)

// AIEntry represents an AI provider entry returned by AIManager.
// Provider selects the wire protocol (anthropic | openai | google |
// mistral | ollama | manual). The chat-client factory dispatches on
// this — without it, all keyed providers were routed through the
// Anthropic client, which broke Mistral/OpenAI/Google calls.
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

// ActionRule represents a rule template from AIManager
type ActionRule struct {
	ID                  int64   `json:"id"`
	Title               string  `json:"title"`
	TriggerPrompt       string  `json:"triggerPrompt"`
	ActionType          string  `json:"actionType"`
	ActionMessage       string  `json:"actionMessage"`
	RulePrompt          string  `json:"rulePrompt"`
	ActionPriority      int     `json:"actionPriority"`
	AI                  *int64  `json:"ai"`
	ActiveRoutine       *int64  `json:"activeRoutine"`
	GenerateRequestedAt *string `json:"generateRequestedAt"`
	CreatedAt           string  `json:"createdAt"`
	ModifiedAt          string  `json:"modifiedAt"`
}

// ListActionRulesResponse represents the response from the action-rules endpoint
type ListActionRulesResponse struct {
	ActionRules []ActionRule `json:"actionRules"`
	Total       int          `json:"total"`
}

// GetActionRules fetches all action rules from AIManager's internal endpoint
func (c *Client) GetActionRules() ([]ActionRule, error) {
	var result ListActionRulesResponse
	if err := c.rc.JSON("GET", "/api/internal/action-rules", nil, &result, http.StatusOK); err != nil {
		return nil, err
	}
	return result.ActionRules, nil
}

// ModuleAISetting represents the per-module AI configuration from AIManager.
type ModuleAISetting struct {
	ModuleName    string   `json:"moduleName"`
	DefaultAIID   *int64   `json:"defaultAiId"`
	SystemPrompt  string   `json:"systemPrompt"`
	ActiveRoutine *int64   `json:"activeRoutine"`
	AI            *AIEntry `json:"ai,omitempty"`
}

// GetModuleAISetting fetches the AI assignment and system prompt for a module.
func (c *Client) GetModuleAISetting(moduleName string) (*ModuleAISetting, error) {
	var setting ModuleAISetting
	if err := c.rc.JSON("GET", "/api/internal/module-ai-settings/"+moduleName, nil, &setting, http.StatusOK); err != nil {
		return nil, err
	}
	return &setting, nil
}

// Routine represents a generated routine stored in AIManager
type Routine struct {
	ID          int64  `json:"id"`
	Code        string `json:"code"`
	Rule        int64  `json:"rule"`
	Type        string `json:"type"`
	Version     string `json:"version"`
	Model       string `json:"model"`
	SHA256      string `json:"sha256"`
	GeneratedAt string `json:"generatedAt"`
	Status      string `json:"status"` // "passed" or "failed"
	Log         string `json:"log"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// CreateRoutineRequest is the payload for creating a routine
type CreateRoutineRequest struct {
	Code        string `json:"code"`
	Rule        int64  `json:"rule"`
	Type        string `json:"type"`
	Version     string `json:"version"`
	Model       string `json:"model"`
	SHA256      string `json:"sha256"`
	GeneratedAt string `json:"generatedAt"`
	Status      string `json:"status"` // "passed" or "failed"
	Log         string `json:"log"`
}

// CreateRoutine stores a generated routine. Answers 201, not 200.
func (c *Client) CreateRoutine(payload CreateRoutineRequest) (*Routine, error) {
	var routine Routine
	if err := c.rc.JSON("POST", "/api/internal/routines", payload, &routine, http.StatusCreated); err != nil {
		return nil, err
	}
	return &routine, nil
}

// GetRoutine fetches one stored routine by ID.
func (c *Client) GetRoutine(id int64) (*Routine, error) {
	var routine Routine
	if err := c.rc.JSON("GET", fmt.Sprintf("/api/internal/routines/%d", id), nil, &routine, http.StatusOK); err != nil {
		return nil, err
	}
	return &routine, nil
}

// ClearGenerateRequest clears a rule's generate flag and records why the
// generation ended as it did. An empty reason means it succeeded.
func (c *Client) ClearGenerateRequest(ruleID int64, reason string) error {
	payload := map[string]interface{}{
		"clearGenerateRequest": true,
		"generateError":        reason,
	}
	return c.rc.JSON("PATCH", fmt.Sprintf("/api/internal/action-rules/%d", ruleID), payload, nil, http.StatusOK)
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

// GetAIByID fetches an AI provider entry by its ID.
func (c *Client) GetAIByID(id int64) (*AIEntry, error) {
	return c.getAI(fmt.Sprintf("/api/internal/ais/%d", id),
		fmt.Sprintf("AI with ID %d not found in AIManager", id))
}

// GetAIByName fetches an AI provider entry by name.
func (c *Client) GetAIByName(name string) (*AIEntry, error) {
	return c.getAI("/api/internal/ais/by-name/"+name,
		fmt.Sprintf("AI with name %q not found in AIManager", name))
}

// EnabledToolRule is the subset of tool_rule fields needed to map a
// user's tool overviews to the capabilities Tachyonik can actually
// drive (i.e. the "Automated Capabilities" column in
// Resources/Tools). Returned by GetEnabledToolRules.
//
// /api/internal/tool-rules already filters out disabled rules, so any
// entry here is genuinely driveable. ToolOverviewID is non-nil for
// rules created via the normal flow; nil only when a rule was created
// without an overview link (data bug).
type EnabledToolRule struct {
	ID                     int64   `json:"id"`
	ToolOverviewID         *int64  `json:"toolOverviewId"`
	AutomatedCapabilityIDs []int64 `json:"automatedCapabilityIds"`
}

// GetEnabledToolRules fetches the enabled tool rules.
func (c *Client) GetEnabledToolRules() ([]EnabledToolRule, error) {
	var result struct {
		Tools []EnabledToolRule `json:"tools"`
	}
	if err := c.rc.JSON("GET", "/api/internal/tool-rules", nil, &result, http.StatusOK); err != nil {
		return nil, err
	}
	return result.Tools, nil
}

// ToolOverviewEntry is the subset of tool_overview fields needed to
// map a user's tools to their "Manual Capabilities" (the overview's
// manualCapabilityIds list).
type ToolOverviewEntry struct {
	ID                  int64   `json:"id"`
	Name                string  `json:"name"`
	ManualCapabilityIDs []int64 `json:"manualCapabilityIds"`
}

// GetToolOverviews fetches the tool catalogue.
func (c *Client) GetToolOverviews() ([]ToolOverviewEntry, error) {
	var result struct {
		Overviews []ToolOverviewEntry `json:"overviews"`
	}
	if err := c.rc.JSON("GET", "/api/internal/tool-overviews", nil, &result, http.StatusOK); err != nil {
		return nil, err
	}
	return result.Overviews, nil
}

// ToolCapabilityEntry is the id+name mapping needed to render the
// "Manual Capabilities" set as names rather than IDs.
type ToolCapabilityEntry struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// GetToolCapabilities fetches the capability catalogue.
func (c *Client) GetToolCapabilities() ([]ToolCapabilityEntry, error) {
	var result struct {
		ToolCapabilities []ToolCapabilityEntry `json:"toolCapabilities"`
	}
	if err := c.rc.JSON("GET", "/api/internal/tool-capabilities", nil, &result, http.StatusOK); err != nil {
		return nil, err
	}
	return result.ToolCapabilities, nil
}

// CountPersonalAIs reports how many AI providers the user owns, which a rule
// reads to tell "has none" from "has some, none assigned".
func (c *Client) CountPersonalAIs(userID int64) (int, error) {
	var result struct {
		Count int `json:"count"`
	}
	if err := c.rc.JSON("GET", fmt.Sprintf("/api/internal/users/%d/ais/count", userID), nil, &result, http.StatusOK); err != nil {
		return 0, err
	}
	return result.Count, nil
}

// GetCapabilityNamesForToolRules resolves tool-rule IDs to capability names.
// An empty list is answered without a request: asking for the capabilities of
// nothing is not a call worth making.
func (c *Client) GetCapabilityNamesForToolRules(toolRuleIDs []int64) ([]string, error) {
	if len(toolRuleIDs) == 0 {
		return []string{}, nil
	}

	parts := make([]string, len(toolRuleIDs))
	for i, id := range toolRuleIDs {
		parts[i] = fmt.Sprintf("%d", id)
	}

	var result struct {
		Capabilities []string `json:"capabilities"`
	}
	path := "/api/internal/tool-rule-capabilities?ids=" + strings.Join(parts, ",")
	if err := c.rc.JSON("GET", path, nil, &result, http.StatusOK); err != nil {
		return nil, err
	}
	return result.Capabilities, nil
}
