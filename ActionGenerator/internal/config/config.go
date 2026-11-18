// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package config loads the daemon's configuration from, in increasing order of
// precedence, built-in defaults, config.yaml and environment variables.
//
// It covers the endpoints ActionGenerator talks to (AIManager, AssetManager,
// ActionManager, ResourceManager, SystemManager, ToolManager) with their
// internal service keys, the SystemManager heartbeat, the JS execution budget, and logging. The AI
// section holds only what a bootstrap needs: the authoritative AI assignment and
// system prompt are fetched from AIManager at startup and refreshed live.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"

	"gopkg.in/yaml.v3"
	"tachyonik/lib/settings"
)

type Config struct {
	AssetManager    ServiceConfig
	SystemManager   ServiceConfig
	ResourceManager ServiceConfig
	ActionManager   ServiceConfig
	AIManager       ServiceConfig
	// ToolManager answers capabilities.get for bridge routines.
	ToolManager ServiceConfig
	AI          AIConfig
	Heartbeat   HeartbeatConfig
	Log         LogConfig
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
	ToolManager     FileServiceConfig `yaml:"toolmanager"`
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
//
// It refuses a configuration it cannot use: a file that exists but cannot
// be read or parsed, or a key it does not know (a misspelt key would leave
// its setting at the default without a word). Running on defaults the
// operator did not mean is worse than not starting. A missing file is fine:
// the defaults and the environment run a local development setup. A value
// it cannot use — from the file or the environment — is refused as well
// (AG-OPS-15), naming the setting and where it came from.
func Load() (*Config, error) {
	// 1. Set built-in defaults
	cfg := &Config{
		AssetManager:    ServiceConfig{URL: "http://localhost:8081"},
		SystemManager:   ServiceConfig{URL: "http://localhost:8083"},
		ResourceManager: ServiceConfig{URL: "http://localhost:8080"},
		ActionManager:   ServiceConfig{URL: "http://localhost:8082"},
		AIManager:       ServiceConfig{URL: "http://localhost:8085"},
		ToolManager:     ServiceConfig{URL: "http://localhost:8086"},
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
	fileData, err := os.ReadFile(configPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		log.Printf("No configuration file at %s; using the defaults and the environment", configPath)
	case err != nil:
		return nil, fmt.Errorf("configuration file %s cannot be read: %w", configPath, err)
	default:
		var fc FileConfig
		if err := decodeStrict(fileData, &fc); err != nil {
			return nil, fmt.Errorf("configuration file %s cannot be used: %w", configPath, err)
		}
		fileCfg = &fc
	}

	// 3. Apply config.yaml on top of defaults, then 4. the environment on
	// top (overrides everything). The reader collects every value that
	// cannot be used, from both, and stops the daemon with the whole list
	// (AG-OPS-15).
	r := settings.NewReader(configPath)
	if fileCfg != nil {
		applyFileConfig(cfg, fileCfg, r)
	}
	applyEnvOverrides(cfg, r)
	if err := r.Err(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// applyFileConfig applies non-zero values from the parsed config.yaml onto cfg.
// A 0 reads as an absent key (YAML cannot tell them apart, AG-OPS-03); a
// negative number or an unknown log level is recorded in r (AG-OPS-15).
func applyFileConfig(cfg *Config, fileCfg *FileConfig, r *settings.Reader) {
	for _, svc := range []struct {
		dst *ServiceConfig
		src FileServiceConfig
	}{
		{&cfg.AssetManager, fileCfg.AssetManager},
		{&cfg.SystemManager, fileCfg.SystemManager},
		{&cfg.ResourceManager, fileCfg.ResourceManager},
		{&cfg.ActionManager, fileCfg.ActionManager},
		{&cfg.AIManager, fileCfg.AIManager},
		{&cfg.ToolManager, fileCfg.ToolManager},
	} {
		if svc.src.URL != "" {
			svc.dst.URL = svc.src.URL
		}
		if svc.src.InternalServiceKey != "" {
			svc.dst.InternalServiceKey = svc.src.InternalServiceKey
		}
	}
	r.FileInt("ai.timeout_seconds", fileCfg.AI.TimeoutSeconds, &cfg.AI.TimeoutSeconds, settings.Min(1))
	r.FileInt("ai.js_exec_timeout_seconds", fileCfg.AI.JSExecTimeoutSeconds, &cfg.AI.JSExecTimeoutSeconds, settings.Min(1))
	r.FileInt("heartbeat.interval_seconds", fileCfg.Heartbeat.IntervalSeconds, &cfg.Heartbeat.IntervalSeconds, settings.Min(1))
	if fileCfg.Log.FilePath != "" {
		cfg.Log.FilePath = fileCfg.Log.FilePath
	}
	r.FileLevel("log.level", fileCfg.Log.Level, &cfg.Log.Level)
	cfg.Log.ToConsole = fileCfg.Log.ToConsole
	cfg.Log.ToFile = fileCfg.Log.ToFile
}

// applyEnvOverrides applies environment variable overrides onto cfg. An empty
// variable counts as unset; a value it cannot use is recorded in r
// (AG-OPS-15).
func applyEnvOverrides(cfg *Config, r *settings.Reader) {
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
		{&cfg.ToolManager, "TOOLMANAGER_URL", "TOOLMANAGER_INTERNAL_SERVICE_KEY"},
	} {
		if url := os.Getenv(svc.urlEnv); url != "" {
			svc.dst.URL = url
		}
		if key := os.Getenv(svc.keyEnv); key != "" {
			svc.dst.InternalServiceKey = key
		}
	}
	r.Int("ACTIONGENERATOR_AI_TIMEOUT_SECONDS", &cfg.AI.TimeoutSeconds, settings.Min(1))
	// 0 is a value here: the JS budget switched off (AG-OPS-03).
	r.Int("ACTIONGENERATOR_JS_EXEC_TIMEOUT_SECONDS", &cfg.AI.JSExecTimeoutSeconds, settings.Min(1), settings.ZeroMeansOff())
	r.Int("ACTIONGENERATOR_HEARTBEAT_INTERVAL_SECONDS", &cfg.Heartbeat.IntervalSeconds, settings.Min(1))
	if logPath := os.Getenv("ACTIONGENERATOR_LOG_FILE"); logPath != "" {
		cfg.Log.FilePath = logPath
	}
	r.Bool("ACTIONGENERATOR_LOG_TO_CONSOLE", &cfg.Log.ToConsole)
	r.Bool("ACTIONGENERATOR_LOG_TO_FILE", &cfg.Log.ToFile)
	r.Level("ACTIONGENERATOR_LOG_LEVEL", &cfg.Log.Level)
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// decodeStrict parses YAML into out, refusing a key out does not have. An
// empty file is an empty configuration.
func decodeStrict(data []byte, out interface{}) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
