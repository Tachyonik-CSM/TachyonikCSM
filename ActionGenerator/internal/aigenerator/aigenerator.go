// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package aigenerator owns the action rules and the JavaScript routines
// generated from them. It loads the rules from AIManager, keeps one executor per
// rule, and asks the configured AI to write a routine for a rule that has none —
// storing the result back in AIManager with its version, model and checksum.
//
// A generated routine is not trusted on arrival: it must name exactly the rule
// it was generated for, and it must pass evaluation against mock contexts before
// being marked usable. Nor is it trusted on the way back in — the stored
// checksum is verified when a routine is loaded, so code that changed in the
// database since it was validated is refused rather than run. The rule watcher calls in here when a rule changes, so a
// running daemon picks up new and edited rules live.
package aigenerator

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"tachyonik/actiongenerator/internal/actionrulewatcher"
	"tachyonik/actiongenerator/internal/aimanager"
	"tachyonik/actiongenerator/internal/codegen"
	"tachyonik/actiongenerator/internal/config"
	"tachyonik/actiongenerator/internal/jsruntime"
	"tachyonik/lib/aiclient"
	"tachyonik/lib/aipick"
	"tachyonik/lib/logger"
)

// ChatClientFactory creates a ChatClient from an AIEntry. Returns nil if the
// entry cannot be used (e.g. unreachable provider).
type ChatClientFactory func(entry *aimanager.AIEntry) codegen.ChatClient

// AIActionGenerator orchestrates AI-powered action rule code generation.
// It manages action rules, generates JS code via AI per rule, and maintains
// per-rule JS executors for independent lifecycle management.
type AIActionGenerator struct {
	aiMgrClient       *aimanager.Client
	chatClient        codegen.ChatClient
	chatClientFactory ChatClientFactory
	cfg               *config.Config
	rules             map[int64]*aimanager.ActionRule     // by ID
	executors         map[int64]*jsruntime.JSRuleExecutor // by rule ID
	mu                sync.RWMutex
}

// New creates a new AIActionGenerator.
func New(aiMgrClient *aimanager.Client, chatClient codegen.ChatClient, cfg *config.Config) *AIActionGenerator {
	return &AIActionGenerator{
		aiMgrClient: aiMgrClient,
		chatClient:  chatClient,
		cfg:         cfg,
		rules:       make(map[int64]*aimanager.ActionRule),
		executors:   make(map[int64]*jsruntime.JSRuleExecutor),
	}
}

// newExecutor builds a rule executor with this installation's JS execution
// budget. Routine code is AI-generated, so every execution is bounded; the
// budget lives in config precisely so an operator can raise it for a slow
// installation without a rebuild.
func (ag *AIActionGenerator) newExecutor() *jsruntime.JSRuleExecutor {
	return jsruntime.New(time.Duration(ag.cfg.AI.JSExecTimeoutSeconds) * time.Second)
}

// loadActiveRoutine fetches and loads the active routine for a rule into ag.executors.
// Caller must hold ag.mu lock.
func (ag *AIActionGenerator) loadActiveRoutine(rule *aimanager.ActionRule) {
	if rule.ActiveRoutine == nil {
		// Remove any previously loaded executor for this rule
		delete(ag.executors, rule.ID)
		logger.Debugf("Action rule %d (%s) has no active routine", rule.ID, rule.Title)
		return
	}

	routine, err := ag.aiMgrClient.GetRoutine(*rule.ActiveRoutine)
	if err != nil {
		logger.Errorf("Failed to fetch active routine %d for action rule %d (%s): %v",
			*rule.ActiveRoutine, rule.ID, rule.Title, err)
		return
	}

	if routine.Status != "passed" {
		logger.Warnf("Active routine %d for action rule %d has status '%s', skipping",
			routine.ID, rule.ID, routine.Status)
		return
	}

	// The stored hash is what says this is the code that was generated and
	// validated. Nothing else on the path checks it: AIManager hands back
	// whatever its routines table holds, and this daemon executes it. Anyone
	// able to write that row already has the database, so this is depth rather
	// than a boundary — but the hash is computed and stored at generation time
	// and was never read, which made it decoration.
	if err := verifyRoutineHash(routine); err != nil {
		logger.Errorf("Refusing routine %d for action rule %d (%s): %v",
			routine.ID, rule.ID, rule.Title, err)
		return
	}

	executor := ag.newExecutor()
	if err := executor.LoadFromString(routine.Code); err != nil {
		logger.Errorf("Failed to load routine %d code for action rule %d (%s): %v",
			routine.ID, rule.ID, rule.Title, err)
		return
	}

	ag.executors[rule.ID] = executor
	logger.Infof("Loaded routine %d for action rule %d (%s)", routine.ID, rule.ID, rule.Title)
	logger.Debugf("Routine %d source for rule %d:\n%s", routine.ID, rule.ID, routine.Code)
}

