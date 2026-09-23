// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command actiongenerator is the ActionGenerator daemon entry point. It loads
// configuration, sets up logging, builds the AIManager/AssetManager/
// ActionManager/ResourceManager/SystemManager clients, resolves the configured
// AI chat client, and wires up the AIActionGenerator and the Generator. It then
// starts the watchers (asset and system changes, live action-rule changes) and
// the SystemManager heartbeat, and runs the loop that evaluates rules until it
// receives SIGINT/SIGTERM.
//
// The AI is needed only to *generate* a rule routine. Evaluating an
// already-generated routine is plain JavaScript, so the daemon keeps producing
// actions with no AI configured — only code generation is refused.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"tachyonik/actiongenerator/internal/actionmanager"
	"tachyonik/actiongenerator/internal/actionrulewatcher"
	"tachyonik/actiongenerator/internal/aigenerator"
	"tachyonik/actiongenerator/internal/aimanager"
	"tachyonik/actiongenerator/internal/assetmanager"
	"tachyonik/actiongenerator/internal/codegen"
	"tachyonik/actiongenerator/internal/config"
	"tachyonik/actiongenerator/internal/generator"
	"tachyonik/actiongenerator/internal/resourcemanager"
	"tachyonik/actiongenerator/internal/systemmanager"
	"tachyonik/actiongenerator/internal/version"
	"tachyonik/actiongenerator/internal/watcher"
	"tachyonik/lib/aiclient"
	"tachyonik/lib/aimwatcher"
	"tachyonik/lib/heartbeat"
	"tachyonik/lib/logger"
)

// resolveAI looks up the AI entry from AIManager and creates the appropriate ChatClient.
// Returns nil if no AI is configured or the AI cannot be resolved (logs a warning).
func resolveAI(cfg *config.Config, aiMgrClient *aimanager.Client) codegen.ChatClient {
	if cfg.AI.AIName == "" {
		logger.Warn("No AI configured (ai.name is empty), AI activities disabled")
		return nil
	}

	entry, err := aiMgrClient.GetAIByName(cfg.AI.AIName)
	if err != nil {
		logger.Warnf("AI '%s' not available, AI activities disabled: %v", cfg.AI.AIName, err)
		return nil
	}

	// Store the resolved model in config for CodeGenerator use
	cfg.AI.Model = entry.Model

	timeout := time.Duration(cfg.AI.TimeoutSeconds) * time.Second

	client := buildChatClient(entry, timeout)
	if client == nil {
		logger.Warnf("AI '%s' has unsupported provider %q, AI activities disabled", entry.Name, entry.Provider)
		return nil
	}
	logger.Infof("Using %s AI provider '%s' (model: %s)", entry.Provider, entry.Name, entry.Model)
	return client
}

// chatClientFromEntry creates a ChatClient from an AIEntry.
func chatClientFromEntry(entry *aimanager.AIEntry) codegen.ChatClient {
	return buildChatClient(entry, 300*time.Second)
}

// buildChatClient dispatches on entry.Provider. Unknown providers return
// nil so the caller can disable AI rather than silently routing traffic
// to the wrong wire format. openai|mistral|google|manual share an
// OpenAI-compatible client; google supports the OpenAI shape via its
// /v1beta/openai endpoint, but the entry URL must point there.
func buildChatClient(entry *aimanager.AIEntry, timeout time.Duration) codegen.ChatClient {
	return aiclient.ForProvider(entry.Provider, entry.URL, entry.APIKey, timeout)
}

func setupLogging(cfg *config.Config) (*os.File, error) {
	return logger.SetupFromOptions(logger.FileOptions{
		ToConsole: cfg.Log.ToConsole,
		ToFile:    cfg.Log.ToFile,
		FilePath:  cfg.Log.FilePath,
		Level:     cfg.Log.Level,
	})
}

func main() {
	cfg := config.Load()

	logFile, err := setupLogging(cfg)
	if err != nil {
		logger.Fatalf("Failed to setup logging: %v", err)
	}
	if logFile != nil {
		defer logFile.Close()
	}

	// Route subcommand. Flags are accepted as well as bare words, matching
	// TachyonikProxy: `actiongenerator version` and `actiongenerator --version`
	// both work, so neither habit is wrong.
	for _, arg := range os.Args[1:] {
		switch arg {
		case "help", "--help", "-h":
			runHelp()
			return
		case "version", "--version", "-v":
			fmt.Printf("Tachyonik ActionGenerator %s\n", version.Version)
			return
		}
	}

	runDaemon(cfg)
}

