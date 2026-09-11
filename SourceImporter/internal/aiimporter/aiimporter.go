// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package aiimporter owns the import rules and the JavaScript routines generated
// from them. It loads the rules from AIManager, keeps one executor per rule, and
// asks the configured AI to generate a routine for a rule that has none — then
// stores the result back in AIManager with its version, model and checksum.
//
// Generation and execution are deliberately separable: an AI is needed only to
// *write* a routine, so an importer with no AI configured still loads and runs
// every routine already marked "passed". The rule watcher calls in here when a
// rule changes, so a running daemon picks up new and edited rules live.
//
// GetImporterVersion composes the per-rule version string ("AI-<type>-v<rule
// version>") that is stamped onto each imported source and later compared to
// decide whether that source deserves a re-import.
package aiimporter

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"tachyonik/lib/logger"
	"tachyonik/lib/textextract"
	"tachyonik/sourceimporter/internal/aimanager"
	"tachyonik/sourceimporter/internal/assetmanager"
	"tachyonik/sourceimporter/internal/codegen"
	"tachyonik/sourceimporter/internal/config"
	"tachyonik/sourceimporter/internal/importrulewatcher"
	"tachyonik/sourceimporter/internal/jsruntime"
	"tachyonik/sourceimporter/internal/rscmanager"
)

// ChatClientFactory creates a ChatClient from an AIEntry. Returns nil if the
// entry cannot be used (e.g. unreachable provider).
type ChatClientFactory func(entry *aimanager.AIEntry) codegen.ChatClient

// AIImporter orchestrates AI-powered source imports.
// It manages import rules, generates JS code via AI, and executes
// the generated code to extract assets/vulnerabilities/detections.
type AIImporter struct {
	rscManagerAPI     *rscmanager.Client
	aiMgrClient       *aimanager.Client
	assetManagerAPI   *assetmanager.Client
	chatClient        codegen.ChatClient
	chatClientFactory ChatClientFactory
	cfg               *config.Config
	rules             map[int64]*aimanager.ImportRule       // by ID
	typeToRule        map[string]*aimanager.ImportRule      // sourceType → rule
	executors         map[int64]*jsruntime.JSImportExecutor // by rule ID
	mu                sync.RWMutex
}

// New creates a new AIImporter
func New(rscManagerAPI *rscmanager.Client, aiMgrClient *aimanager.Client, assetManagerAPI *assetmanager.Client, chatClient codegen.ChatClient, cfg *config.Config) *AIImporter {
	return &AIImporter{
		rscManagerAPI:   rscManagerAPI,
		aiMgrClient:     aiMgrClient,
		assetManagerAPI: assetManagerAPI,
		chatClient:      chatClient,
		cfg:             cfg,
		rules:           make(map[int64]*aimanager.ImportRule),
		typeToRule:      make(map[string]*aimanager.ImportRule),
		executors:       make(map[int64]*jsruntime.JSImportExecutor),
	}
}

// LoadRules fetches all import rules from AIManager, builds lookup maps,
// and loads the active routine for each rule that has one.
func (ai *AIImporter) LoadRules() error {
	rules, err := ai.aiMgrClient.GetImportRules()
	if err != nil {
		return fmt.Errorf("failed to fetch import rules: %w", err)
	}

	ai.mu.Lock()
	defer ai.mu.Unlock()

	ai.rules = make(map[int64]*aimanager.ImportRule, len(rules))
	ai.typeToRule = make(map[string]*aimanager.ImportRule, len(rules))

	for i := range rules {
		rule := &rules[i]
		ai.rules[rule.ID] = rule
		ai.typeToRule[rule.Type] = rule
		ai.loadExecutorForRuleLocked(rule)
	}

	logger.Infof("Loaded %d import rules (%d with active routines)", len(rules), len(ai.executors))
	return nil
}

