// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command actionexecutor is the ActionExecutor daemon entry point. It loads
// configuration, sets up logging, builds the service clients, resolves the
// configured AI chat client, and wires up the AIActionExecutor, the api bridge
// and the runner shared by manual and automatic runs. It then starts the action
// watcher (automatic runs), the execution-rule watcher (rule changes and users'
// run requests) and the SystemManager heartbeat, and runs until it receives
// SIGINT/SIGTERM.
//
// Like the other daemons it serves no HTTP: a user's Execute reaches it as a
// request from AIManager, and the outcome goes back the same way.
//
// The AI is needed only to *generate* an execution routine. Running an
// already-generated routine is plain JavaScript, so the daemon keeps executing
// actions with no AI configured — only code generation is refused.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tachyonik/actionexecutor/internal/actionwatcher"
	"tachyonik/actionexecutor/internal/aiexecutor"
	"tachyonik/actionexecutor/internal/aimanager"
	"tachyonik/actionexecutor/internal/apibridge"
	"tachyonik/actionexecutor/internal/autoexecute"
	"tachyonik/actionexecutor/internal/codegen"
	"tachyonik/actionexecutor/internal/config"
	"tachyonik/actionexecutor/internal/executionrulewatcher"
	"tachyonik/actionexecutor/internal/manualrun"
	"tachyonik/actionexecutor/internal/run"
	"tachyonik/actionexecutor/internal/version"
	"tachyonik/lib/aiclient"
	"tachyonik/lib/aimwatcher"
	"tachyonik/lib/heartbeat"
	"tachyonik/lib/logger"
	"tachyonik/lib/systemmanager"
)

const moduleName = "actionexecutor"

// resolveAI looks up the AI entry from AIManager and creates the appropriate ChatClient.
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

	cfg.AI.Model = entry.Model
	timeout := time.Duration(cfg.AI.TimeoutSeconds) * time.Second

	client := aiclient.ForProvider(entry.Provider, entry.URL, entry.APIKey, timeout)
	if client == nil {
		logger.Warnf("AI '%s' has unsupported provider %q, AI activities disabled", entry.Name, entry.Provider)
		return nil
	}
	logger.Infof("Using %s AI provider '%s' (model: %s)", entry.Provider, entry.Name, entry.Model)
	return client
}

// loadModuleSettings reads this module's AI settings from AIManager into cfg:
// which AI generates routines, and with which system prompt. Used at startup
// and again whenever the settings change or a feed is imported; it used to be
// written out twice.
func loadModuleSettings(cfg *config.Config, aiMgrClient *aimanager.Client) error {
	setting, err := aiMgrClient.GetModuleAISetting(moduleName)
	if err != nil {
		return err
	}
	cfg.AI.AIName = ""
	if setting.AI != nil {
		cfg.AI.AIName = setting.AI.Name
	}
	cfg.AI.SystemPrompt = setting.SystemPrompt
	if cfg.AI.AIName != "" {
		logger.Infof("Module AI settings loaded: AI=%s, SystemPrompt=%d chars", cfg.AI.AIName, len(cfg.AI.SystemPrompt))
	} else {
		logger.Info("Module AI settings loaded: AI unset (disabled)")
	}
	if cfg.AI.SystemPrompt == "" {
		logger.Warn("ActionExecutor system prompt is empty in AIManager — code generation will be refused until configured (Settings → ActionExecutor → System Prompt). Routine execution continues to work.")
	}
	return nil
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
	// Flags are accepted as well as bare words, matching ActionGenerator:
	// `actionexecutor version` and `actionexecutor --version` both work, so
	// neither habit is wrong.
	for _, arg := range os.Args[1:] {
		switch arg {
		case "help", "--help", "-h":
			runHelp()
			return
		case "version", "--version", "-v":
			fmt.Printf("Tachyonik ActionExecutor %s\n", version.Version)
			return
		}
	}

	cfg := config.Load()

	logFile, err := setupLogging(cfg)
	if err != nil {
		logger.Fatalf("Failed to setup logging: %v", err)
	}
	if logFile != nil {
		defer logFile.Close()
	}

	runDaemon(cfg)
}

func runHelp() {
	fmt.Println("Tachyonik ActionExecutor")
	fmt.Println()
	fmt.Println("Usage: actionexecutor [command]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  (none)      Start the ActionExecutor daemon (default)")
	fmt.Println("  help        Show this help message")
	fmt.Println("  version     Print the build version and exit")
}

