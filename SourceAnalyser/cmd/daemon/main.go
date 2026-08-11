// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command sourceanalyser is the SourceAnalyser daemon entry point. It loads
// configuration, sets up logging, builds the ResourceManager/AIManager/
// SystemManager clients, resolves the configured AI chat client, and wires up
// the AIAnalyser. It then starts the AIManager watcher (live rule and settings
// changes) and the SystemManager heartbeat, and runs the poll loop that drives
// analysis until it receives SIGINT/SIGTERM.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tachyonik/lib/aiclient/claude"
	"tachyonik/lib/aiclient/ollama"
	"tachyonik/lib/aiclient/openai"
	"tachyonik/lib/aimwatcher"
	"tachyonik/lib/heartbeat"
	"tachyonik/lib/logger"
	"tachyonik/lib/systemmanager"
	"tachyonik/sourceanalyser/internal/aianalyser"
	"tachyonik/sourceanalyser/internal/aimanager"
	"tachyonik/sourceanalyser/internal/analyzer"
	"tachyonik/sourceanalyser/internal/codegen"
	"tachyonik/sourceanalyser/internal/config"
	"tachyonik/sourceanalyser/internal/resourcemanager"
	"tachyonik/sourceanalyser/internal/version"
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

	client := buildChatClient(entry)
	if client == nil {
		logger.Warnf("AI '%s' has unsupported provider %q, AI activities disabled", entry.Name, entry.Provider)
		return nil
	}
	logger.Infof("Using %s AI provider '%s' (model: %s)", entry.Provider, entry.Name, entry.Model)
	return client
}

// applyModuleSettings copies AIManager's per-module AI settings onto cfg and
// reports what was applied. An unset AI clears the name — which disables code
// generation — while the system prompt is taken either way, so a prompt
// configured ahead of the AI is not discarded.
//
// Startup and the live-update path (MODULE_AI_SETTING_UPDATED, and the feed
// import that rewrites module_ai_settings) both route through here, so the two
// cannot drift.
func applyModuleSettings(cfg *config.Config, setting *aimanager.ModuleAISetting) {
	if setting.AI != nil {
		cfg.AI.AIName = setting.AI.Name
		logger.Infof("Module AI settings loaded: AI=%s, SystemPrompt=%d chars", setting.AI.Name, len(setting.SystemPrompt))
	} else {
		cfg.AI.AIName = ""
		logger.Info("Module AI settings loaded: AI unset (disabled)")
	}
	cfg.AI.SystemPrompt = setting.SystemPrompt
	warnIfNoSystemPrompt(cfg)
}

// warnIfNoSystemPrompt reports the misconfiguration that silently disables code
// generation while leaving already-generated routines running.
func warnIfNoSystemPrompt(cfg *config.Config) {
	if cfg.AI.SystemPrompt == "" {
		logger.Warn("SourceAnalyser system prompt is empty in AIManager — code generation will be refused until configured (Settings → SourceAnalyser → System Prompt). Existing analysis routines continue to run.")
	}
}

// buildChatClient dispatches on entry.Provider. Unknown providers return
// nil so the caller can disable AI rather than silently routing traffic
// to the wrong wire format. openai|mistral|google|manual share an
// OpenAI-compatible client; google supports the OpenAI shape via its
// /v1beta/openai endpoint, but the entry URL must point there.
func buildChatClient(entry *aimanager.AIEntry) codegen.ChatClient {
	switch entry.Provider {
	case "ollama":
		return ollama.NewClient(entry.URL, entry.APIKey)
	case "anthropic":
		return claude.NewClient(entry.URL, entry.APIKey)
	case "openai", "mistral", "google", "manual":
		return openai.NewClient(entry.URL, entry.APIKey)
	default:
		return nil
	}
}

func setupLogging(cfg *config.Config) (*os.File, error) {
	var writers []io.Writer
	var logFile *os.File

	// Add console output if enabled
	if cfg.Log.ToConsole {
		writers = append(writers, os.Stdout)
	}

	// Add file output if enabled
	if cfg.Log.ToFile {
		var err error
		logFile, err = os.OpenFile(cfg.Log.FilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, err
		}
		writers = append(writers, logFile)
	}

	// Parse log level
	logLevel := logger.ParseLogLevel(cfg.Log.Level)

	// Setup logger with writers and level
	logger.Setup(logLevel, writers...)

	return logFile, nil
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

	// Route subcommand
	for _, arg := range os.Args[1:] {
		switch arg {
		case "help":
			runHelp()
			return
		}
	}

	runDaemon(cfg)
}