// LoadRules fetches all action rules from AIManager, builds lookup maps,
// and loads the active routine for each rule that has one.
func (ag *AIActionGenerator) LoadRules() error {
	rules, err := ag.aiMgrClient.GetActionRules()
	if err != nil {
		return fmt.Errorf("failed to fetch action rules: %w", err)
	}

	ag.mu.Lock()
	defer ag.mu.Unlock()

	ag.rules = make(map[int64]*aimanager.ActionRule, len(rules))
	ag.executors = make(map[int64]*jsruntime.JSRuleExecutor)

	for i := range rules {
		rule := &rules[i]
		ag.rules[rule.ID] = rule
		ag.loadActiveRoutine(rule)
	}

	logger.Infof("Loaded %d action rules (%d with active routines)", len(rules), len(ag.executors))
	return nil
}

// Executors returns a snapshot copy of the executor map for the generator to evaluate.
// OnDemandRules returns the ids of the rules that fire only when something asks
// for them by name — those with no trigger prompt.
//
// An empty trigger is not an omission, it is the statement that the rule has no
// condition of its own: the system prompt has the model write `check()` as a
// plain `return false` for it, and the only way such a rule ever produces an
// action is a forced evaluation, which is what an action option of type "Action"
// requests.
//
// Two things have to know this, and neither can work it out from the routine.
// The generator must not withdraw the actions of such a rule when an unforced
// evaluation finds no match — for these rules that is every evaluation, so the
// action would be swept within seconds of being asked for. And validation must
// not reject `return false` as a condition that could never fire, which for an
// ordinary rule is a real fault and here is the specification.
func (ag *AIActionGenerator) OnDemandRules() map[int64]bool {
	ag.mu.RLock()
	defer ag.mu.RUnlock()

	result := make(map[int64]bool, len(ag.rules))
	for id, rule := range ag.rules {
		if strings.TrimSpace(rule.TriggerPrompt) == "" {
			result[id] = true
		}
	}
	return result
}

func (ag *AIActionGenerator) Executors() map[int64]*jsruntime.JSRuleExecutor {
	ag.mu.RLock()
	defer ag.mu.RUnlock()

	result := make(map[int64]*jsruntime.JSRuleExecutor, len(ag.executors))
	for k, v := range ag.executors {
		result[k] = v
	}
	return result
}

// optionsForPrompt reduces AIManager's options to what the code generator needs:
// the id to key the generated function by, the title so the model can tell the
// options apart, and this rule's condition for it.
//
// Only options that carry a condition FOR THIS RULE are included. One without is
// always shown, and asking the model to write a function returning true for it
// would be a function that can only be wrong.
func optionsForPrompt(ruleID int64, options []aimanager.ActionOption) []aimanager.ActionRuleOption {
	var out []aimanager.ActionRuleOption
	for _, opt := range options {
		for _, link := range opt.Links {
			if link.ActionRuleID != ruleID || strings.TrimSpace(link.VisibilityPrompt) == "" {
				continue
			}
			out = append(out, aimanager.ActionRuleOption{
				ID:               opt.ID,
				Title:            opt.Title,
				VisibilityPrompt: link.VisibilityPrompt,
			})
		}
	}
	return out
}

