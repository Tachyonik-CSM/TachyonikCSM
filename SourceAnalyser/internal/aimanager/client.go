// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package aimanager is an outbound HTTP client for the AIManager REST API. It
// fetches analysis rules, AI provider entries, and this module's AI settings,
// and creates and reads the generated analysis routines that AIManager stores.
// The request plumbing lives in internal/restclient; what remains here is the
// wire types and one line per endpoint.
package aimanager

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"tachyonik/sourceanalyser/internal/restclient"
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

// AnalysisRule represents an analysis rule from AIManager
type AnalysisRule struct {
	ID                  int64   `json:"id"`
	Description         string  `json:"description"`
	Type                string  `json:"type"`
	RulePrompt          string  `json:"rulePrompt"`
	ActiveRoutine       *int64  `json:"activeRoutine"`
	AI                  *int64  `json:"ai"` // nullable FK to ais.id; nil = use default AI
	GenerateRequestedAt *string `json:"generateRequestedAt"`
}

// AnalysisRulesResponse represents the list analysis rules API response
type AnalysisRulesResponse struct {
	AnalysisRules []AnalysisRule `json:"analysisRules"`
}

// GetAnalysisRules fetches all analysis rules from AIManager
func (c *Client) GetAnalysisRules() ([]AnalysisRule, error) {
	var rulesResp AnalysisRulesResponse
	if err := c.rc.JSON("GET", "/api/internal/analysis-rules", nil, &rulesResp, http.StatusOK); err != nil {
		return nil, err
	}
	return rulesResp.AnalysisRules, nil
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

// CreateRoutine stores a generated routine in AIManager.
// Returns the created routine (with its assigned ID).
func (c *Client) CreateRoutine(payload CreateRoutineRequest) (*Routine, error) {
	var routine Routine
	if err := c.rc.JSON("POST", "/api/internal/routines", payload, &routine, http.StatusCreated); err != nil {
		return nil, err
	}
	return &routine, nil
}

// GetRoutine fetches a single routine by ID from AIManager.
func (c *Client) GetRoutine(id int64) (*Routine, error) {
	var routine Routine
	path := fmt.Sprintf("/api/internal/routines/%d", id)
	if err := c.rc.JSON("GET", path, nil, &routine, http.StatusOK); err != nil {
		return nil, err
	}
	return &routine, nil
}

// ModuleAISetting represents the per-module AI configuration from AIManager.
type ModuleAISetting struct {
	ModuleName   string   `json:"moduleName"`
	DefaultAIID  *int64   `json:"defaultAiId"`
	SystemPrompt string   `json:"systemPrompt"`
	AI           *AIEntry `json:"ai,omitempty"`
}

// GetModuleAISetting fetches the AI setting for a given module via the internal API.
func (c *Client) GetModuleAISetting(moduleName string) (*ModuleAISetting, error) {
	var setting ModuleAISetting
	path := "/api/internal/module-ai-settings/" + url.PathEscape(moduleName)
	if err := c.rc.JSON("GET", path, nil, &setting, http.StatusOK); err != nil {
		return nil, err
	}
	return &setting, nil
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

// GetAIByID fetches an AI entry by ID from AIManager.
func (c *Client) GetAIByID(id int64) (*AIEntry, error) {
	return c.getAI(
		fmt.Sprintf("/api/internal/ais/%d", id),
		fmt.Sprintf("AI with ID %d not found in AIManager", id),
	)
}

// GetAIByName fetches an AI entry by exact name from AIManager. The name is an
// admin-chosen label, so it is escaped rather than interpolated raw — one
// containing '/', '?' or '#' would otherwise change which endpoint is called.
func (c *Client) GetAIByName(name string) (*AIEntry, error) {
	return c.getAI(
		"/api/internal/ais/by-name/"+url.PathEscape(name),
		fmt.Sprintf("AI '%s' not found in AIManager", name),
	)
}

// ClearGenerateRequest clears the generate_requested_at flag on an analysis rule
// via the internal PATCH endpoint.
func (c *Client) ClearGenerateRequest(ruleID int64) error {
	payload := map[string]any{"clearGenerateRequest": true}
	path := fmt.Sprintf("/api/internal/analysis-rules/%d", ruleID)
	return c.rc.JSON("PATCH", path, payload, nil, http.StatusOK)
}