func runHelp() {
	fmt.Println("Tachyonik SourceAnalyser")
	fmt.Println()
	fmt.Println("Usage: sourceanalyser [command]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  (none)      Start the SourceAnalyser daemon (default)")
	fmt.Println("  help        Show this help message")
}

// runDaemon starts the daemon with AIAnalyser, analysis rule watcher, and polling loop.
func runDaemon(cfg *config.Config) {
	logger.Info("Starting Tachyonik SourceAnalyser daemon...")
	logger.Infof("Version: %s", version.Version)
	logger.Info("Configuration loaded:")
	logger.Infof("  AIManager URL: %s", cfg.AIManager.URL)
	logger.Infof("  ResourceManager URL: %s", cfg.ResourceManager.URL)
	logger.Infof("  SystemManager URL: %s", cfg.SystemManager.URL)
	logger.Infof("  Poll Interval: %d seconds", cfg.Analyzer.PollInterval)
	logger.Infof("  Log File: %s", cfg.Log.FilePath)
	logger.Infof("  Log to Console: %v", cfg.Log.ToConsole)
	logger.Infof("  Log to File: %v", cfg.Log.ToFile)
	logger.Infof("  Log Level: %s", cfg.Log.Level)

	// Validate required config
	if cfg.ResourceManager.URL == "" {
		logger.Fatalf("ResourceManager URL is required")
	}
	if cfg.ResourceManager.InternalServiceKey == "" {
		logger.Fatalf("ResourceManager Internal Service Key is required")
	}

	// Initialize API clients
	rscMgrClient := resourcemanager.NewClient(cfg.ResourceManager.URL, cfg.ResourceManager.InternalServiceKey)
	logger.Info("ResourceManager API client initialized")

	// Initialize AIManager client and resolve AI
	aiMgrClient := aimanager.NewClient(cfg.AIManager.URL, cfg.AIManager.InternalServiceKey)

	// Fetch initial AI settings from AIManager
	moduleSetting, err := aiMgrClient.GetModuleAISetting("sourceanalyser")
	if err != nil {
		logger.Warnf("Failed to fetch module AI settings from AIManager, AI disabled: %v", err)
		cfg.AI.AIName = ""
		warnIfNoSystemPrompt(cfg)
	} else {
		applyModuleSettings(cfg, moduleSetting)
	}

	// Resolve AI chat client from AIManager
	chatClient := resolveAI(cfg, aiMgrClient)

	// Initialize analyzer
	a := analyzer.New(rscMgrClient, cfg)
	// Wire SystemManager audit-event client. Best-effort: nil-safe in Analyzer.
	smClient := systemmanager.NewClient(cfg.SystemManager.URL, cfg.SystemManager.InternalServiceKey)
	a.SetAuditEmitter(smClient)

	// Initialize the AIAnalyser unconditionally. AI is only required to
	// *generate* rule routines; loading and executing already-generated
	// ("passed") routines is pure JS and needs no AI. Gating the whole
	// subsystem on chatClient would mean a daemon with AI unset applies no
	// rules at all and marks every source Unsupported. chatClient may be nil
	// here — the AIAnalyser tolerates it, and the watchers below pick up a
	// later AI assignment live via refreshModuleSettings.
	{
		ai := aianalyser.New(aiMgrClient, chatClient, cfg)
		ai.SetChatClientFactory(buildChatClient)

		if err := ai.LoadRules(); err != nil {
			logger.Errorf("Failed to load analysis rules: %v", err)
		} else {
			logger.Infof("AIAnalyser loaded: %d rules (%d with existing JS code)", len(ai.Rules()), ai.RuleCount())
		}

		a.SetAIAnalyser(ai)
		if chatClient != nil {
			logger.Info("Analyzer initialized with AIAnalyser")
		} else {
			logger.Info("Analyzer initialized with AIAnalyser: no AI configured, code generation disabled; existing analysis routines will still be applied")
		}

		// Shared module-settings refresh used by both the
		// MODULE_AI_SETTING_UPDATED watcher and the FEED_IMPORTED reload
		// path below — a feed import rewrites module_ai_settings but only
		// emits FEED_IMPORTED, so we route through the same helper to
		// keep AI client and system prompt in sync either way.
		refreshModuleSettings := func() {
			setting, err := aiMgrClient.GetModuleAISetting("sourceanalyser")
			if err != nil {
				logger.Warnf("Failed to re-fetch module AI settings: %v", err)
				return
			}
			applyModuleSettings(cfg, setting)
			newClient := resolveAI(cfg, aiMgrClient)
			if newClient != nil {
				ai.SetChatClient(newClient)
				logger.Info("AI chat client re-resolved successfully via AIManager settings")
			} else {
				logger.Warn("AI re-resolution via AIManager settings resulted in nil client")
			}
		}

		// Generate for rules that don't have executors yet. This requires an
		// AI chat client, so only attempt it when one is configured. Without
		// AI the already-loaded routines still run; missing ones simply won't
		// be generated until an AI is assigned (picked up via the watcher).
		if chatClient != nil {
			rules := ai.Rules()
			needsGeneration := false
			for id := range rules {
				if !ai.HasExecutor(id) {
					needsGeneration = true
					break
				}
			}
			if needsGeneration {
				logger.Info("Generating JS for rules without executors...")
				go func() {
					for id, rule := range rules {
						if !ai.HasExecutor(id) {
							if err := ai.GenerateForRule(rule); err != nil {
								logger.Errorf("Failed to generate JS for rule %d: %v", id, err)
							}
						}
					}
					logger.Infof("Startup generation complete (%d executors loaded)", ai.RuleCount())
				}()
			}
		}

		// Start the AIManager watcher. A single WebSocket connection carries
		// analysis rule changes, feed imports, and module AI setting updates.
		//
		// The FEED_IMPORTED callback runs a full LoadRules() rebuild — the import
		// wipes and reinserts rules + routines, so the in-memory rule/executor
		// maps are stale until rebuilt. It also refreshes module settings because
		// the feed carries the system prompt and AI selection too.
		watcher := aimwatcher.New(cfg.AIManager.URL, cfg.AIManager.InternalServiceKey, aimwatcher.Handlers{
			ModuleName: "sourceanalyser",
			OnRuleChange: func(event aimwatcher.RuleChangeEvent) {
				ai.HandleRuleChange(event)
				// A changed/created rule may now identify resources previously left
				// Unsupported — request a re-analysis on the next poll.
				a.RequestRevisit()
			},
			OnFeedImported: func() {
				logger.Info("FEED_IMPORTED: reloading analysis rules + module settings")
				if err := ai.LoadRules(); err != nil {
					logger.Errorf("Failed to reload analysis rules after feed import: %v", err)
				} else {
					logger.Infof("Analysis rules reloaded: %d rules (%d executors)", len(ai.Rules()), ai.RuleCount())
				}
				refreshModuleSettings()
				a.RequestRevisit()
			},
			OnModuleSettingChange: refreshModuleSettings,
		})
		watcher.Start()
		defer watcher.Close()
		logger.Info("AIManager watcher started")
	}

	// Start liveness heartbeat to SystemManager. Runs for the life of the
	// daemon — process exit reaps the goroutine, so we use a Background
	// context here.
	heartbeat.Start(context.Background(), heartbeat.Config{
		ServiceName:        "SourceAnalyser",
		IntervalSeconds:    cfg.Heartbeat.IntervalSeconds,
		SystemManagerURL:   cfg.SystemManager.URL,
		InternalServiceKey: cfg.SystemManager.InternalServiceKey,
	}, logger.Errorf)

	// Create ticker for periodic checks
	ticker := time.NewTicker(time.Duration(cfg.Analyzer.PollInterval) * time.Second)
	defer ticker.Stop()

	// Setup signal handling for graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	logger.Infof("SourceAnalyser daemon is running. Checking every %d seconds...", cfg.Analyzer.PollInterval)

	// Main loop
	for {
		select {
		case <-ticker.C:
			if err := a.ProcessEntries(); err != nil {
				logger.Errorf("Error processing entries: %v", err)
			}

		case sig := <-quit:
			logger.Infof("Received signal: %v. Shutting down...", sig)
			logger.Info("Daemon stopped")
			return
		}
	}
}
