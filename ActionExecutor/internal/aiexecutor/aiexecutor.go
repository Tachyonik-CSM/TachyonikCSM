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
//
// Only bridge routines — `var routineContract = 2;`, run on the platform API
// bridge — are generated and loaded. An active routine that predates the
// platform API is held back when it is loaded, with the reason kept for the
// runs asked of its rule (HeldBack), and the system prompt must ask for the
// catalogue ({{API_CATALOGUE}}) before the AI is asked for a routine.
package aiexecutor

import (
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
	"tachyonik/lib/aicode"
	"tachyonik/lib/aipick"
	"tachyonik/lib/apiholder"
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
	// heldBack says, by rule, why its active routine is not loaded although
	// AIManager names one — so a run asked of the rule says so.
	heldBack map[int64]string
	mu       sync.RWMutex
	// catalogue is the platform API catalogue routines are generated,
	// validated and checked against; nil: none can be generated.
	catalogue *apiholder.Holder
}

// APICataloguePlaceholder is where the system prompt wants the catalogue's
// operations, read and write: replaced at generation (PA-RT-05). A prompt
// without it predates the platform API, and no routine is generated with it.
const APICataloguePlaceholder = "{{API_CATALOGUE}}"

// errPromptPredatesAPI fails a generation whose system prompt does not ask for
// the catalogue.
var errPromptPredatesAPI = errors.New("the ActionExecutor system prompt predates the platform API (it has no " +
	APICataloguePlaceholder + "): replace it with prompts/actionexecutor.txt in AIManager (AI → Modules → ActionExecutor)")

// SetCatalogue gives the executor the platform API catalogue.
func (ag *AIActionExecutor) SetCatalogue(c *apiholder.Holder) {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	ag.catalogue = c
}

// New creates a new AIActionExecutor.
func New(aiMgrClient *aimanager.Client, chatClient codegen.ChatClient, cfg *config.Config) *AIActionExecutor {
	return &AIActionExecutor{
		aiMgrClient: aiMgrClient,
		chatClient:  chatClient,
		cfg:         cfg,
		rules:       make(map[int64]*aimanager.ExecutionRule),
		runtimes:    make(map[int64]*jsruntime.JSExecutorRuntime),
		heldBack:    make(map[int64]string),
	}
}

// hold leaves the rule without a routine and keeps why. Caller holds ag.mu.
func (ag *AIActionExecutor) hold(ruleID int64, why string) {
	delete(ag.runtimes, ruleID)
	ag.heldBack[ruleID] = why
}

// HeldBack is why the rule's active routine is not loaded, or "" when it is,
// or when the rule has none.
func (ag *AIActionExecutor) HeldBack(ruleID int64) string {
	ag.mu.RLock()
	defer ag.mu.RUnlock()
	return ag.heldBack[ruleID]
}

// PredatesAPIMessage is why a routine without `var routineContract = 2;` is
// neither loaded nor run.
func PredatesAPIMessage(ruleID, routineID int64) string {
	return fmt.Sprintf("execution rule %d: routine %d predates the platform API, which this version no longer runs — generate the rule anew", ruleID, routineID)
}

