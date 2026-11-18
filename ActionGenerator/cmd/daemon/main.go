// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command actiongenerator is the ActionGenerator daemon entry point. It loads
// configuration, sets up logging, builds the AIManager/ActionManager/
// SystemManager clients and the platform API bridge, resolves the configured
// AI chat client, and wires up the AIActionGenerator and the Generator. It then
// starts the watchers (the managers' change messages, live action-rule
// changes) and the SystemManager heartbeat, and runs the loop that evaluates
// rules until it receives SIGINT/SIGTERM.
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
	"sync/atomic"
	"syscall"
	"time"

	"tachyonik/actiongenerator/internal/actionmanager"
	"tachyonik/actiongenerator/internal/actionrulewatcher"
	"tachyonik/actiongenerator/internal/aigenerator"
	"tachyonik/actiongenerator/internal/aimanager"
	"tachyonik/actiongenerator/internal/codegen"
	"tachyonik/actiongenerator/internal/config"
	"tachyonik/actiongenerator/internal/generator"
	"tachyonik/actiongenerator/internal/systemmanager"
	"tachyonik/actiongenerator/internal/version"
	"tachyonik/actiongenerator/internal/watcher"
	"tachyonik/lib/aiclient"
	"tachyonik/lib/aimwatcher"
	"tachyonik/lib/apibridge"
	"tachyonik/lib/apiholder"
	"tachyonik/lib/heartbeat"
	"tachyonik/lib/logger"
	"tachyonik/lib/reload"
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
	// Route subcommand before anything else. Asking a binary its version must
	// work wherever the binary does — from a package script, as another user,
	// on a host where the configured log path is not writable — and
	// setupLogging is fatal on a log file it cannot open. Neither `version`
	// nor `help` needs the configuration, so neither should be able to fail on
	// it.
	//
	// Flags are accepted as well as bare words, matching TachyonikProxy:
	// `actiongenerator version` and `actiongenerator --version` both
	// work, so neither habit is wrong.
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

	cfg, err := config.Load()
	if err != nil {
		// Before logging is set up: standard error reaches journalctl and
		// docker logs. Running on defaults instead would be worse.
		fmt.Fprintf(os.Stderr, "ActionGenerator cannot start: %v\n", err)
		os.Exit(1)
	}

	logFile, err := setupLogging(cfg)
	if err != nil {
		logger.Fatalf("Failed to setup logging: %v", err)
	}
	if logFile != nil {
		defer logFile.Close()
	}
	logger.Startup("ActionGenerator", version.Version)

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
	// Generation requests made before this moment are left behind by an
	// earlier run (AG-GEN-02).
	startedAt := time.Now()
	logConfiguration(cfg)

	c := newClients(cfg)
	systemMgrClient, actionMgrClient, aiMgrClient := c.systemMgr, c.actionMgr, c.aiMgr

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
	gen := generator.New(systemMgrClient, actionMgrClient)
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

	// The platform API catalogue, and the bridge built from it, that the
	// routines read through (docs/specs/01-platform-api-catalogue.md). Loaded
	// before the rules, so a routine is checked against it as it is loaded.
	apiCatalogue := newAPICatalogue(cfg)
	loadAPICatalogue := func() {
		cat, err := aiMgrClient.GetAPICatalogue()
		if err != nil {
			logger.Warnf("The platform API catalogue cannot be read yet, no routine runs until it can: %v", err)
			return
		}
		docs := make([]apiholder.Document, len(cat.Documents))
		for i, d := range cat.Documents {
			docs[i] = apiholder.Document{Module: d.Module, Document: d.Document}
		}
		if err := apiCatalogue.Load(cat.Fingerprint, docs); err != nil {
			logger.Warnf("API catalogue: %v", err)
		}
		logger.Infof("API catalogue loaded: %d documents, fingerprint %.12s", len(cat.Documents), cat.Fingerprint)
		for _, gap := range apiCatalogue.Gaps() {
			logger.Warnf("API catalogue: %s", gap)
		}
	}
	loadAPICatalogue()
	aiGen.SetCatalogue(apiCatalogue)
	gen.SetCatalogue(apiCatalogue)

	// The message types an active routine's answers depend on (PA-RT-07),
	// recomputed whenever the rules or the catalogue change and read by every
	// watcher on every message.
	var triggers atomic.Value
	triggers.Store(map[string]bool{})
	refreshTriggers := func() { triggers.Store(aiGen.Triggers()) }
	extraTrigger := func(messageType string) bool { return triggers.Load().(map[string]bool)[messageType] }

	// The rules with their active routines (no AI needed). Loaded now, and
	// again on every reconnect to AIManager and after a feed import; a load
	// that fails is tried again every 30 s until one succeeds (AG-OPS-08).
	// The first load that succeeds clears the generation requests an earlier
	// run left standing (AG-GEN-02).
	var requestsCleared atomic.Bool
	loadRules := func() error {
		if err := aiGen.LoadRules(); err != nil {
			return err
		}
		gen.SetExecutors(aiGen.Executors())
		gen.SetOnDemandRules(aiGen.OnDemandRules())
		refreshTriggers()
		if requestsCleared.CompareAndSwap(false, true) {
			aiGen.ClearStandingRequests(startedAt)
		}
		return nil
	}
	loadedAtStartup := true
	if err := loadRules(); err != nil {
		logger.Errorf("Failed to load action rules, trying again every %s: %v", reload.DefaultRetry, err)
		loadedAtStartup = false
	}
	refreshTriggers()

	// Every full pass goes through one runner: never two at once
	// (AG-EVAL-03), and a pass asked for while one runs follows it
	// (AG-EVAL-04). Change messages are debounced (AG-EVAL-02); the other
	// triggers ask for a pass at once.
	passes := generator.NewPassRunner(func() {
		if err := gen.ProcessRules(); err != nil {
			logger.Errorf("Error processing rules: %v", err)
		}
	}, 2*time.Second)
	onChange := passes.Debounced

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

	// The watchers that make rules evaluate, closed first on a stop
	// (AG-OPS-09): from then on nothing new is reacted to.
	var stopWatching []func() error
	stopWatching = append(stopWatching, func() error { passes.Stop(); return nil })

	// A later load is followed by a pass over every rule, as at startup.
	ruleLoader := reload.New("the action rules", func() error {
		if err := loadRules(); err != nil {
			return err
		}
		passes.Now()
		return nil
	})
	ruleLoader.Start()
	stopWatching = append(stopWatching, func() error { ruleLoader.Stop(); return nil })
	if !loadedAtStartup {
		// Also when AIManager's WebSocket stays down while its API is back.
		time.AfterFunc(reload.DefaultRetry, ruleLoader.Request)
	}

	// Start action rule watcher for rule changes and evaluation requests
	arw, err := actionrulewatcher.New(cfg.AIManager.URL, cfg.AIManager.InternalServiceKey, func(event actionrulewatcher.RuleChangeEvent) {
		logger.Infof("Action rule %d changed (%s), regenerating...", event.RuleID, event.Type)

		aiGen.HandleRuleChange(event)

		// Update executor map on generator and re-process rules
		gen.SetExecutors(aiGen.Executors())
		gen.SetOnDemandRules(aiGen.OnDemandRules())
		refreshTriggers()
		logger.Info("Re-processing rules after regeneration...")
		passes.Now()
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
		// FEED_IMPORTED refreshes module settings and reloads the rules,
		// followed by a pass, so freshly imported active routines start
		// firing immediately.
		arw.SetFeedImportedHandler(func() {
			logger.Info("FEED_IMPORTED: reloading action rules + module settings")
			refreshModuleSettings()
			ruleLoader.Request()
		})
		// Every (re)connect loads the rules again: a connection that was down
		// has missed the changes announced meanwhile, and a startup that could
		// not reach AIManager has no rules yet (AG-OPS-08). The first
		// connection follows a successful startup load directly and needs none.
		var connectedBefore atomic.Bool
		arw.SetConnectHandler(func() {
			if !connectedBefore.Swap(true) && loadedAtStartup {
				return
			}
			logger.Info("Connected to AIManager: loading the action rules again")
			ruleLoader.Request()
		})
		// A changed catalogue rebuilds the bridge and re-checks the active
		// routines against it (PA-RT-06, -08).
		arw.SetCatalogueUpdatedHandler(func() {
			loadAPICatalogue()
			if err := aiGen.LoadRules(); err != nil {
				logger.Errorf("Failed to reload action rules after the API catalogue changed: %v", err)
			} else {
				gen.SetExecutors(aiGen.Executors())
				gen.SetOnDemandRules(aiGen.OnDemandRules())
			}
			refreshTriggers()
			passes.Now()
		})
		// AIManager's own messages that invalidate a routine's answers —
		// the tool catalogue's, for instance.
		arw.SetOtherMessageHandler(func(messageType string) {
			if extraTrigger(messageType) {
				onChange()
			}
		})
		arw.Start()
		stopWatching = append(stopWatching, arw.Close)
		logger.Info("Action rule watcher started")
	}

	// Process rules on startup
	logger.Info("Processing rules on startup...")
	passes.Now()

	// WebSocket watchers for the managers' change messages. What a routine
	// reads is decided by the operations it calls, so what re-evaluates is the
	// x-invalidated-by of those operations (PA-RT-07, AG-IF-24), plus, from
	// SystemManager, a user appearing or going — which changes whom the rules
	// are evaluated for (AG-IF-18). All share onChange, so messages arriving
	// together collapse into one debounced re-evaluation, and each re-evaluates
	// on (re)connect.
	//
	// ActionGenerator's own creating and withdrawing is announced by
	// ActionManager too; the pass that follows finds nothing to change.
	for _, m := range []struct {
		name     string
		svc      config.ServiceConfig
		triggers []watcher.MessageType
	}{
		{"AssetManager", cfg.AssetManager, nil},
		{"SystemManager", cfg.SystemManager, watcher.SystemTriggers},
		{"ResourceManager", cfg.ResourceManager, nil},
		{"ActionManager", cfg.ActionManager, nil},
	} {
		w, err := watcher.NewFor(m.name, m.svc.URL, m.svc.InternalServiceKey, m.triggers, onChange)
		if err != nil {
			logger.Errorf("Failed to create %s watcher: %v", m.name, err)
			continue
		}
		stopWatching = append(stopWatching, w.Close)
		w.SetExtraTriggers(extraTrigger)
		w.Start()
		logger.Infof("%s WebSocket watcher started", m.name)
	}

	// Start liveness heartbeat to SystemManager.
	heartbeatCtx, stopHeartbeat := context.WithCancel(context.Background())
	heartbeat.Start(heartbeatCtx, heartbeat.Config{
		ServiceName:        "ActionGenerator",
		IntervalSeconds:    cfg.Heartbeat.IntervalSeconds,
		SystemManagerURL:   cfg.SystemManager.URL,
		InternalServiceKey: cfg.SystemManager.InternalServiceKey,
	}, logger.Errorf)

	waitForShutdown()

	// AG-OPS-09, in its order: stop reacting to messages, let the evaluation
	// in progress finish, close the connections and exit. A running pass stops
	// after the rule and user it is on; the rest is evaluated at the next
	// start. A generation in progress is not waited for — it takes minutes —
	// and its request is cleared at the next start (AG-GEN-02).
	for _, closeWatcher := range stopWatching {
		_ = closeWatcher()
	}
	gen.Stop()
	if unit := gen.Wait(shutdownDrain); unit != "" {
		logger.Warnf("Stopping although %s is still being evaluated after %s; it is cut off", unit, shutdownDrain)
	}
	stopHeartbeat()
	logger.Info("Daemon stopped")
}

