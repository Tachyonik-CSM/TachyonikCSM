// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package aianalyser orchestrates per-rule AI code generation and execution.
// It loads analysis rules from AIManager, generates a JavaScript routine for
// each rule via an AI chat client, validates the routine (syntax plus a
// mock-context runtime check) and stores it back in AIManager, then runs the
// generated routines to identify the format of uploaded source files. Loading
// and executing already-generated ("passed") routines is pure JS and needs no
// AI — only generation requires a chat client.
package aianalyser

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"tachyonik/lib/aimwatcher"
	"tachyonik/lib/logger"
	"tachyonik/sourceanalyser/internal/aimanager"
	"tachyonik/sourceanalyser/internal/codegen"
	"tachyonik/sourceanalyser/internal/config"
	"tachyonik/sourceanalyser/internal/jsruntime"
)

// ChatClientFactory creates a ChatClient from an AIEntry. Returns nil if the
// entry cannot be used (e.g. unreachable provider).
type ChatClientFactory func(entry *aimanager.AIEntry) codegen.ChatClient

// AIAnalyser orchestrates per-rule AI code generation and execution.
// It manages analysis rules, generates JS code via AI, stores routines
// in AIManager, and executes generated code to identify uploaded source files.
type AIAnalyser struct {
	aiMgrClient       *aimanager.Client
	chatClient        codegen.ChatClient
	chatClientFactory ChatClientFactory
	cfg               *config.Config
	rules             map[int64]*aimanager.AnalysisRule
	executors         map[int64]*jsruntime.JSRuleExecutor
	mu                sync.RWMutex
}

// New creates a new AIAnalyser
func New(aiMgrClient *aimanager.Client, chatClient codegen.ChatClient, cfg *config.Config) *AIAnalyser {
	return &AIAnalyser{
		aiMgrClient: aiMgrClient,
		chatClient:  chatClient,
		cfg:         cfg,
		rules:       make(map[int64]*aimanager.AnalysisRule),
		executors:   make(map[int64]*jsruntime.JSRuleExecutor),
	}
}

// SetChatClientFactory sets the factory used to create per-rule AI chat clients.
func (a *AIAnalyser) SetChatClientFactory(factory ChatClientFactory) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.chatClientFactory = factory
}

