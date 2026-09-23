// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command sourceimporter is the SourceImporter daemon entry point. It loads
// configuration, sets up logging, builds the ResourceManager/AssetManager/
// AIManager/SystemManager clients, resolves the configured AI chat client, and
// wires up the AIImporter and the Importer. It then starts the AIManager
// watchers (live AI-settings and import-rule changes) and the SystemManager
// heartbeat, and runs the poll loop that drives importing until it receives
// SIGINT/SIGTERM.
//
// The AI is optional here in a way worth stating: it is needed only to
// *generate* an import routine. Executing an already-generated routine is plain
// JavaScript, so the daemon starts and keeps importing with no AI configured —
// only code generation is refused.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"tachyonik/lib/aiclient"
	"tachyonik/lib/aimwatcher"
	"tachyonik/lib/heartbeat"
	"tachyonik/lib/logger"
	"tachyonik/lib/systemmanager"
	"tachyonik/sourceimporter/internal/aiimporter"
	"tachyonik/sourceimporter/internal/aimanager"
	"tachyonik/sourceimporter/internal/assetmanager"
	"tachyonik/sourceimporter/internal/codegen"
	"tachyonik/sourceimporter/internal/config"
	"tachyonik/sourceimporter/internal/importer"
	"tachyonik/sourceimporter/internal/importrulewatcher"
	"tachyonik/sourceimporter/internal/rscmanager"
	"tachyonik/sourceimporter/internal/version"
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

// buildChatClient dispatches on entry.Provider. Unknown providers return
// nil so the caller can disable AI rather than silently routing traffic
// to the wrong wire format. openai|mistral|google|manual share an
// OpenAI-compatible client; google supports the OpenAI shape via its
// /v1beta/openai endpoint, but the entry URL must point there.
func buildChatClient(entry *aimanager.AIEntry) codegen.ChatClient {
	return aiclient.ForProvider(entry.Provider, entry.URL, entry.APIKey, 0)
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
	// Route subcommand before anything else. Asking a binary its version must
	// work wherever the binary does — from a package script, as another user,
	// on a host where the configured log path is not writable — and
	// setupLogging is fatal on a log file it cannot open. Neither `version`
	// nor `help` needs the configuration, so neither should be able to fail on
	// it.
	//
	// Flags are accepted as well as bare words, matching TachyonikProxy:
	// `sourceimporter version` and `sourceimporter --version` both
	// work, so neither habit is wrong.
	for _, arg := range os.Args[1:] {
		switch arg {
		case "help", "--help", "-h":
			runHelp()
			return
		case "version", "--version", "-v":
			fmt.Printf("Tachyonik SourceImporter %s\n", version.Version)
			return
		}
	}

	// Load configuration
	cfg := config.Load()

	// Setup logging based on configuration
	logFile, err := setupLogging(cfg)
	if err != nil {
		logger.Fatalf("Failed to setup logging: %v", err)
	}
	if logFile != nil {
		defer logFile.Close()
	}

	runDaemon(cfg)
}