// shutdownDrain bounds how long a stop waits for the unit being evaluated.
// The container is given 30 s (stop_grace_period in compose.yaml).
const shutdownDrain = 20 * time.Second

// newAPICatalogue makes the holder of the platform API catalogue, with where
// this daemon reaches each manager. A bridge call is bounded by the shorter
// of 3 seconds and the routine's own execution budget (PA-BR-10).
func newAPICatalogue(cfg *config.Config) *apiholder.Holder {
	callTimeout := 3 * time.Second
	if js := time.Duration(cfg.AI.JSExecTimeoutSeconds) * time.Second; js > 0 && js < callTimeout {
		callTimeout = js
	}
	ep := func(s config.ServiceConfig) apiholder.Endpoint {
		return apiholder.Endpoint{BaseURL: s.URL, ServiceKey: s.InternalServiceKey}
	}
	return apiholder.New(map[string]apiholder.Endpoint{
		"assetmanager":    ep(cfg.AssetManager),
		"systemmanager":   ep(cfg.SystemManager),
		"resourcemanager": ep(cfg.ResourceManager),
		"actionmanager":   ep(cfg.ActionManager),
		"aimanager":       ep(cfg.AIManager),
		"toolmanager":     ep(cfg.ToolManager),
	}, apibridge.ReadOnly, apibridge.Options{CallTimeout: callTimeout})
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
	logger.Infof("  ToolManager URL: %s", cfg.ToolManager.URL)
	logger.Infof("  Log File: %s", cfg.Log.FilePath)
	logger.Infof("  Log to Console: %v", cfg.Log.ToConsole)
	logger.Infof("  Log to File: %v", cfg.Log.ToFile)
	logger.Infof("  Log Level: %s", cfg.Log.Level)
}

// clients are this daemon's handles on the platform's services.
type clients struct {
	systemMgr *systemmanager.Client
	actionMgr *actionmanager.Client
	aiMgr     *aimanager.Client
}

// newClients builds them all. Constructing a client performs no I/O, so there
// is nothing here that can fail — the first request is where a wrong URL or a
// missing key shows up.
func newClients(cfg *config.Config) *clients {
	c := &clients{
		systemMgr: systemmanager.NewClient(cfg.SystemManager.URL, cfg.SystemManager.InternalServiceKey),
		actionMgr: actionmanager.NewClient(cfg.ActionManager.URL, cfg.ActionManager.InternalServiceKey),
		aiMgr:     aimanager.NewClient(cfg.AIManager.URL, cfg.AIManager.InternalServiceKey),
	}
	logger.Info("API clients initialized")
	return c
}

// waitForShutdown blocks until the service is asked to stop; runDaemon then
// runs the stop sequence of AG-OPS-09.
func waitForShutdown() {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	logger.Info("ActionGenerator daemon is running. Press Ctrl+C to stop.")
	<-quit

	logger.Info("Shutting down ActionGenerator daemon...")
}
