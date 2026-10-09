// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package aiexecutor owns the execution rules and the JavaScript routines
// generated from them. It loads the rules from AIManager, keeps one runtime per
// rule, and asks the configured AI to write a routine for a rule that has none —
// storing the result back in AIManager with its version, model and checksum.
//
// The rule watcher calls in here when a rule changes, so a running daemon picks
// up new and edited rules live.
package aiexecutor

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"tachyonik/actionexecutor/internal/aimanager"
	"tachyonik/actionexecutor/internal/codegen"
	"tachyonik/actionexecutor/internal/config"
	"tachyonik/actionexecutor/internal/executionrulewatcher"
	"tachyonik/actionexecutor/internal/jsruntime"
	"tachyonik/lib/aiclient"
	"tachyonik/lib/aipick"
	"tachyonik/lib/logger"
)

// AIActionExecutor orchestrates AI-powered execution rule code generation.
// It manages execution rules, generates JS code via AI per rule, and maintains
// per-rule JS executor runtimes that can be invoked on demand by the HTTP server.
type AIActionExecutor struct {
	aiMgrClient *aimanager.Client
	chatClient  codegen.ChatClient
	cfg         *config.Config
	rules       map[int64]*aimanager.ExecutionRule
	runtimes    map[int64]*jsruntime.JSExecutorRuntime
	mu          sync.RWMutex
}

// New creates a new AIActionExecutor.
func New(aiMgrClient *aimanager.Client, chatClient codegen.ChatClient, cfg *config.Config) *AIActionExecutor {
	return &AIActionExecutor{
		aiMgrClient: aiMgrClient,
		chatClient:  chatClient,
		cfg:         cfg,
		rules:       make(map[int64]*aimanager.ExecutionRule),
		runtimes:    make(map[int64]*jsruntime.JSExecutorRuntime),
	}
}

// loadActiveRoutine fetches and loads the active routine for a rule into ag.runtimes.
// Caller must hold ag.mu lock.
//
// Whatever cannot be loaded leaves the rule with NO routine, not with the one it
// had: what runs must be what AIManager says is active. Keeping the previous
// routine when the newly activated one would not load meant an older routine
// went on running under a rule that no longer named it.
func (ag *AIActionExecutor) loadActiveRoutine(rule *aimanager.ExecutionRule) {
	if rule.ActiveRoutine == nil {
		delete(ag.runtimes, rule.ID)
		logger.Debugf("Execution rule %d (%s) has no active routine", rule.ID, rule.Title)
		return
	}

	routine, err := ag.aiMgrClient.GetRoutine(*rule.ActiveRoutine)
	if err != nil {
		logger.Errorf("Failed to fetch active routine %d for execution rule %d (%s), the rule has no routine loaded: %v",
			*rule.ActiveRoutine, rule.ID, rule.Title, err)
		delete(ag.runtimes, rule.ID)
		return
	}

	if routine.Status != "passed" {
		logger.Warnf("Active routine %d for execution rule %d has status '%s'; the rule has no routine loaded",
			routine.ID, rule.ID, routine.Status)
		delete(ag.runtimes, rule.ID)
		return
	}

	rt := jsruntime.New()
	if err := rt.LoadFromString(routine.Code); err != nil {
		logger.Errorf("Failed to load routine %d code for execution rule %d (%s), the rule has no routine loaded: %v",
			routine.ID, rule.ID, rule.Title, err)
		delete(ag.runtimes, rule.ID)
		return
	}

	ag.runtimes[rule.ID] = rt
	logger.Infof("Loaded routine %d for execution rule %d (%s)", routine.ID, rule.ID, rule.Title)
}

// LoadRules fetches all execution rules from AIManager and loads the active routine for each.
func (ag *AIActionExecutor) LoadRules() error {
	rules, err := ag.aiMgrClient.GetExecutionRules()
	if err != nil {
		return fmt.Errorf("failed to fetch execution rules: %w", err)
	}

	ag.mu.Lock()
	defer ag.mu.Unlock()

	ag.rules = make(map[int64]*aimanager.ExecutionRule, len(rules))
	ag.runtimes = make(map[int64]*jsruntime.JSExecutorRuntime)

	for i := range rules {
		rule := &rules[i]
		ag.rules[rule.ID] = rule
		ag.loadActiveRoutine(rule)
	}

	logger.Infof("Loaded %d execution rules (%d with active routines)", len(rules), len(ag.runtimes))
	return nil
}

// GetRuntime returns the JS runtime for a rule, or nil if no active routine is loaded.
func (ag *AIActionExecutor) GetRuntime(ruleID int64) *jsruntime.JSExecutorRuntime {
	ag.mu.RLock()
	defer ag.mu.RUnlock()
	return ag.runtimes[ruleID]
}