func runHelp() {
	fmt.Println("Tachyonik ActionGenerator")
	fmt.Println()
	fmt.Println("Usage: actiongenerator [command]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  (none)      Start the ActionGenerator daemon (default)")
	fmt.Println("  help        Show this help message")
	fmt.Println("  version     Print the build version and exit")
}

// runDaemon starts the daemon with per-rule JS executors, action rule watcher, and asset watcher.
func runDaemon(cfg *config.Config) {
	logger.Info("Starting Tachyonik ActionGenerator daemon...")
	logConfiguration(cfg)

	c := newClients(cfg)
	systemMgrClient, resourceMgrClient := c.systemMgr, c.resourceMgr
	assetMgrClient, actionMgrClient, aiMgrClient := c.assetMgr, c.actionMgr, c.aiMgr

	// Fetch initial AI settings from AIManager
	moduleSetting, err := aiMgrClient.GetModuleAISetting("actiongenerator")
	if err != nil {
		logger.Warnf("Failed to fetch module AI settings from AIManager, AI disabled: %v", err)
		cfg.AI.AIName = ""
	} else if moduleSetting.AI != nil {
		cfg.AI.AIName = moduleSetting.AI.Name
		cfg.AI.SystemPrompt = moduleSetting.SystemPrompt
		logger.Infof("Module AI settings loaded: AI=%s, SystemPrompt=%d chars", cfg.AI.AIName, len(cfg.AI.SystemPrompt))
	} else {
		cfg.AI.AIName = ""
		cfg.AI.SystemPrompt = moduleSetting.SystemPrompt
		logger.Info("Module AI settings loaded: AI unset (disabled)")
	}
	if cfg.AI.SystemPrompt == "" {
		logger.Warn("ActionGenerator system prompt is empty in AIManager — code generation will be refused until configured (Settings → ActionGenerator → System Prompt). Rule evaluation continues to work.")
	}

	// Initialize generator
	gen := generator.New(systemMgrClient, resourceMgrClient, assetMgrClient, actionMgrClient, aiMgrClient)
	logger.Info("Generator initialized")

	// Create AIActionGenerator — always created for rule loading and evaluation.
	// The AI chat client is optional (only needed for code generation, not evaluation).
	chatClient := resolveAI(cfg, aiMgrClient)

	aiGen := aigenerator.New(aiMgrClient, chatClient, cfg)
	if chatClient != nil {
		aiGen.SetChatClientFactory(chatClientFromEntry)
	} else {
		logger.Info("No AI configured — rule evaluation works, but code generation is disabled")
	}

	// Load rules with per-rule active routines (does not require AI)
	if err := aiGen.LoadRules(); err != nil {
		logger.Errorf("Failed to load action rules: %v", err)
	} else {
		gen.SetExecutors(aiGen.Executors())
	}

	// Shared module-settings refresh — used by both the
	// MODULE_AI_SETTING_UPDATED watcher and the FEED_IMPORTED reload
	// path below. See SourceAnalyser's daemon for full rationale.
	refreshModuleSettings := func() {
		setting, fetchErr := aiMgrClient.GetModuleAISetting("actiongenerator")
		if fetchErr != nil {
			logger.Warnf("Failed to re-fetch module AI settings: %v", fetchErr)
			return
		}
		if setting.AI != nil {
			cfg.AI.AIName = setting.AI.Name
			cfg.AI.SystemPrompt = setting.SystemPrompt
		} else {
			cfg.AI.AIName = ""
			cfg.AI.SystemPrompt = setting.SystemPrompt
		}
		if cfg.AI.SystemPrompt == "" {
			logger.Warn("ActionGenerator system prompt is empty in AIManager — code generation will be refused until configured.")
		}
		newClient := resolveAI(cfg, aiMgrClient)
		if newClient != nil {
			aiGen.SetChatClient(newClient)
			logger.Info("AI chat client re-resolved successfully via AIManager settings")
		} else {
			logger.Warn("AI re-resolution via AIManager settings resulted in nil client")
		}
	}

	// Start AIManager settings watcher for real-time AI config changes
	aimw := aimwatcher.New(cfg.AIManager.URL, cfg.AIManager.InternalServiceKey, aimwatcher.Handlers{ModuleName: "actiongenerator", OnModuleSettingChange: refreshModuleSettings})
	aimw.Start()
	defer aimw.Close()
	logger.Info("AIManager settings watcher started")

	// Start action rule watcher for rule changes and evaluation requests
	arw, err := actionrulewatcher.New(cfg.AIManager.URL, cfg.AIManager.InternalServiceKey, func(event actionrulewatcher.RuleChangeEvent) {
		logger.Infof("Action rule %d changed (%s), regenerating...", event.RuleID, event.Type)

		aiGen.HandleRuleChange(event)

		// Update executor map on generator and re-process rules
		gen.SetExecutors(aiGen.Executors())
		logger.Info("Re-processing rules after regeneration...")
		if err := gen.ProcessRules(); err != nil {
			logger.Errorf("Error processing rules after regeneration: %v", err)
		}
	})
	if err != nil {
		logger.Errorf("Failed to create action rule watcher: %v", err)
	} else {
		arw.SetEvaluationRequestedHandler(func(event actionrulewatcher.EvaluationRequestEvent) {
			logger.Infof("Processing evaluation request for rule %d, user %d", event.RuleID, event.UserID)
			created, err := gen.ProcessRuleForUser(event.RuleID, event.UserID, event.Force)
			if err != nil {
				logger.Errorf("Failed to process rule %d for user %d: %v", event.RuleID, event.UserID, err)
				return
			}
			logger.Infof("Evaluation request for rule %d, user %d: created %d actions", event.RuleID, event.UserID, created)
		})
		// FEED_IMPORTED rebuilds rules + executors and refreshes module
		// settings, then re-processes rules so any freshly imported
		// active routines start firing immediately.
		arw.SetFeedImportedHandler(func() {
			logger.Info("FEED_IMPORTED: reloading action rules + module settings")
			if err := aiGen.LoadRules(); err != nil {
				logger.Errorf("Failed to reload action rules after feed import: %v", err)
			} else {
				gen.SetExecutors(aiGen.Executors())
			}
			refreshModuleSettings()
			if err := gen.ProcessRules(); err != nil {
				logger.Errorf("Error processing rules after feed import: %v", err)
			}
		})
		arw.Start()
		defer arw.Close()
		logger.Info("Action rule watcher started")
	}

	// Process rules on startup
	logger.Info("Processing rules on startup...")
	if err := gen.ProcessRules(); err != nil {
		logger.Errorf("Error processing rules: %v", err)
	}

	onChange := newDebouncedProcessor(gen)

	// Start WebSocket watcher for AssetManager events
	w, err := watcher.New(cfg.AssetManager.URL, cfg.AssetManager.InternalServiceKey, onChange)
	if err != nil {
		logger.Fatalf("Failed to create watcher: %v", err)
	}
	defer w.Close()

	w.Start()
	logger.Info("AssetManager WebSocket watcher started")

	// Start WebSocket watcher for SystemManager events.
	//
	// A rule's context is not only assets. It reads the organisation record and
	// the user's own settings, and neither of those produces an AssetManager
	// event — so a rule that had matched went on matching after the thing it
	// tested changed. Assigning an AI provider to Research left the action
	// telling you to assign one, until something unrelated happened to wake the
	// daemon.
	//
	// Shares onChange with the asset watcher, so events arriving together
	// collapse into one debounced re-evaluation.
	sw, err := watcher.NewFor("SystemManager", cfg.SystemManager.URL,
		cfg.SystemManager.InternalServiceKey, watcher.SystemTriggers, onChange)
	if err != nil {
		logger.Errorf("Failed to create SystemManager watcher: %v", err)
	} else {
		defer sw.Close()
		sw.Start()
		logger.Info("SystemManager WebSocket watcher started")
	}

	// Start liveness heartbeat to SystemManager.
	heartbeat.Start(context.Background(), heartbeat.Config{
		ServiceName:        "ActionGenerator",
		IntervalSeconds:    cfg.Heartbeat.IntervalSeconds,
		SystemManagerURL:   cfg.SystemManager.URL,
		InternalServiceKey: cfg.SystemManager.InternalServiceKey,
	}, logger.Errorf)

	waitForShutdown()
}