// loadExecutorForRuleLocked (re)loads ai.executors[rule.ID] to match the rule's
// current active_routine: it loads the active routine's code when that routine
// is "passed", and otherwise removes any stale executor. This keeps the running
// executor in sync with what the admin has activated. Callers MUST hold ai.mu.
//
// Note: this performs a network fetch (GetRoutine) while holding the lock, which
// matches the pre-existing LoadRules behaviour.
func (ai *AIImporter) loadExecutorForRuleLocked(rule *aimanager.ImportRule) {
	if rule.ActiveRoutine == nil {
		if _, existed := ai.executors[rule.ID]; existed {
			delete(ai.executors, rule.ID)
			logger.Infof("Import rule %d (%s) has no active routine, executor removed", rule.ID, rule.Type)
		} else {
			logger.Debugf("Import rule %d (%s) has no active routine", rule.ID, rule.Type)
		}
		return
	}

	routine, err := ai.aiMgrClient.GetRoutine(*rule.ActiveRoutine)
	if err != nil {
		logger.Errorf("Failed to fetch active routine %d for import rule %d (%s): %v",
			*rule.ActiveRoutine, rule.ID, rule.Type, err)
		return
	}

	if routine.Status != "passed" {
		logger.Warnf("Active routine %d for import rule %d has status '%s', removing executor",
			routine.ID, rule.ID, routine.Status)
		delete(ai.executors, rule.ID)
		return
	}

	executor := jsruntime.New()
	if err := executor.LoadFromString(routine.Code); err != nil {
		logger.Errorf("Failed to load routine %d code for import rule %d (%s): %v",
			routine.ID, rule.ID, rule.Type, err)
		return
	}

	ai.executors[rule.ID] = executor
	logger.Infof("Loaded routine %d for import rule %d (%s)", routine.ID, rule.ID, rule.Type)
}

// FindMatchingRule returns the import rule matching the given source type, or nil.
func (ai *AIImporter) FindMatchingRule(sourceType string) *aimanager.ImportRule {
	ai.mu.RLock()
	defer ai.mu.RUnlock()
	return ai.typeToRule[sourceType]
}

// HasExecutor returns true if JS code has been generated and loaded for the given rule ID.
func (ai *AIImporter) HasExecutor(ruleID int64) bool {
	ai.mu.RLock()
	defer ai.mu.RUnlock()
	_, exists := ai.executors[ruleID]
	return exists
}

// Rules returns a copy of the rules map for iteration.
func (ai *AIImporter) Rules() map[int64]*aimanager.ImportRule {
	ai.mu.RLock()
	defer ai.mu.RUnlock()

	result := make(map[int64]*aimanager.ImportRule, len(ai.rules))
	for k, v := range ai.rules {
		result[k] = v
	}
	return result
}

