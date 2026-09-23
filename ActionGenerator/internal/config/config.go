// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package config loads the daemon's configuration from, in increasing order of
// precedence, built-in defaults, config.yaml and environment variables.
//
// It covers the endpoints ActionGenerator talks to (AIManager, AssetManager,
// ActionManager, ResourceManager, SystemManager) with their internal service
// keys, the SystemManager heartbeat, the JS execution budget, and logging. The AI
// section holds only what a bootstrap needs: the authoritative AI assignment and
// system prompt are fetched from AIManager at startup and refreshed live.
package config

import (
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	AssetManager    ServiceConfig
	SystemManager   ServiceConfig
	ResourceManager ServiceConfig
	ActionManager   ServiceConfig
	AIManager       ServiceConfig
	AI              AIConfig
	Heartbeat       HeartbeatConfig
	Log             LogConfig
}

// ServiceConfig is how this daemon reaches one of the platform's services.
// Every one of them is addressed the same way, so they share a type rather than
// five identical ones — which is what lets the file and environment overrides
// below be a table instead of the same six lines repeated per service.
type ServiceConfig struct {
	URL                string
	InternalServiceKey string
}

// HeartbeatConfig controls the liveness signal sent to SystemManager.
type HeartbeatConfig struct {
	IntervalSeconds int
}

type AIConfig struct {
	AIName         string // resolved from AIManager at runtime
	Model          string // resolved from AIManager at runtime
	SystemPrompt   string // resolved from AIManager at runtime
	TimeoutSeconds int    // HTTP client timeout for AI requests (default 300)
	// JSExecTimeoutSeconds bounds a single JS execution: loading a routine,
	// one check(), one createAction(). goja cannot be preempted, so without it
	// a generated rule that does not terminate hangs its goroutine for good —
	// and the code being run here was written by an AI from a prompt.
	// 0 disables the guard.
	JSExecTimeoutSeconds int
}

type LogConfig struct {
	FilePath  string
	ToConsole bool
	ToFile    bool
	Level     string
}

// FileConfig represents the structure of config.yaml
// FileServiceConfig is one service's block in config.yaml.
type FileServiceConfig struct {
	URL                string `yaml:"url"`
	InternalServiceKey string `yaml:"internal_service_key"`
}

type FileConfig struct {
	AssetManager    FileServiceConfig `yaml:"assetmanager"`
	SystemManager   FileServiceConfig `yaml:"systemmanager"`
	ResourceManager FileServiceConfig `yaml:"resourcemanager"`
	ActionManager   FileServiceConfig `yaml:"actionmanager"`
	AIManager       FileServiceConfig `yaml:"ai_manager"`
	AI              struct {
		TimeoutSeconds       int `yaml:"timeout_seconds"`
		JSExecTimeoutSeconds int `yaml:"js_exec_timeout_seconds"`
	} `yaml:"ai"`
	Heartbeat struct {
		IntervalSeconds int `yaml:"interval_seconds"`
	} `yaml:"heartbeat"`
	Log struct {
		FilePath  string `yaml:"file_path"`
		ToConsole bool   `yaml:"to_console"`
		ToFile    bool   `yaml:"to_file"`
		Level     string `yaml:"level"`
	} `yaml:"log"`
}

// Load loads configuration with priority: env vars > config.yaml > defaults
func Load() *Config {
	// 1. Set built-in defaults
	cfg := &Config{
		AssetManager:    ServiceConfig{URL: "http://localhost:8081"},
		SystemManager:   ServiceConfig{URL: "http://localhost:8083"},
		ResourceManager: ServiceConfig{URL: "http://localhost:8080"},
		ActionManager:   ServiceConfig{URL: "http://localhost:8082"},
		AIManager:       ServiceConfig{URL: "http://localhost:8085"},
		AI: AIConfig{
			TimeoutSeconds:       300,
			JSExecTimeoutSeconds: 5,
		},
		Heartbeat: HeartbeatConfig{
			IntervalSeconds: 10,
		},
		Log: LogConfig{
			FilePath:  "./actiongenerator.log",
			ToConsole: true,
			ToFile:    true,
			Level:     "INFO",
		},
	}

	// 2. Parse config.yaml
	var fileCfg *FileConfig
	configPath := getEnv("ACTIONGENERATOR_CONFIG", "./config.yaml")
	if fileData, err := os.ReadFile(configPath); err == nil {
		var fc FileConfig
		if err := yaml.Unmarshal(fileData, &fc); err == nil {
			fileCfg = &fc
		}
	}

	// 3. Apply config.yaml on top of defaults
	if fileCfg != nil {
		applyFileConfig(cfg, fileCfg)
	}

	// 4. Apply env var overrides on top (overrides everything)
	applyEnvOverrides(cfg)

	return cfg
}