// logConfiguration reports what the daemon is about to run with. Kept whole and
// separate: it is the first thing read in a support log, and nothing about it
// is logic.
func logConfiguration(cfg *config.Config) {
	logger.Info("Configuration loaded:")
	logger.Infof("  AIManager URL: %s", cfg.AIManager.URL)
	logger.Infof("  AI Timeout: %ds", cfg.AI.TimeoutSeconds)
	logger.Infof("  JS Exec Timeout: %ds", cfg.AI.JSExecTimeoutSeconds)
	logger.Infof("  AssetManager URL: %s", cfg.AssetManager.URL)
	logger.Infof("  SystemManager URL: %s", cfg.SystemManager.URL)
	logger.Infof("  ResourceManager URL: %s", cfg.ResourceManager.URL)
	logger.Infof("  ActionManager URL: %s", cfg.ActionManager.URL)
	logger.Infof("  Log File: %s", cfg.Log.FilePath)
	logger.Infof("  Log to Console: %v", cfg.Log.ToConsole)
	logger.Infof("  Log to File: %v", cfg.Log.ToFile)
	logger.Infof("  Log Level: %s", cfg.Log.Level)
}

// clients are this daemon's handles on the platform's services.
type clients struct {
	systemMgr   *systemmanager.Client
	resourceMgr *resourcemanager.Client
	assetMgr    *assetmanager.Client
	actionMgr   *actionmanager.Client
	aiMgr       *aimanager.Client
}