// LoadRules fetches all analysis rules from AIManager. For each rule that has
// an active_routine set, it fetches the routine code from SystemManager and loads it.
func (a *AIAnalyser) LoadRules() error {
	rules, err := a.aiMgrClient.GetAnalysisRules()
	if err != nil {
		return fmt.Errorf("failed to fetch analysis rules: %w", err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.rules = make(map[int64]*aimanager.AnalysisRule, len(rules))

	for i := range rules {
		rule := &rules[i]
		a.rules[rule.ID] = rule
		a.loadExecutorForRuleLocked(rule)
	}

	// Per-rule load summary, so "did my routines even load?" is answerable
	// from a single place at startup. Guarded to skip the iteration entirely
	// when DEBUG is off.
	if logger.GetLevel() <= logger.DEBUG {
		for _, rule := range a.rules {
			if _, ok := a.executors[rule.ID]; ok {
				logger.Debugf("  rule %d (%s): executor loaded", rule.ID, rule.Description)
			} else if rule.ActiveRoutine == nil {
				logger.Debugf("  rule %d (%s): no executor (no active routine)", rule.ID, rule.Description)
			} else {
				logger.Debugf("  rule %d (%s): no executor (active routine %d not 'passed' or failed to load)",
					rule.ID, rule.Description, *rule.ActiveRoutine)
			}
		}
	}

	logger.Infof("Loaded %d analysis rules (%d with active routines)", len(rules), len(a.executors))
	return nil
}

// loadExecutorForRuleLocked (re)loads a.executors[rule.ID] to match the rule's
// current active_routine: it loads the active routine's code when that routine
// is "passed", and otherwise removes any stale executor. This keeps the running
// executor in sync with what the admin has activated. Callers MUST hold a.mu.
//
// Note: this performs a network fetch (GetRoutine) while holding the lock, which
// matches the pre-existing LoadRules behaviour.
func (a *AIAnalyser) loadExecutorForRuleLocked(rule *aimanager.AnalysisRule) {
	if rule.ActiveRoutine == nil {
		if _, existed := a.executors[rule.ID]; existed {
			delete(a.executors, rule.ID)
			logger.Infof("Analysis rule %d (%s) has no active routine, executor removed", rule.ID, rule.Description)
		} else {
			logger.Debugf("Analysis rule %d (%s) has no active routine", rule.ID, rule.Description)
		}
		return
	}

	routine, err := a.aiMgrClient.GetRoutine(*rule.ActiveRoutine)
	if err != nil {
		logger.Errorf("Failed to fetch active routine %d for rule %d (%s): %v",
			*rule.ActiveRoutine, rule.ID, rule.Description, err)
		return
	}

	if routine.Status != "passed" {
		logger.Warnf("Active routine %d for rule %d has status '%s', removing executor",
			routine.ID, rule.ID, routine.Status)
		delete(a.executors, rule.ID)
		return
	}

	executor := jsruntime.New(time.Duration(a.cfg.Analyzer.JSExecTimeoutSeconds) * time.Second)
	if err := executor.LoadFromString(routine.Code); err != nil {
		logger.Errorf("Failed to load routine %d code for rule %d (%s): %v",
			routine.ID, rule.ID, rule.Description, err)
		return
	}

	a.executors[rule.ID] = executor
	logger.Infof("Loaded routine %d for analysis rule %d (%s)", routine.ID, rule.ID, rule.Description)
}

// resolveRuleAI returns the ChatClient and model to use for code generation.
// If the rule has a per-rule AI configured and a factory is available, it uses that;
// otherwise it falls back to the default client and model.
func (a *AIAnalyser) resolveRuleAI(rule *aimanager.AnalysisRule) (codegen.ChatClient, string) {
	if rule.AI != nil && a.chatClientFactory != nil {
		entry, err := a.aiMgrClient.GetAIByID(*rule.AI)
		if err != nil {
			logger.Warnf("Failed to fetch AI %d for rule %d, falling back to default: %v", *rule.AI, rule.ID, err)
			return a.chatClient, a.cfg.AI.Model
		}
		client := a.chatClientFactory(entry)
		if client != nil {
			logger.Infof("Using per-rule AI '%s' (model: %s) for rule %d", entry.Name, entry.Model, rule.ID)
			return client, entry.Model
		}
		logger.Warnf("Per-rule AI '%s' for rule %d could not be created, falling back to default", entry.Name, rule.ID)
	}
	return a.chatClient, a.cfg.AI.Model
}

// GenerateForRule runs the AI code generation pipeline for a single analysis rule:
// generate JS, validate syntax and runtime, store in AIManager.
func (a *AIAnalyser) GenerateForRule(rule *aimanager.AnalysisRule) error {
	logger.Infof("Generating JS code for analysis rule %d (%s)...", rule.ID, rule.Description)

	// Resolve AI: use per-rule AI if configured, otherwise default
	ruleClient, ruleModel := a.resolveRuleAI(rule)

	// Generation requires an AI chat client. When no AI is configured the
	// module default client is nil; without a usable per-rule client there is
	// nothing to generate with. Skip rather than calling Chat on a nil client
	// (which would panic). Existing routines keep running regardless — only
	// generation is disabled until an AI is assigned.
	if ruleClient == nil {
		logger.Warnf("Cannot generate routine for analysis rule %d (%s): no AI configured", rule.ID, rule.Description)
		return fmt.Errorf("no AI configured for rule %d", rule.ID)
	}

	// Create code generator with resolved AI config
	codeGen := codegen.New(ruleClient, ruleModel, a.cfg.AI.SystemPrompt)

	// Convert to codegen.AnalysisRule
	codegenRule := codegen.AnalysisRule{
		ID:          rule.ID,
		Description: rule.Description,
		Type:        rule.Type,
		RulePrompt:  rule.RulePrompt,
	}

	// Generate JS code
	code, err := codeGen.Generate(codegenRule)
	if err != nil {
		return fmt.Errorf("code generation failed for rule %d: %w", rule.ID, err)
	}

	// Compute SHA256
	hash := sha256.Sum256([]byte(code))
	sha256Hex := hex.EncodeToString(hash[:])

	now := time.Now()
	version := fmt.Sprintf("v%s", now.Format("20060102_150405"))

	// Validate syntax by loading into a JS runtime
	tempExecutor := jsruntime.New(time.Duration(a.cfg.Analyzer.JSExecTimeoutSeconds) * time.Second)
	if err := tempExecutor.LoadFromString(code); err != nil {
		// Store as failed routine
		a.storeRoutine(code, rule.ID, version, ruleModel, sha256Hex, now, "failed", fmt.Sprintf("Syntax validation failed: %v", err))
		return fmt.Errorf("syntax validation failed for rule %d: %w", rule.ID, err)
	}

	// Validate runtime behavior with mock contexts
	validationErrors := tempExecutor.ValidateWithMockCtx()
	if len(validationErrors) > 0 {
		combinedErr := fmt.Errorf("runtime validation failed with %d errors", len(validationErrors))
		var logMsg string
		for i, e := range validationErrors {
			logger.Errorf("  Validation error %d for rule %d: %v", i+1, rule.ID, e)
			logMsg += fmt.Sprintf("Error %d: %v\n", i+1, e)
		}
		a.storeRoutine(code, rule.ID, version, ruleModel, sha256Hex, now, "failed", logMsg)
		return combinedErr
	}

	// Store as passed routine in AIManager
	_, err = a.storeRoutine(code, rule.ID, version, ruleModel, sha256Hex, now, "passed", "")
	if err != nil {
		return fmt.Errorf("failed to store routine for rule %d: %w", rule.ID, err)
	}

	// Swap executor in map
	a.mu.Lock()
	a.executors[rule.ID] = tempExecutor
	a.mu.Unlock()

	logger.Infof("Successfully generated and saved routine for analysis rule %d (%s)", rule.ID, rule.Description)
	return nil
}

// storeRoutine saves a generated routine to AIManager via API.
// Returns the created routine on success, or nil and logs the error.
func (a *AIAnalyser) storeRoutine(code string, ruleID int64, version, model, sha256Hex string, generatedAt time.Time, status, logMsg string) (*aimanager.Routine, error) {
	routine, err := a.aiMgrClient.CreateRoutine(aimanager.CreateRoutineRequest{
		Code:        code,
		Rule:        ruleID,
		Type:        "SourceAnalyser",
		Version:     version,
		Model:       model,
		SHA256:      sha256Hex,
		GeneratedAt: generatedAt.Format(time.RFC3339),
		Status:      status,
		Log:         logMsg,
	})
	if err != nil {
		logger.Errorf("Failed to store %s routine in AIManager for rule %d: %v", status, ruleID, err)
		return nil, err
	}

	logger.Infof("Stored %s routine %d in AIManager for rule %d (version: %s)", status, routine.ID, ruleID, version)
	return routine, nil
}

// HasExecutor returns true if JS code has been generated and loaded for the given rule ID.
func (a *AIAnalyser) HasExecutor(ruleID int64) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, exists := a.executors[ruleID]
	return exists
}

// Rules returns a copy of the rules map for iteration.
func (a *AIAnalyser) Rules() map[int64]*aimanager.AnalysisRule {
	a.mu.RLock()
	defer a.mu.RUnlock()

	result := make(map[int64]*aimanager.AnalysisRule, len(a.rules))
	for k, v := range a.rules {
		result[k] = v
	}
	return result
}

// AnalyzeSource runs all loaded rule executors against the context in sorted rule-ID order
// and returns the first non-null match.
func (a *AIAnalyser) AnalyzeSource(ctx jsruntime.RuleContext) (*jsruntime.AnalysisResult, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	fileSize := "nil"
	if ctx.FileSize != nil {
		fileSize = fmt.Sprintf("%d", *ctx.FileSize)
	}
	logger.Debugf("AnalyzeSource: file=%q contentLen=%d fileSize=%s — evaluating %d executor(s) over %d known rule(s)",
		ctx.Filename, len(ctx.Content), fileSize, len(a.executors), len(a.rules))
	logger.Debugf("AnalyzeSource: content preview (first 200B) for %q: %q",
		ctx.Filename, ctx.Content[:min(200, len(ctx.Content))])

	if len(a.executors) == 0 {
		logger.Debugf("AnalyzeSource: no executors loaded (rules may lack a passed active_routine) — %q will be Unsupported", ctx.Filename)
		return nil, nil
	}

	// Sort rule IDs for deterministic evaluation order
	ids := make([]int64, 0, len(a.executors))
	for id := range a.executors {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	for _, id := range ids {
		executor := a.executors[id]
		result, err := executor.AnalyzeSource(ctx)
		if err != nil {
			logger.Errorf("JS rule executor error for rule %d: %v", id, err)
			continue
		}
		if result != nil {
			return result, nil
		}
	}

	return nil, nil
}

// AnalyzeSourceWithRoutine classifies ctx using ONLY the given routine,
// bypassing the loaded active-routine set. It fetches the routine's code from
// AIManager, builds a throwaway executor, and returns its first match (or nil).
// This backs the WebUI's single-routine test: it lets a specific — possibly
// draft, not-yet-active — routine be exercised against a real file (including
// PDF text extraction) without affecting or depending on the deployed pipeline.
func (a *AIAnalyser) AnalyzeSourceWithRoutine(ctx jsruntime.RuleContext, routineID int64) (*jsruntime.AnalysisResult, error) {
	routine, err := a.aiMgrClient.GetRoutine(routineID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch test routine %d: %w", routineID, err)
	}

	executor := jsruntime.New(time.Duration(a.cfg.Analyzer.JSExecTimeoutSeconds) * time.Second)
	if err := executor.LoadFromString(routine.Code); err != nil {
		return nil, fmt.Errorf("failed to load test routine %d code: %w", routineID, err)
	}

	return executor.AnalyzeSource(ctx)
}

// HandleRuleChange is called by the analysis rule watcher when a rule changes.
func (a *AIAnalyser) HandleRuleChange(event aimwatcher.RuleChangeEvent) {
	switch event.Type {
	case "created", "updated":
		// Fetch latest rules from ResourceManager
		rules, err := a.aiMgrClient.GetAnalysisRules()
		if err != nil {
			logger.Errorf("Failed to fetch analysis rules after change event: %v", err)
			return
		}

		// Find the specific rule
		var targetRule *aimanager.AnalysisRule
		for i := range rules {
			if rules[i].ID == event.RuleID {
				targetRule = &rules[i]
				break
			}
		}

		if targetRule == nil {
			logger.Warnf("Analysis rule %d not found after %s event", event.RuleID, event.Type)
			return
		}

		// Update maps and sync the executor to the rule's active routine, so
		// activating (or switching) a routine takes effect immediately without
		// a daemon restart.
		a.mu.Lock()
		a.rules[targetRule.ID] = targetRule
		a.loadExecutorForRuleLocked(targetRule)
		a.mu.Unlock()

		// Only regenerate when explicitly requested via the generate_requested_at flag
		if targetRule.GenerateRequestedAt != nil {
			logger.Infof("Routine generation requested for analysis rule %d (%s), generating...", event.RuleID, targetRule.Type)
			if strings.TrimSpace(targetRule.RulePrompt) == "" {
				logger.Warnf("Cannot generate routine for analysis rule %d (%s): rule prompt is empty", event.RuleID, targetRule.Type)
			} else if err := a.GenerateForRule(targetRule); err != nil {
				logger.Errorf("Failed to generate routine for analysis rule %d: %v", event.RuleID, err)
			}
			// Clear the flag regardless of success/failure to avoid retry loops
			if err := a.aiMgrClient.ClearGenerateRequest(event.RuleID); err != nil {
				logger.Errorf("Failed to clear generate request flag for analysis rule %d: %v", event.RuleID, err)
			}
		} else {
			logger.Infof("Analysis rule %d (%s) updated, executor synced to active routine", event.RuleID, targetRule.Type)
		}

	case "deleted":
		a.mu.Lock()
		delete(a.rules, event.RuleID)
		delete(a.executors, event.RuleID)
		a.mu.Unlock()

		logger.Infof("Removed analysis rule %d from AI analyser", event.RuleID)
	}
}

// SetChatClient swaps the AI chat client used for code generation.
func (a *AIAnalyser) SetChatClient(client codegen.ChatClient) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.chatClient = client
}

// RuleCount returns the total number of loaded executors.
func (a *AIAnalyser) RuleCount() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.executors)
}