// applyFileConfig applies non-zero values from the parsed config.yaml onto cfg.
func applyFileConfig(cfg *Config, fileCfg *FileConfig) {
	for _, svc := range []struct {
		dst *ServiceConfig
		src FileServiceConfig
	}{
		{&cfg.AssetManager, fileCfg.AssetManager},
		{&cfg.SystemManager, fileCfg.SystemManager},
		{&cfg.ResourceManager, fileCfg.ResourceManager},
		{&cfg.ActionManager, fileCfg.ActionManager},
		{&cfg.AIManager, fileCfg.AIManager},
	} {
		if svc.src.URL != "" {
			svc.dst.URL = svc.src.URL
		}
		if svc.src.InternalServiceKey != "" {
			svc.dst.InternalServiceKey = svc.src.InternalServiceKey
		}
	}
	if fileCfg.AI.TimeoutSeconds > 0 {
		cfg.AI.TimeoutSeconds = fileCfg.AI.TimeoutSeconds
	}
	if fileCfg.AI.JSExecTimeoutSeconds > 0 {
		cfg.AI.JSExecTimeoutSeconds = fileCfg.AI.JSExecTimeoutSeconds
	}
	if fileCfg.Heartbeat.IntervalSeconds > 0 {
		cfg.Heartbeat.IntervalSeconds = fileCfg.Heartbeat.IntervalSeconds
	}
	if fileCfg.Log.FilePath != "" {
		cfg.Log.FilePath = fileCfg.Log.FilePath
	}
	if fileCfg.Log.Level != "" {
		cfg.Log.Level = fileCfg.Log.Level
	}
	cfg.Log.ToConsole = fileCfg.Log.ToConsole
	cfg.Log.ToFile = fileCfg.Log.ToFile
}

// applyEnvOverrides applies environment variable overrides onto cfg.
func applyEnvOverrides(cfg *Config) {
	// The variable names are not derivable from the field names — AIManager's
	// URL is read from ACTIONGENERATOR_AI_MANAGER_URL while its key comes from
	// the plain AIMANAGER_INTERNAL_SERVICE_KEY — so each pair is named here
	// rather than built from a prefix. Deployments already set these.
	for _, svc := range []struct {
		dst    *ServiceConfig
		urlEnv string
		keyEnv string
	}{
		{&cfg.AssetManager, "ASSETMANAGER_URL", "ASSETMANAGER_INTERNAL_SERVICE_KEY"},
		{&cfg.SystemManager, "SYSTEMMANAGER_URL", "SYSTEMMANAGER_INTERNAL_SERVICE_KEY"},
		{&cfg.ResourceManager, "RESOURCEMANAGER_URL", "RESOURCEMANAGER_INTERNAL_SERVICE_KEY"},
		{&cfg.ActionManager, "ACTIONMANAGER_URL", "ACTIONMANAGER_INTERNAL_SERVICE_KEY"},
		{&cfg.AIManager, "ACTIONGENERATOR_AI_MANAGER_URL", "AIMANAGER_INTERNAL_SERVICE_KEY"},
	} {
		if url := os.Getenv(svc.urlEnv); url != "" {
			svc.dst.URL = url
		}
		if key := os.Getenv(svc.keyEnv); key != "" {
			svc.dst.InternalServiceKey = key
		}
	}
	if timeoutStr := os.Getenv("ACTIONGENERATOR_AI_TIMEOUT_SECONDS"); timeoutStr != "" {
		if v, err := strconv.Atoi(timeoutStr); err == nil && v > 0 {
			cfg.AI.TimeoutSeconds = v
		}
	}
	if jsTimeout := os.Getenv("ACTIONGENERATOR_JS_EXEC_TIMEOUT_SECONDS"); jsTimeout != "" {
		// >= 0: zero is the meaningful value "no guard", distinct from unparseable.
		if n, err := strconv.Atoi(jsTimeout); err == nil && n >= 0 {
			cfg.AI.JSExecTimeoutSeconds = n
		}
	}
	if interval := os.Getenv("ACTIONGENERATOR_HEARTBEAT_INTERVAL_SECONDS"); interval != "" {
		if n, err := strconv.Atoi(interval); err == nil && n > 0 {
			cfg.Heartbeat.IntervalSeconds = n
		}
	}
	if logPath := os.Getenv("ACTIONGENERATOR_LOG_FILE"); logPath != "" {
		cfg.Log.FilePath = logPath
	}
	if logConsole := os.Getenv("ACTIONGENERATOR_LOG_TO_CONSOLE"); logConsole != "" {
		cfg.Log.ToConsole = logConsole == "true" || logConsole == "1"
	}
	if logFile := os.Getenv("ACTIONGENERATOR_LOG_TO_FILE"); logFile != "" {
		cfg.Log.ToFile = logFile == "true" || logFile == "1"
	}
	if logLevel := os.Getenv("ACTIONGENERATOR_LOG_LEVEL"); logLevel != "" {
		cfg.Log.Level = logLevel
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