// GetRule returns the execution rule for a rule ID, or nil if not loaded.
func (ag *AIActionExecutor) GetRule(ruleID int64) *aimanager.ExecutionRule {
	ag.mu.RLock()
	defer ag.mu.RUnlock()
	return ag.rules[ruleID]
}

// GenerateForRule runs the AI code generation pipeline for a single execution rule:
// generate JS, validate syntax, save to AIManager (operator activates manually via UI).
func (ag *AIActionExecutor) GenerateForRule(rule *aimanager.ExecutionRule) error {
	logger.Infof("Generating JS code for execution rule %d (%s)...", rule.ID, rule.Title)

	// Which AI, asked of AIManager now rather than remembered from startup:
	// the rule's own, or the module's current default. Named in every error
	// below, so a refused request says which account refused it.
	choice, err := ag.pickAI(rule.AI)
	if err != nil {
		logger.Warnf("Cannot generate routine for execution rule %d: %v", rule.ID, err)
		return fmt.Errorf("cannot generate the routine for rule %d: %w", rule.ID, err)
	}
	ruleModel := choice.Entry.Model
	logger.Infof("Generating the routine for execution rule %d with %s", rule.ID, choice.Describe())

	codeGen := codegen.New(choice.Client, ruleModel, choice.SystemPrompt)

	code, err := codeGen.Generate(*rule)
	if err != nil {
		return fmt.Errorf("code generation failed for rule %d with %s: %w", rule.ID, choice.Describe(), err)
	}

	hash := sha256.Sum256([]byte(code))
	sha256Hex := hex.EncodeToString(hash[:])

	version := fmt.Sprintf("v%s", time.Now().Format("20060102_150405"))

	tempRuntime := jsruntime.New()
	if err := tempRuntime.LoadFromString(code); err != nil {
		ag.storeRoutine(code, rule.ID, version, ruleModel, sha256Hex, "failed", fmt.Sprintf("Syntax validation failed: %v", err))
		return fmt.Errorf("syntax validation failed for rule %d: %w", rule.ID, err)
	}

	if err := codegen.ValidateRuleID(tempRuntime.RuleIDs(), rule.ID); err != nil {
		ag.storeRoutine(code, rule.ID, version, ruleModel, sha256Hex, "failed", fmt.Sprintf("Rule ID validation failed: %v", err))
		return fmt.Errorf("rule ID validation failed for rule %d: %w", rule.ID, err)
	}

	routine, err := ag.storeRoutine(code, rule.ID, version, ruleModel, sha256Hex, "passed", "")
	if err != nil {
		return fmt.Errorf("failed to store routine for rule %d: %w", rule.ID, err)
	}

	logger.Infof("Successfully generated and saved JS for execution rule %d (%s), routine ID %d (activate manually via UI)",
		rule.ID, rule.Title, routine.ID)
	return nil
}

func (ag *AIActionExecutor) storeRoutine(code string, ruleID int64, version, model, sha256Hex, status, log string) (*aimanager.Routine, error) {
	routine, err := ag.aiMgrClient.CreateRoutine(aimanager.CreateRoutineRequest{
		Code:        code,
		Rule:        ruleID,
		Type:        "ActionExecutor",
		Version:     version,
		Model:       model,
		SHA256:      sha256Hex,
		GeneratedAt: time.Now().Format(time.RFC3339),
		Status:      status,
		Log:         log,
	})
	if err != nil {
		logger.Errorf("Failed to store routine in AIManager: %v", err)
		return nil, err
	}
	return routine, nil
}