// GenerateForRule runs the AI code generation pipeline for a single action rule:
// generate JS, validate syntax and runtime, save to AIManager, set active routine.
func (ag *AIActionGenerator) GenerateForRule(rule *aimanager.ActionRule) error {
	logger.Infof("Generating JS code for action rule %d (%s)...", rule.ID, rule.Title)

	// Which AI, asked of AIManager now rather than remembered from startup:
	// the rule's own, or the module's current default. Named in every error
	// below, so a refused request says which account refused it.
	choice, err := ag.pickAI(rule.AI)
	if err != nil {
		logger.Warnf("Cannot generate routine for action rule %d: %v", rule.ID, err)
		return fmt.Errorf("cannot generate the routine for rule %d: %w", rule.ID, err)
	}
	ruleModel := choice.Entry.Model
	logger.Infof("Generating the routine for action rule %d with %s", rule.ID, choice.Describe())

	codeGen := codegen.New(choice.Client, ruleModel, choice.SystemPrompt)

	// The options this rule offers, with the condition each is offered under.
	// They go into the prompt so the generated routine can decide per option
	// whether it applies — the conditions are plain language, and this is the
	// only point at which they are turned into code.
	//
	// Best-effort: a rule whose options cannot be read still generates, and
	// every option then applies, which is the behaviour without conditions.
	ruleWithOptions := *rule
	if options, oerr := ag.aiMgrClient.GetActionOptionsByRuleID(rule.ID); oerr != nil {
		logger.Warnf("Could not read the options of action rule %d, generating without conditions: %v", rule.ID, oerr)
	} else {
		ruleWithOptions.Options = optionsForPrompt(rule.ID, options)
	}

	// Generate JS code
	code, err := codeGen.Generate(ruleWithOptions)
	if err != nil {
		return fmt.Errorf("code generation failed for rule %d with %s: %w", rule.ID, choice.Describe(), err)
	}

	// Compute SHA256
	hash := sha256.Sum256([]byte(code))
	sha256Hex := hex.EncodeToString(hash[:])

	version := fmt.Sprintf("v%s", time.Now().Format("20060102_150405"))

	// Validate syntax by loading into a JS runtime
	tempExecutor := ag.newExecutor()
	if err := tempExecutor.LoadFromString(code); err != nil {
		ag.storeRoutine(code, rule.ID, version, ruleModel, sha256Hex, "failed", fmt.Sprintf("Syntax validation failed: %v", err))
		return fmt.Errorf("syntax validation failed for rule %d: %w", rule.ID, err)
	}

	// Validate rule ID matches
	if err := codegen.ValidateRuleID(tempExecutor.RuleIDs(), rule.ID); err != nil {
		ag.storeRoutine(code, rule.ID, version, ruleModel, sha256Hex, "failed", fmt.Sprintf("Rule ID validation failed: %v", err))
		return fmt.Errorf("rule ID validation failed for rule %d: %w", rule.ID, err)
	}

	// A rule with no trigger of its own is judged differently: its check() is
	// meant to return false everywhere, and validation would otherwise refuse
	// the only routine such a rule can have.
	tempExecutor.SetOnDemand(strings.TrimSpace(rule.TriggerPrompt) == "")

	// Validate runtime behavior with mock contexts. The report's log goes into
	// the routine either way: for a failure it says why, and for a pass it
	// records what each scenario decided and which fields were read, so a
	// routine that never fires can be understood without re-running anything.
	report := tempExecutor.ValidateWithMockCtxReport()
	for _, note := range report.Notes {
		logger.Infof("  Validation note for rule %d: %s", rule.ID, note)
	}
	if len(report.Errors) > 0 {
		combinedErr := fmt.Errorf("runtime validation failed with %d errors", len(report.Errors))
		for i, e := range report.Errors {
			logger.Errorf("  Validation error %d for rule %d: %v", i+1, rule.ID, e)
		}
		ag.storeRoutine(code, rule.ID, version, ruleModel, sha256Hex, "failed", report.Log())
		return combinedErr
	}

	// Store as passed routine in AIManager
	routine, err := ag.storeRoutine(code, rule.ID, version, ruleModel, sha256Hex, "passed", report.Log())
	if err != nil {
		return fmt.Errorf("failed to store routine for rule %d: %w", rule.ID, err)
	}

	logger.Infof("Successfully generated and saved JS for action rule %d (%s), routine ID %d (activate manually via UI)", rule.ID, rule.Title, routine.ID)
	return nil
}