// newClients builds them all. Constructing a client performs no I/O, so there
// is nothing here that can fail — the first request is where a wrong URL or a
// missing key shows up.
func newClients(cfg *config.Config) *clients {
	c := &clients{
		systemMgr:   systemmanager.NewClient(cfg.SystemManager.URL, cfg.SystemManager.InternalServiceKey),
		resourceMgr: resourcemanager.NewClient(cfg.ResourceManager.URL, cfg.ResourceManager.InternalServiceKey),
		assetMgr:    assetmanager.NewClient(cfg.AssetManager.URL, cfg.AssetManager.InternalServiceKey),
		actionMgr:   actionmanager.NewClient(cfg.ActionManager.URL, cfg.ActionManager.InternalServiceKey),
		aiMgr:       aimanager.NewClient(cfg.AIManager.URL, cfg.AIManager.InternalServiceKey),
	}
	logger.Info("API clients initialized")
	return c
}

// newDebouncedProcessor returns the callback the event watchers share.
//
// Two guards, doing different jobs. The timer collapses a burst of events — an
// import produces hundreds — into one re-evaluation two seconds after the last
// one. The processing flag then keeps a second run from starting while the
// first is still going, since a full pass over every rule and user can outlast
// the debounce window.
func newDebouncedProcessor(gen *generator.Generator) func() {
	var mu sync.Mutex
	var processing bool
	var debounceTimer *time.Timer

	return func() {
		mu.Lock()
		defer mu.Unlock()

		if debounceTimer != nil {
			debounceTimer.Stop()
		}

		debounceTimer = time.AfterFunc(2*time.Second, func() {
			mu.Lock()
			if processing {
				mu.Unlock()
				return
			}
			processing = true
			mu.Unlock()

			logger.Info("Asset/Vulnerability change detected, processing rules...")
			if err := gen.ProcessRules(); err != nil {
				logger.Errorf("Error processing rules: %v", err)
			}

			mu.Lock()
			processing = false
			mu.Unlock()
		})
	}
}

// waitForShutdown blocks until the service is asked to stop. Returning from
// here is what runs runDaemon's deferred Close calls.
func waitForShutdown() {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	logger.Info("ActionGenerator daemon is running. Press Ctrl+C to stop.")
	<-quit

	logger.Info("Shutting down ActionGenerator daemon...")
	logger.Info("Daemon stopped")
}