// HandleRuleChange is called by the execution rule watcher when a rule changes.
func (ag *AIActionExecutor) HandleRuleChange(event executionrulewatcher.RuleChangeEvent) {
	switch event.Type {
	case "created", "updated":
		rules, err := ag.aiMgrClient.GetExecutionRules()
		if err != nil {
			logger.Errorf("Failed to fetch execution rules after change event: %v", err)
			return
		}

		var targetRule *aimanager.ExecutionRule
		for i := range rules {
			if rules[i].ID == event.RuleID {
				targetRule = &rules[i]
				break
			}
		}

		if targetRule == nil {
			logger.Warnf("Execution rule %d not found after %s event", event.RuleID, event.Type)
			return
		}

		// Only the rule that changed: every rule's change is announced on
		// its own, and reloading all of them here fetched every active
		// routine again on each single edit. Startup and feed imports still
		// reload everything (LoadRules).
		ag.mu.Lock()
		ag.rules[targetRule.ID] = targetRule
		ag.loadActiveRoutine(targetRule)
		ag.mu.Unlock()

		// Only regenerate when explicitly requested via the generate_requested_at flag
		if targetRule.GenerateRequestedAt != nil {
			logger.Infof("Routine generation requested for execution rule %d (%s), generating...", event.RuleID, targetRule.Title)
			// GenerateForRule guards against a missing AI client internally
			// (including the per-rule AI case), so we only pre-check the
			// rule prompt here.
			// The rule prompt is OPTIONAL: it advises on variable
			// substitution for rules that use variables, and a rule without
			// one is perfectly legitimate. Code generation is given the whole
			// rule and never required this field — refusing on it meant the
			// button appeared to do nothing, with only a log line to say why.
			if strings.TrimSpace(targetRule.RulePrompt) == "" {
				logger.Infof("Execution rule %d (%s) has no rule prompt; generating from the rule alone", event.RuleID, targetRule.Title)
			}
			// What to report back. Empty means it worked.
			var failure string
			if err := ag.GenerateForRule(targetRule); err != nil {
				logger.Errorf("Failed to generate routine for execution rule %d: %v", event.RuleID, err)
				failure = err.Error()
			}
			// Clear the flag regardless of success/failure to avoid retry
			// loops, and record the reason so the UI can show it rather than
			// leaving it in this log.
			if err := ag.aiMgrClient.ClearGenerateRequest(event.RuleID, failure); err != nil {
				logger.Errorf("Failed to clear generate request flag for execution rule %d: %v", event.RuleID, err)
			}
		} else {
			logger.Infof("Execution rule %d (%s) updated, no generation requested", event.RuleID, targetRule.Title)
		}

	case "deleted":
		ag.mu.Lock()
		delete(ag.rules, event.RuleID)
		delete(ag.runtimes, event.RuleID)
		ag.mu.Unlock()

		logger.Infof("Removed execution rule %d from AI action executor", event.RuleID)
	}
}

// SetChatClient swaps the AI chat client used for code generation.
func (ag *AIActionExecutor) SetChatClient(client codegen.ChatClient) {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	ag.chatClient = client
}

// pickAI chooses the AI that generates a rule's routine, asking AIManager at
// the moment of generation; see tachyonik/lib/aipick. The AI this module
// loaded last is the fallback for when AIManager cannot be asked.
func (ag *AIActionExecutor) pickAI(ruleAI *int64) (aipick.Choice, error) {
	ag.mu.RLock()
	loaded := aipick.Choice{
		SystemPrompt: ag.cfg.AI.SystemPrompt,
		Entry:        aipick.Entry{Name: ag.cfg.AI.AIName, Model: ag.cfg.AI.Model},
	}
	if ag.chatClient != nil {
		loaded.Client = ag.chatClient
	}
	timeout := time.Duration(ag.cfg.AI.TimeoutSeconds) * time.Second
	ag.mu.RUnlock()

	lookup := aipick.Lookup{
		ByID: func(id int64) (*aipick.Entry, error) {
			if ag.aiMgrClient == nil {
				return nil, errNoAIManager
			}
			e, err := ag.aiMgrClient.GetAIByID(id)
			if err != nil {
				return nil, err
			}
			return pickEntry(e), nil
		},
		ModuleSetting: func() (*aipick.ModuleSetting, error) {
			if ag.aiMgrClient == nil {
				return nil, errNoAIManager
			}
			s, err := ag.aiMgrClient.GetModuleAISetting("actionexecutor")
			if err != nil {
				return nil, err
			}
			out := &aipick.ModuleSetting{SystemPrompt: s.SystemPrompt}
			if s.AI != nil {
				out.AI = pickEntry(s.AI)
			}
			return out, nil
		},
		// Every client this module makes is built here or at startup, both
		// with ai.timeout_seconds. There used to be three ways, with 300 s,
		// the configured value, and no timeout at all.
		Build: func(e aipick.Entry) aiclient.ChatClient {
			return aiclient.ForProvider(e.Provider, e.URL, e.APIKey, timeout)
		},
	}
	return lookup.Pick(ruleAI, loaded)
}

// errNoAIManager is what a lookup reports when this module has no AIManager
// client to ask.
var errNoAIManager = errors.New("no AIManager client configured")

// pickEntry converts AIManager's record into aipick's.
func pickEntry(e *aimanager.AIEntry) *aipick.Entry {
	return &aipick.Entry{ID: e.ID, Name: e.Name, Provider: e.Provider, Model: e.Model, URL: e.URL, APIKey: e.APIKey}
}