// storeRoutine creates a routine in AIManager with the given parameters.
func (ag *AIActionGenerator) storeRoutine(code string, ruleID int64, version, model, sha256Hex, status, log string) (*aimanager.Routine, error) {
	routine, err := ag.aiMgrClient.CreateRoutine(aimanager.CreateRoutineRequest{
		Code:        code,
		Rule:        ruleID,
		Type:        "ActionGenerator",
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

// HandleRuleChange is called by the action rule watcher when a rule changes.
func (ag *AIActionGenerator) HandleRuleChange(event actionrulewatcher.RuleChangeEvent) {
	switch event.Type {
	case "created", "updated":
		// Fetch latest rules from AIManager
		rules, err := ag.aiMgrClient.GetActionRules()
		if err != nil {
			logger.Errorf("Failed to fetch action rules after change event: %v", err)
			return
		}

		// Find the specific rule
		var targetRule *aimanager.ActionRule
		for i := range rules {
			if rules[i].ID == event.RuleID {
				targetRule = &rules[i]
				break
			}
		}

		if targetRule == nil {
			logger.Warnf("Action rule %d not found after %s event", event.RuleID, event.Type)
			return
		}

		// Update rules map and reload active routines for all rules
		ag.mu.Lock()
		for i := range rules {
			rule := &rules[i]
			ag.rules[rule.ID] = rule
			ag.loadActiveRoutine(rule)
		}
		ag.mu.Unlock()

		// Check if routine generation was explicitly requested via the generate_requested_at flag
		if targetRule.GenerateRequestedAt != nil {
			logger.Infof("Routine generation requested for rule %d (%s), generating...", event.RuleID, targetRule.Title)
			// GenerateForRule guards against a missing AI client internally
			// (including the per-rule AI case), so we only pre-check the
			// rule prompt here.
			// The rule prompt is OPTIONAL: it advises on variable
			// substitution for rules that use variables, and a rule without
			// one is perfectly legitimate. Code generation is given the whole
			// rule and never required this field — refusing on it meant the
			// button appeared to do nothing, with only a log line to say why.
			if strings.TrimSpace(targetRule.RulePrompt) == "" {
				logger.Infof("Rule %d (%s) has no rule prompt; generating from the rule alone", event.RuleID, targetRule.Title)
			}
			// What to report back. Empty means it worked.
			var failure string
			if err := ag.GenerateForRule(targetRule); err != nil {
				logger.Errorf("Failed to generate routine for rule %d: %v", event.RuleID, err)
				failure = err.Error()
			}
			// Clear the flag regardless of success/failure to avoid retry
			// loops, and record the reason so the UI can show it rather than
			// leaving it in this log.
			if err := ag.aiMgrClient.ClearGenerateRequest(event.RuleID, failure); err != nil {
				logger.Errorf("Failed to clear generate request flag for rule %d: %v", event.RuleID, err)
			}
		} else {
			logger.Infof("Action rule %d (%s) updated, reloaded active routines", event.RuleID, targetRule.Title)
		}

	case "deleted":
		ag.mu.Lock()
		delete(ag.rules, event.RuleID)
		delete(ag.executors, event.RuleID)
		ag.mu.Unlock()

		logger.Infof("Removed action rule %d from AI action generator", event.RuleID)
	}
}

// SetChatClient swaps the AI chat client used for code generation.
func (ag *AIActionGenerator) SetChatClient(client codegen.ChatClient) {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	ag.chatClient = client
}

// SetChatClientFactory sets the factory used to create per-rule AI chat clients.
func (ag *AIActionGenerator) SetChatClientFactory(factory ChatClientFactory) {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	ag.chatClientFactory = factory
}

// pickAI chooses the AI that generates a rule's routine, asking AIManager at
// the moment of generation; see tachyonik/lib/aipick. The AI this module
// loaded last is the fallback for when AIManager cannot be asked.
func (ag *AIActionGenerator) pickAI(ruleAI *int64) (aipick.Choice, error) {
	ag.mu.RLock()
	loaded := aipick.Choice{
		SystemPrompt: ag.cfg.AI.SystemPrompt,
		Entry:        aipick.Entry{Name: ag.cfg.AI.AIName, Model: ag.cfg.AI.Model},
	}
	if ag.chatClient != nil {
		loaded.Client = ag.chatClient
	}
	factory := ag.chatClientFactory
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
			s, err := ag.aiMgrClient.GetModuleAISetting("actiongenerator")
			if err != nil {
				return nil, err
			}
			out := &aipick.ModuleSetting{SystemPrompt: s.SystemPrompt}
			if s.AI != nil {
				out.AI = pickEntry(s.AI)
			}
			return out, nil
		},
		Build: func(e aipick.Entry) aiclient.ChatClient {
			if factory == nil {
				return aiclient.ForProvider(e.Provider, e.URL, e.APIKey, 0)
			}
			if c := factory(&aimanager.AIEntry{ID: e.ID, Name: e.Name, Provider: e.Provider, Model: e.Model, URL: e.URL, APIKey: e.APIKey}); c != nil {
				return c
			}
			return nil
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

// verifyRoutineHash checks stored code against the SHA256 recorded with it.
//
// An empty stored hash is accepted: routines generated before the hash was
// recorded have none, and refusing those would disable every rule on an
// installation that has not regenerated since. A WRONG hash is refused — that
// is the case this exists for.
func verifyRoutineHash(routine *aimanager.Routine) error {
	if routine.SHA256 == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(routine.Code))
	if got := hex.EncodeToString(sum[:]); got != routine.SHA256 {
		return fmt.Errorf("stored code does not match its recorded SHA256 (recorded %s, got %s)", routine.SHA256, got)
	}
	return nil
}