// loadActiveRoutine fetches and loads the active routine for a rule into ag.runtimes.
// Caller must hold ag.mu lock.
//
// Whatever cannot be loaded leaves the rule with NO routine, not with the one it
// had: what runs must be what AIManager says is active. Keeping the previous
// routine when the newly activated one would not load meant an older routine
// went on running under a rule that no longer named it.
func (ag *AIActionExecutor) loadActiveRoutine(rule *aimanager.ExecutionRule) {
	delete(ag.heldBack, rule.ID)
	if rule.ActiveRoutine == nil {
		delete(ag.runtimes, rule.ID)
		logger.Debugf("Execution rule %d (%s) has no active routine", rule.ID, rule.Title)
		return
	}

	routine, err := ag.aiMgrClient.GetRoutine(*rule.ActiveRoutine)
	if err != nil {
		logger.Errorf("Failed to fetch active routine %d for execution rule %d (%s), the rule has no routine loaded: %v",
			*rule.ActiveRoutine, rule.ID, rule.Title, err)
		ag.hold(rule.ID, fmt.Sprintf("execution rule %d: its active routine %d could not be fetched from AIManager", rule.ID, *rule.ActiveRoutine))
		return
	}

	if routine.Status != "passed" {
		logger.Warnf("Active routine %d for execution rule %d has status '%s'; the rule has no routine loaded",
			routine.ID, rule.ID, routine.Status)
		ag.hold(rule.ID, fmt.Sprintf("execution rule %d: its active routine %d has status %q", rule.ID, routine.ID, routine.Status))
		return
	}

	// Run only the code that was generated and validated: a missing or wrong
	// checksum means the stored code is not that (aicode.VerifyChecksum).
	if err := aicode.VerifyChecksum(routine.Code, routine.SHA256); err != nil {
		logger.Errorf("Refusing routine %d for execution rule %d (%s): %v", routine.ID, rule.ID, rule.Title, err)
		ag.hold(rule.ID, fmt.Sprintf("execution rule %d: its active routine %d is refused: %v", rule.ID, routine.ID, err))
		return
	}

	rt := jsruntime.New()
	if err := rt.LoadFromString(routine.Code); err != nil {
		if errors.Is(err, jsruntime.ErrPredatesBridge) {
			why := PredatesAPIMessage(rule.ID, routine.ID)
			logger.Errorf("%s (%s)", why, rule.Title)
			ag.hold(rule.ID, why)
			return
		}
		logger.Errorf("Failed to load routine %d code for execution rule %d (%s), the rule has no routine loaded: %v",
			routine.ID, rule.ID, rule.Title, err)
		ag.hold(rule.ID, fmt.Sprintf("execution rule %d: its active routine %d does not load: %v", rule.ID, routine.ID, err))
		return
	}

	// A routine runs only while the catalogue still offers what it calls
	// (PA-RT-06).
	ops := routine.Operations()
	rt.SetOperations(ops)
	if ag.catalogue != nil && ag.catalogue.Loaded() {
		if missing := ag.catalogue.Missing(ops); len(missing) > 0 {
			logger.Errorf("Holding back routine %d of execution rule %d (%s): it calls %s, which the API catalogue no longer has — regenerate the routine",
				routine.ID, rule.ID, rule.Title, strings.Join(missing, ", "))
			ag.hold(rule.ID, fmt.Sprintf("execution rule %d: routine %d calls %s, which the platform API no longer has — generate the rule anew",
				rule.ID, routine.ID, strings.Join(missing, ", ")))
			return
		}
		if dep := ag.catalogue.Deprecated(ops); len(dep) > 0 {
			logger.Warnf("Routine %d of execution rule %d (%s) calls deprecated operations %s — regenerate it before they go",
				routine.ID, rule.ID, rule.Title, strings.Join(dep, ", "))
		}
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
	ag.heldBack = make(map[int64]string)

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
// generate JS, validate syntax, save to AIManager. It returns the stored routine
// when it passed; whether it becomes the active one is the caller's decision
// (the request's generateActivate), otherwise the operator picks it in the UI.
func (ag *AIActionExecutor) GenerateForRule(rule *aimanager.ExecutionRule) (*aimanager.Routine, error) {
	logger.Infof("Generating JS code for execution rule %d (%s)...", rule.ID, rule.Title)

	// Which AI, asked of AIManager now rather than remembered from startup:
	// the rule's own, or the module's current default. Named in every error
	// below, so a refused request says which account refused it.
	choice, err := ag.pickAI(rule.AI)
	if err != nil {
		logger.Warnf("Cannot generate routine for execution rule %d: %v", rule.ID, err)
		return nil, fmt.Errorf("cannot generate the routine for rule %d: %w", rule.ID, err)
	}
	ruleModel := choice.Entry.Model
	logger.Infof("Generating the routine for execution rule %d with %s", rule.ID, choice.Describe())

	// The system prompt gets the operations a routine may call, read and
	// write (PA-RT-05). One that does not ask for them describes the api
	// object this module no longer has: the AI is not asked.
	systemPrompt := choice.SystemPrompt
	if !strings.Contains(systemPrompt, APICataloguePlaceholder) {
		logger.Errorf("Cannot generate the routine for execution rule %d: %v", rule.ID, errPromptPredatesAPI)
		return nil, fmt.Errorf("cannot generate the routine for rule %d: %w", rule.ID, errPromptPredatesAPI)
	}
	ag.mu.RLock()
	cat := ag.catalogue
	ag.mu.RUnlock()
	if cat == nil || !cat.Loaded() {
		return nil, fmt.Errorf("cannot generate the routine for rule %d: the platform API catalogue is not loaded", rule.ID)
	}
	systemPrompt = strings.ReplaceAll(systemPrompt, APICataloguePlaceholder, cat.Render())
	codeGen := codegen.New(choice.Client, ruleModel, systemPrompt)

	code, err := codeGen.Generate(*rule)
	if err != nil {
		return nil, fmt.Errorf("code generation failed for rule %d with %s: %w", rule.ID, choice.Describe(), err)
	}

	sha256Hex := aicode.Checksum(code)

	version := fmt.Sprintf("v%s", time.Now().Format("20060102_150405"))

	tempRuntime := jsruntime.New()
	if err := tempRuntime.LoadFromString(code); errors.Is(err, jsruntime.ErrPredatesBridge) {
		ag.storeRoutine(code, rule.ID, version, ruleModel, sha256Hex, "failed", "Validation failed: "+errNotBridgeRoutine.Error())
		return nil, fmt.Errorf("validation failed for rule %d: %w", rule.ID, errNotBridgeRoutine)
	} else if err != nil {
		ag.storeRoutine(code, rule.ID, version, ruleModel, sha256Hex, "failed", fmt.Sprintf("Syntax validation failed: %v", err))
		return nil, fmt.Errorf("syntax validation failed for rule %d: %w", rule.ID, err)
	}

	if err := codegen.ValidateRuleID(tempRuntime.RuleIDs(), rule.ID); err != nil {
		ag.storeRoutine(code, rule.ID, version, ruleModel, sha256Hex, "failed", fmt.Sprintf("Rule ID validation failed: %v", err))
		return nil, fmt.Errorf("rule ID validation failed for rule %d: %w", rule.ID, err)
	}

	// The routine is run against the managers' mock answers first
	// (PA-VAL-02), and stored with what it calls (PA-RT-04).
	b, fingerprint := cat.Bridge()
	v := tempRuntime.ValidateBridge(b, ag.cfg.ExecutionTimeout())
	log, operations := v.Log(), v.Operations
	if len(v.Errors) > 0 {
		ag.storeRoutineAPI(code, rule.ID, version, ruleModel, sha256Hex, "failed", log, fingerprint, operations)
		return nil, fmt.Errorf("validation against the API's mock answers failed for rule %d with %d errors", rule.ID, len(v.Errors))
	}

	routine, err := ag.storeRoutineAPI(code, rule.ID, version, ruleModel, sha256Hex, "passed", log, fingerprint, operations)
	if err != nil {
		return nil, fmt.Errorf("failed to store routine for rule %d: %w", rule.ID, err)
	}

	logger.Infof("Successfully generated and saved JS for execution rule %d (%s), routine ID %d",
		rule.ID, rule.Title, routine.ID)
	return routine, nil
}

func (ag *AIActionExecutor) storeRoutine(code string, ruleID int64, version, model, sha256Hex, status, log string) (*aimanager.Routine, error) {
	return ag.storeRoutineAPI(code, ruleID, version, model, sha256Hex, status, log, "", nil)
}

// storeRoutineAPI stores a routine with what a bridge routine was generated
// against (PA-RT-04).
func (ag *AIActionExecutor) storeRoutineAPI(code string, ruleID int64, version, model, sha256Hex, status, log, fingerprint string, operations []string) (*aimanager.Routine, error) {
	routine, err := ag.aiMgrClient.CreateRoutine(aimanager.CreateRoutineRequest{
		APIFingerprint: fingerprint,
		APIOperations:  operations,
		Code:           code,
		Rule:           ruleID,
		Type:           "ActionExecutor",
		Version:        version,
		Model:          model,
		SHA256:         sha256Hex,
		GeneratedAt:    time.Now().Format(time.RFC3339),
		Status:         status,
		Log:            log,
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
			// The new routine becomes the active one only when the request
			// asked for it and the routine passed; a failed one never
			// replaces what is active.
			var activate int64
			routine, err := ag.GenerateForRule(targetRule)
			if err != nil {
				logger.Errorf("Failed to generate routine for execution rule %d: %v", event.RuleID, err)
				failure = err.Error()
			} else if targetRule.GenerateActivate && routine != nil {
				activate = routine.ID
				logger.Infof("Making routine %d the active routine of execution rule %d, as requested", routine.ID, event.RuleID)
			}
			// Clear the flag regardless of success/failure to avoid retry
			// loops, and record the reason so the UI can show it rather than
			// leaving it in this log.
			if err := ag.aiMgrClient.ClearGenerateRequest(event.RuleID, failure, activate); err != nil {
				logger.Errorf("Failed to clear generate request flag for execution rule %d: %v", event.RuleID, err)
			}
		} else {
			logger.Infof("Execution rule %d (%s) updated, no generation requested", event.RuleID, targetRule.Title)
		}

	case "deleted":
		ag.mu.Lock()
		delete(ag.rules, event.RuleID)
		delete(ag.runtimes, event.RuleID)
		delete(ag.heldBack, event.RuleID)
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

// errNotBridgeRoutine fails a generated routine that does not declare the
// routine contract.
var errNotBridgeRoutine = errors.New("the routine must begin with var routineContract = 2;")

// errNoAIManager is what a lookup reports when this module has no AIManager
// client to ask.
var errNoAIManager = errors.New("no AIManager client configured")

// pickEntry converts AIManager's record into aipick's.
func pickEntry(e *aimanager.AIEntry) *aipick.Entry {
	return &aipick.Entry{ID: e.ID, Name: e.Name, Provider: e.Provider, Model: e.Model, URL: e.URL, APIKey: e.APIKey}
}