// runDaemon starts the daemon with polling, AI import support, and watchers.
// setupAIImporter builds the AI importer and starts the watchers that keep it
// current.
//
// Constructed unconditionally. AI is only required to *generate* import
// routines; loading and executing already-generated ("passed") routines is pure
// JS and needs none. Gating the whole subsystem on an AI being configured would
// mean a daemon with AI unset applies no import routines at all and marks every
// source "No import routine".
//
// Returns the revisit flag, which the caller hands to the Importer — the
// watcher closures set it whenever the rules reload, so the next poll
// re-examines resources parked in "No import routine". It is a shared atomic
// rather than a method call because these closures are built before the
// Importer exists.
//
// Also returns a close function rather than deferring the watcher's own
// shutdown, which would fire when this function returns instead of when the
// daemon stops. It is never nil, so the caller can defer it unconditionally.
func setupAIImporter(cfg *config.Config, rscManagerClient *rscmanager.Client, aiMgrClient *aimanager.Client, assetManagerClient *assetmanager.Client, chatClient codegen.ChatClient) (*aiimporter.AIImporter, *atomic.Bool, func()) {
	revisitImport := &atomic.Bool{}
	closeWatcher := func() {}

	aiImp := aiimporter.New(rscManagerClient, aiMgrClient, assetManagerClient, chatClient, cfg)
	aiImp.SetChatClientFactory(buildChatClient)
	if err := aiImp.LoadRules(); err != nil {
		logger.Warnf("Failed to load import rules: %v", err)
	}
	if chatClient == nil {
		logger.Info("AI importer initialized: no AI configured, code generation disabled; existing import routines will still be applied")
	}

	// Shared module-settings refresh — used by both the
	// MODULE_AI_SETTING_UPDATED watcher and the FEED_IMPORTED reload
	// path below. See SourceAnalyser's daemon for full rationale.
	refreshModuleSettings := func() {
		setting, err := aiMgrClient.GetModuleAISetting("sourceimporter")
		if err != nil {
			logger.Warnf("Failed to re-fetch module AI settings: %v", err)
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
			logger.Warn("SourceImporter system prompt is empty in AIManager — code generation will be refused until configured.")
		}
		newClient := resolveAI(cfg, aiMgrClient)
		if newClient != nil {
			aiImp.SetChatClient(newClient)
			logger.Info("AI chat client re-resolved successfully via AIManager settings")
		} else {
			logger.Warn("AI re-resolution via AIManager settings resulted in nil client")
		}
	}

	// Start AIManager settings watcher for real-time AI config changes
	aimw := aimwatcher.New(cfg.AIManager.URL, cfg.AIManager.InternalServiceKey, aimwatcher.Handlers{ModuleName: "sourceimporter", OnModuleSettingChange: refreshModuleSettings})
	aimw.Start()
	defer aimw.Close()
	logger.Info("AIManager settings watcher started")

	// Generate missing JS code on startup. This requires an AI chat
	// client, so only attempt it when one is configured. Without AI the
	// already-loaded routines still run; missing ones simply won't be
	// generated until an AI is assigned (picked up via the watcher).
	if chatClient != nil {
		for ruleID, rule := range aiImp.Rules() {
			if !aiImp.HasExecutor(ruleID) {
				logger.Infof("Generating missing JS for rule %d (%s) on startup...", ruleID, rule.Type)
				if err := aiImp.GenerateForRule(rule); err != nil {
					logger.Errorf("Failed to generate JS for rule %d on startup: %v", ruleID, err)
				}
			}
		}
	}

	// Start import rule watcher. FEED_IMPORTED triggers a full
	// LoadRules() rebuild plus module-settings refresh — see
	// SourceAnalyser's daemon for the rationale.
	irw, err := importrulewatcher.New(cfg.AIManager.URL, cfg.AIManager.InternalServiceKey, func(event importrulewatcher.RuleChangeEvent) {
		aiImp.HandleRuleChange(event)
		// A changed/created rule may match resources previously parked in
		// "No import routine" — request a re-visit on the next poll.
		revisitImport.Store(true)
	}, func() {
		logger.Info("FEED_IMPORTED: reloading import rules + module settings")
		if err := aiImp.LoadRules(); err != nil {
			logger.Errorf("Failed to reload import rules after feed import: %v", err)
		}
		refreshModuleSettings()
		revisitImport.Store(true)
	})
	if err != nil {
		logger.Errorf("Failed to create import rule watcher: %v", err)
	} else {
		irw.Start()
		// NOT deferred here: this function returns long before the
		// daemon does, and closing the watcher on the way out would
		// leave it deaf to every later rule change. The caller owns
		// the lifetime.
		closeWatcher = func() {
			if cerr := irw.Close(); cerr != nil {
				logger.Warnf("Import rule watcher close: %v", cerr)
			}
		}
		logger.Info("Import rule watcher started")
	}

	return aiImp, revisitImport, closeWatcher
}

// logStartupConfig records what the daemon is running with, so a support log
// answers "which URLs, which log level" without asking for the config file.
func logStartupConfig(cfg *config.Config) {
	logger.Info("Starting Tachyonik SourceImporter daemon...")
	logger.Infof("Version: %s", version.Version)
	logger.Info("Configuration loaded:")
	logger.Infof("  ResourceManager URL: %s", cfg.ResourceManager.URL)
	logger.Infof("  AssetManager URL: %s", cfg.AssetManager.URL)
	logger.Infof("  SystemManager URL: %s", cfg.SystemManager.URL)
	logger.Infof("  AIManager URL: %s", cfg.AIManager.URL)
	logger.Infof("  Poll Interval: %d seconds", cfg.Importer.PollInterval)
	logger.Infof("  Log File: %s", cfg.Log.FilePath)
	logger.Infof("  Log to Console: %v", cfg.Log.ToConsole)
	logger.Infof("  Log to File: %v", cfg.Log.ToFile)
	logger.Infof("  Log Level: %s", cfg.Log.Level)
}