// runDaemon starts the daemon: load rules, start watchers, pick up waiting run requests.
func runDaemon(cfg *config.Config) {
	logger.Info("Starting Tachyonik ActionExecutor daemon...")
	logger.Info("Configuration loaded:")
	logger.Infof("  AIManager URL: %s", cfg.AIManager.URL)
	logger.Infof("  AI Timeout: %ds", cfg.AI.TimeoutSeconds)
	logger.Infof("  AssetManager URL: %s", cfg.AssetManager.URL)
	logger.Infof("  SystemManager URL: %s", cfg.SystemManager.URL)
	logger.Infof("  ResourceManager URL: %s", cfg.ResourceManager.URL)
	logger.Infof("  ActionManager URL: %s", cfg.ActionManager.URL)
	logger.Infof("  ToolManager URL: %s", cfg.ToolManager.URL)
	logger.Infof("  Log File: %s", cfg.Log.FilePath)
	logger.Infof("  Log Level: %s", cfg.Log.Level)

	// Initialize AIManager client
	aiMgrClient := aimanager.NewClient(cfg.AIManager.URL, cfg.AIManager.InternalServiceKey)

	// Fetch initial AI settings from AIManager. Without them there is no AI
	// to generate with; running routines does not need one.
	if err := loadModuleSettings(cfg, aiMgrClient); err != nil {
		logger.Warnf("Failed to fetch module AI settings from AIManager, AI disabled: %v", err)
		cfg.AI.AIName = ""
	}

	// Create AIActionExecutor with the default AI chat client
	aiExec := aiexecutor.New(aiMgrClient, resolveAI(cfg, aiMgrClient), cfg)

	// Load execution rules and their active routines
	if err := aiExec.LoadRules(); err != nil {
		logger.Errorf("Failed to load execution rules: %v", err)
	}

	// Shared module-settings refresh — used by both the
	// MODULE_AI_SETTING_UPDATED watcher and the FEED_IMPORTED reload
	// path below. See SourceAnalyser's daemon for full rationale.
	refreshModuleSettings := func() {
		if err := loadModuleSettings(cfg, aiMgrClient); err != nil {
			logger.Warnf("Failed to re-fetch module AI settings: %v", err)
			return
		}
		newClient := resolveAI(cfg, aiMgrClient)
		if newClient != nil {
			aiExec.SetChatClient(newClient)
			logger.Info("AI chat client re-resolved successfully via AIManager settings")
		} else {
			logger.Warn("AI re-resolution via AIManager settings resulted in nil client")
		}
	}

	// Start AIManager settings watcher for real-time AI config changes
	aimw := aimwatcher.New(cfg.AIManager.URL, cfg.AIManager.InternalServiceKey, aimwatcher.Handlers{ModuleName: moduleName, OnModuleSettingChange: refreshModuleSettings})
	aimw.Start()
	defer aimw.Close()
	logger.Info("AIManager settings watcher started")

	bridge := apibridge.New(cfg)

	// One runner for manual and automatic runs alike: it reports each result
	// to AIManager and writes the audit entry through SystemManager.
	smClient := systemmanager.NewClient(cfg.SystemManager.URL, cfg.SystemManager.InternalServiceKey)
	runner := run.New(cfg, bridge, aiMgrClient, smClient)

	// Manual runs: a user's Execute arrives as a request from AIManager.
	manualRuns := manualrun.New(aiMgrClient, aiExec, runner)

	// Start execution rule watcher. FEED_IMPORTED triggers a full
	// LoadRules() rebuild plus module-settings refresh; EXECUTION_REQUESTED
	// is a user's request to run a rule now, and every (re)connect picks up
	// the requests made while the connection was down.
	erw, err := executionrulewatcher.New(cfg.AIManager.URL, cfg.AIManager.InternalServiceKey, func(event executionrulewatcher.RuleChangeEvent) {
		logger.Infof("Execution rule %d changed (%s), regenerating...", event.RuleID, event.Type)
		aiExec.HandleRuleChange(event)
	}, func() {
		logger.Info("FEED_IMPORTED: reloading execution rules + module settings")
		if err := aiExec.LoadRules(); err != nil {
			logger.Errorf("Failed to reload execution rules after feed import: %v", err)
		}
		refreshModuleSettings()
	})
	if err != nil {
		logger.Errorf("Failed to create execution rule watcher: %v", err)
	} else {
		erw.SetRunRequestHandler(manualRuns.OnRequested)
		erw.SetConnectHandler(manualRuns.PickUpPending)
		erw.Start()
		defer erw.Close()
		logger.Info("Execution rule watcher started")
	}

	// Create auto-execute handler and start action watcher
	autoExecHandler := autoexecute.New(aiMgrClient, aiExec, runner, bridge, cfg)
	aw := actionwatcher.New(cfg.ActionManager.URL, cfg.ActionManager.InternalServiceKey, func(action actionwatcher.Action) {
		go autoExecHandler.OnActionCreated(action)
	})
	aw.Start()
	defer aw.Close()
	logger.Info("ActionManager action watcher started (auto-execute)")

	// Start liveness heartbeat to SystemManager.
	heartbeat.Start(context.Background(), heartbeat.Config{
		ServiceName:        "ActionExecutor",
		IntervalSeconds:    cfg.Heartbeat.IntervalSeconds,
		SystemManagerURL:   cfg.SystemManager.URL,
		InternalServiceKey: cfg.SystemManager.InternalServiceKey,
	}, logger.Errorf)

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	logger.Info("ActionExecutor daemon is running. Press Ctrl+C to stop.")
	<-quit

	logger.Info("Shutting down ActionExecutor daemon...")
	logger.Info("Daemon stopped")
}