// GenerateForRule runs the AI code generation pipeline for a single import rule:
// generate JS, validate syntax and runtime, save to AIManager.
func (ai *AIImporter) GenerateForRule(rule *aimanager.ImportRule) error {
	logger.Infof("Generating JS code for import rule %d (%s v%s)...", rule.ID, rule.Type, rule.Version)

	// Resolve AI: use per-rule AI if configured, otherwise default
	ruleClient, ruleModel := ai.resolveRuleAI(rule)

	// Generation requires an AI chat client. When no AI is configured the
	// module default client is nil; without a usable per-rule client there is
	// nothing to generate with. Skip rather than calling Chat on a nil client
	// (which would panic). Existing routines keep running regardless — only
	// generation is disabled until an AI is assigned.
	if ruleClient == nil {
		logger.Warnf("Cannot generate routine for import rule %d (%s): no AI configured", rule.ID, rule.Type)
		return fmt.Errorf("no AI configured for rule %d", rule.ID)
	}

	// Create code generator with resolved AI config
	codeGen := codegen.New(ruleClient, ruleModel, ai.cfg.AI.SystemPrompt)

	// Convert to codegen.ImportRule
	codegenRule := codegen.ImportRule{
		ID:          rule.ID,
		Type:        rule.Type,
		Version:     rule.Version,
		Description: rule.Description,
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

	version := fmt.Sprintf("v%s", time.Now().Format("20060102_150405"))

	// Validate syntax by loading into a JS runtime
	tempExecutor := jsruntime.New()
	if err := tempExecutor.LoadFromString(code); err != nil {
		// Store as failed routine in AIManager
		ai.storeRoutine(code, rule.ID, version, ruleModel, sha256Hex, "failed", fmt.Sprintf("Syntax validation failed: %v", err))
		return fmt.Errorf("syntax validation failed for rule %d: %w", rule.ID, err)
	}

	// Validate runtime behavior with mock contexts
	validationErrors := tempExecutor.ValidateWithMockCtx()
	if len(validationErrors) > 0 {
		combinedErr := fmt.Errorf("runtime validation failed with %d errors", len(validationErrors))
		var logMsg string
		for i, e := range validationErrors {
			logger.Errorf("  Validation error %d for rule %d: %v", i+1, rule.ID, e)
			logMsg += fmt.Sprintf("Validation error %d: %v\n", i+1, e)
		}
		ai.storeRoutine(code, rule.ID, version, ruleModel, sha256Hex, "failed", logMsg)
		return combinedErr
	}

	// Store as passed routine in AIManager
	routine, err := ai.storeRoutine(code, rule.ID, version, ruleModel, sha256Hex, "passed", "")
	if err != nil {
		return fmt.Errorf("failed to store routine for rule %d: %w", rule.ID, err)
	}

	// Swap executor in map
	ai.mu.Lock()
	ai.executors[rule.ID] = tempExecutor
	ai.mu.Unlock()

	logger.Infof("Successfully generated and saved JS for import rule %d (%s v%s), routine ID %d", rule.ID, rule.Type, rule.Version, routine.ID)
	return nil
}

// storeRoutine creates a routine in AIManager with the given parameters.
func (ai *AIImporter) storeRoutine(code string, ruleID int64, version, model, sha256Hex, status, log string) (*aimanager.Routine, error) {
	routine, err := ai.aiMgrClient.CreateRoutine(aimanager.CreateRoutineRequest{
		Code:        code,
		Rule:        ruleID,
		Type:        "SourceImporter",
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

// ImportSource executes the AI-generated import for a source.
// Returns import notes string on success.
func (ai *AIImporter) ImportSource(source *rscmanager.Source) (string, error) {
	// Find matching rule
	rule := ai.FindMatchingRule(source.SourceType)
	if rule == nil {
		return "", fmt.Errorf("no matching import rule for source type: %s", source.SourceType)
	}

	// Get executor
	ai.mu.RLock()
	executor, exists := ai.executors[rule.ID]
	ai.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("no JS executor for rule %d (%s)", rule.ID, rule.Type)
	}

	// Download source file
	data, filename, err := ai.rscManagerAPI.DownloadSourceFile(source.ID)
	if err != nil {
		return "", fmt.Errorf("failed to download source file: %w", err)
	}

	// Convert to the text routines operate on (extracted text for PDFs, raw
	// bytes otherwise) plus the detected media type.
	fileContent, mimeType := textextract.Analyze(data)

	// Build import context
	sourceRef := fmt.Sprintf("%s (ID: %d)", source.Filename, source.ID)
	ctx := jsruntime.ImportContext{
		FileContent: fileContent,
		FileName:    filename,
		SourceType:  source.SourceType,
		SourceRef:   sourceRef,
		MimeType:    mimeType,
	}

	// Execute JS import function
	result, err := executor.Execute(ctx)
	if err != nil {
		return "", fmt.Errorf("JS import execution failed: %w", err)
	}

	// Process results: create assets, vulnerabilities, detections via AssetManager API
	assetSuccess := 0
	assetFail := 0
	for _, asset := range result.Assets {
		if _, err := ai.assetManagerAPI.CreateAsset(asset.Name, asset.Type, source.ID, source.Filename, source.UserID, asset.LastSeen); err != nil {
			logger.Errorf("Failed to create asset %s: %v", asset.Name, err)
			assetFail++
		} else {
			assetSuccess++
		}
	}

	vulnSuccess := 0
	vulnFail := 0
	for _, vuln := range result.Vulnerabilities {
		if _, err := ai.assetManagerAPI.CreateVulnerability(vuln.Name, vuln.Host, vuln.Port, vuln.Severity, sourceRef, source.UserID, vuln.LastSeen); err != nil {
			logger.Errorf("Failed to create vulnerability %s on %s:%s: %v", vuln.Name, vuln.Host, vuln.Port, err)
			vulnFail++
		} else {
			vulnSuccess++
		}
	}

	detSuccess := 0
	detFail := 0
	for _, det := range result.Detections {
		if _, err := ai.assetManagerAPI.CreateDetection(det.Name, det.Host, det.Port, sourceRef, source.UserID, det.LastSeen); err != nil {
			logger.Errorf("Failed to create detection %s on %s:%s: %v", det.Name, det.Host, det.Port, err)
			detFail++
		} else {
			detSuccess++
		}
	}

	logger.Infof("AI import results: %d assets (%d failed), %d vulns (%d failed), %d detections (%d failed)",
		assetSuccess, assetFail, vulnSuccess, vulnFail, detSuccess, detFail)

	if assetSuccess == 0 && len(result.Assets) > 0 {
		return "", fmt.Errorf("failed to import any assets from AI import")
	}

	importNotes := fmt.Sprintf("Imported %d assets, %d vulnerabilities, %d detections (AI Importer, rule: %s v%s)",
		assetSuccess, vulnSuccess, detSuccess, rule.Type, rule.Version)

	return importNotes, nil
}

// ImportTestSummary is the dry-run result of a single import routine: how many
// assets/vulnerabilities/detections it would produce, plus an asset-type
// breakdown. JSON tags match the shape the WebUI import-test panel renders.
type ImportTestSummary struct {
	Assets          int            `json:"assets"`
	Vulnerabilities int            `json:"vulnerabilities"`
	Detections      int            `json:"detections"`
	AssetTypes      map[string]int `json:"assetTypes"`
}

// RunImportTest dry-runs a single import routine (by ID, bypassing rule
// matching) against a source's file and returns the counts it WOULD produce. It
// never writes to AssetManager — this backs the WebUI's import-routine test,
// letting a specific (possibly draft) routine be exercised against a real file,
// including PDF text extraction.
func (ai *AIImporter) RunImportTest(source *rscmanager.Source, routineID int64) (*ImportTestSummary, error) {
	routine, err := ai.aiMgrClient.GetRoutine(routineID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch test routine %d: %w", routineID, err)
	}

	executor := jsruntime.New()
	if err := executor.LoadFromString(routine.Code); err != nil {
		return nil, fmt.Errorf("failed to load test routine %d code: %w", routineID, err)
	}

	data, filename, err := ai.rscManagerAPI.DownloadSourceFile(source.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to download test file: %w", err)
	}
	fileContent, mimeType := textextract.Analyze(data)

	ctx := jsruntime.ImportContext{
		FileContent: fileContent,
		FileName:    filename,
		SourceType:  source.SourceType,
		SourceRef:   fmt.Sprintf("%s (test)", source.Filename),
		MimeType:    mimeType,
	}

	result, err := executor.Execute(ctx)
	if err != nil {
		return nil, fmt.Errorf("import routine execution failed: %w", err)
	}

	assetTypes := make(map[string]int)
	for idx := range result.Assets {
		t := result.Assets[idx].Type
		if t == "" {
			t = "unknown"
		}
		assetTypes[t]++
	}

	return &ImportTestSummary{
		Assets:          len(result.Assets),
		Vulnerabilities: len(result.Vulnerabilities),
		Detections:      len(result.Detections),
		AssetTypes:      assetTypes,
	}, nil
}

// HandleRuleChange is called by the import rule watcher when a rule changes.
func (ai *AIImporter) HandleRuleChange(event importrulewatcher.RuleChangeEvent) {
	switch event.Type {
	case "created", "updated":
		// Fetch the specific rule by ID from AIManager
		targetRule, err := ai.aiMgrClient.GetImportRuleByID(event.RuleID)
		if err != nil {
			logger.Errorf("Failed to fetch import rule %d after %s event: %v", event.RuleID, event.Type, err)
			return
		}

		// Update maps and sync the executor to the rule's active routine, so
		// activating (or switching) a routine takes effect immediately without
		// a daemon restart.
		ai.mu.Lock()
		ai.rules[targetRule.ID] = targetRule
		ai.typeToRule[targetRule.Type] = targetRule
		ai.loadExecutorForRuleLocked(targetRule)
		ai.mu.Unlock()

		// Only regenerate when explicitly requested via the generate_requested_at flag
		if targetRule.GenerateRequestedAt != nil {
			logger.Infof("Routine generation requested for import rule %d (%s), generating...", event.RuleID, targetRule.Type)
			// The rule prompt is OPTIONAL: it advises on variable
			// substitution for rules that use variables, and a rule without
			// one is perfectly legitimate. Code generation is given the whole
			// rule and never required this field — refusing on it meant the
			// button appeared to do nothing, with only a log line to say why.
			if strings.TrimSpace(targetRule.RulePrompt) == "" {
				logger.Infof("Import rule %d (%s) has no rule prompt; generating from the rule alone", event.RuleID, targetRule.Type)
			}
			// What to report back. Empty means it worked.
			var failure string
			if err := ai.GenerateForRule(targetRule); err != nil {
				logger.Errorf("Failed to generate routine for import rule %d: %v", event.RuleID, err)
				failure = err.Error()
			}
			// Clear the flag regardless of success/failure to avoid retry
			// loops, and record the reason so the UI can show it rather than
			// leaving it in this log.
			if err := ai.aiMgrClient.ClearGenerateRequest(event.RuleID, failure); err != nil {
				logger.Errorf("Failed to clear generate request flag for import rule %d: %v", event.RuleID, err)
			}
		} else {
			logger.Infof("Import rule %d (%s) updated, executor synced to active routine", event.RuleID, targetRule.Type)
		}

	case "deleted":
		ai.mu.Lock()
		if rule, exists := ai.rules[event.RuleID]; exists {
			delete(ai.typeToRule, rule.Type)
		}
		delete(ai.rules, event.RuleID)
		delete(ai.executors, event.RuleID)
		ai.mu.Unlock()

		logger.Infof("Removed import rule %d from AI importer", event.RuleID)
	}
}

// SetChatClient swaps the AI chat client used for code generation.
func (ai *AIImporter) SetChatClient(client codegen.ChatClient) {
	ai.mu.Lock()
	defer ai.mu.Unlock()
	ai.chatClient = client
}

// SetChatClientFactory sets the factory used to create per-rule AI chat clients.
func (ai *AIImporter) SetChatClientFactory(factory ChatClientFactory) {
	ai.mu.Lock()
	defer ai.mu.Unlock()
	ai.chatClientFactory = factory
}

// resolveRuleAI returns the ChatClient and model to use for code generation.
// If the rule has a per-rule AI configured and a factory is available, it uses that;
// otherwise it falls back to the default client and model.
func (ai *AIImporter) resolveRuleAI(rule *aimanager.ImportRule) (codegen.ChatClient, string) {
	if rule.AI != nil && ai.chatClientFactory != nil {
		entry, err := ai.aiMgrClient.GetAIByID(*rule.AI)
		if err != nil {
			logger.Warnf("Failed to fetch AI %d for rule %d, falling back to default: %v", *rule.AI, rule.ID, err)
			return ai.chatClient, ai.cfg.AI.Model
		}
		client := ai.chatClientFactory(entry)
		if client != nil {
			logger.Infof("Using per-rule AI '%s' (model: %s) for rule %d", entry.Name, entry.Model, rule.ID)
			return client, entry.Model
		}
		logger.Warnf("Per-rule AI '%s' for rule %d could not be created, falling back to default", entry.Name, rule.ID)
	}
	return ai.chatClient, ai.cfg.AI.Model
}

// GetImporterVersion returns the AI importer version string for a given rule.
func (ai *AIImporter) GetImporterVersion(rule *aimanager.ImportRule) string {
	return fmt.Sprintf("AI-%s-v%s", rule.Type, rule.Version)
}