// requireConfig stops the daemon when something it cannot work without is
// missing. Fatal rather than a returned error on purpose: there is no degraded
// mode here — without ResourceManager it has nothing to import, and without
// AssetManager nowhere to put the result.
func requireConfig(cfg *config.Config) {
	for _, req := range []struct {
		value string
		name  string
	}{
		{cfg.ResourceManager.URL, "ResourceManager URL"},
		{cfg.ResourceManager.InternalServiceKey, "ResourceManager Internal Service Key"},
		{cfg.AssetManager.URL, "AssetManager URL"},
		{cfg.AssetManager.InternalServiceKey, "AssetManager Internal Service Key"},
	} {
		if req.value == "" {
			logger.Fatalf("%s is required", req.name)
		}
	}
}

func runDaemon(cfg *config.Config) {
	logStartupConfig(cfg)
	requireConfig(cfg)

	// Initialize API clients
	assetManagerClient := assetmanager.NewClient(cfg.AssetManager.URL, cfg.AssetManager.InternalServiceKey)
	logger.Info("AssetManager API client initialized")

	rscManagerClient := rscmanager.NewClient(cfg.ResourceManager.URL, cfg.ResourceManager.InternalServiceKey)
	logger.Info("ResourceManager API client initialized")

	// Initialize AIManager client and resolve AI
	aiMgrClient := aimanager.NewClient(cfg.AIManager.URL, cfg.AIManager.InternalServiceKey)

	// Fetch initial AI settings from AIManager
	moduleSetting, err := aiMgrClient.GetModuleAISetting("sourceimporter")
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
		logger.Warn("SourceImporter system prompt is empty in AIManager — code generation will be refused until configured (Settings → SourceImporter → System Prompt). Source ingestion via existing routines continues to work.")
	}

	// Resolve AI chat client from AIManager
	chatClient := resolveAI(cfg, aiMgrClient)

	// chatClient may be nil — see setupAIImporter, which tolerates it and picks
	// up a later AI assignment live.
	aiImp, revisitImport, closeWatcher := setupAIImporter(cfg, rscManagerClient, aiMgrClient, assetManagerClient, chatClient)
	defer closeWatcher()

	// aiImp may be nil if AI is unavailable; the Importer is nil-safe.
	imp := importer.New(assetManagerClient, rscManagerClient, aiImp)
	imp.SetRevisitFlag(revisitImport)
	// Wire SystemManager audit-event client. Best-effort: nil-safe in Importer.
	smClient := systemmanager.NewClient(cfg.SystemManager.URL, cfg.SystemManager.InternalServiceKey)
	imp.SetAuditEmitter(smClient)
	logger.Info("Importer initialized")

	// Start liveness heartbeat to SystemManager.
	heartbeat.Start(context.Background(), heartbeat.Config{
		ServiceName:        "SourceImporter",
		IntervalSeconds:    cfg.Heartbeat.IntervalSeconds,
		SystemManagerURL:   cfg.SystemManager.URL,
		InternalServiceKey: cfg.SystemManager.InternalServiceKey,
	}, logger.Errorf)

	runPollingLoop(cfg, imp)
}

// runPollingLoop drives the daemon until a termination signal arrives.
//
// Both passes are attempted on every tick, and an error in one does not skip
// the other: a source that cannot be imported must not also stop re-imports
// from being noticed.
func runPollingLoop(cfg *config.Config, imp *importer.Importer) {
	ticker := time.NewTicker(time.Duration(cfg.Importer.PollInterval) * time.Second)
	defer ticker.Stop()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	logger.Infof("SourceImporter daemon is running. Checking every %d seconds...", cfg.Importer.PollInterval)

	for {
		select {
		case <-ticker.C:
			// New sources (status: Analysed).
			if err := imp.ProcessSources(); err != nil {
				logger.Errorf("Error processing sources: %v", err)
			}
			// Re-imports (status: Imported, outdated importer version).
			if err := imp.ProcessReImports(); err != nil {
				logger.Errorf("Error processing re-imports: %v", err)
			}

		case sig := <-sigChan:
			logger.Infof("Received signal: %v. Shutting down...", sig)
			logger.Info("Daemon stopped")
			return
		}
	}
}

func runHelp() {
	fmt.Printf("Tachyonik SourceImporter %s\n", version.Version)
	fmt.Println()
	fmt.Println("Usage: sourceimporter [command]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  (none)      Start the SourceImporter daemon (default)")
	fmt.Println("  version     Print version and exit")
	fmt.Println("  help        Show this help message")
}
