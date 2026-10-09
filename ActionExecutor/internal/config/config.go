// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package config loads the daemon's configuration from, in increasing order of
// precedence, built-in defaults, config.yaml and environment variables.
//
// It covers the endpoints ActionExecutor talks to (AIManager, ActionManager,
// AssetManager, ResourceManager, SystemManager, ToolManager) with their
// internal service keys, the SystemManager heartbeat, the time limit of a
// routine run, and logging. There is no server section: ActionExecutor serves
// no HTTP. The AI section holds only what a bootstrap needs: the authoritative
// AI assignment and system prompt are fetched from AIManager at startup and
// refreshed live.
package config

import (
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	AssetManager    AssetManagerConfig
	SystemManager   SystemManagerConfig
	ResourceManager ResourceManagerConfig
	ActionManager   ActionManagerConfig
	ToolManager     ToolManagerConfig
	AIManager       AIManagerConfig
	AI              AIConfig
	Heartbeat       HeartbeatConfig
	Execution       ExecutionConfig
	Log             LogConfig
}

// ExecutionConfig bounds a real routine run, manual or automatic.
type ExecutionConfig struct {
	// TimeoutSeconds is how long one run may take before its JavaScript is
	// interrupted and the run counts as failed.
	TimeoutSeconds int
}

// HeartbeatConfig controls the liveness signal sent to SystemManager.
type HeartbeatConfig struct {
	IntervalSeconds int
}

type AIManagerConfig struct {
	URL                string
	InternalServiceKey string
}

type AIConfig struct {
	AIName         string
	Model          string
	SystemPrompt   string
	TimeoutSeconds int
}

type AssetManagerConfig struct {
	URL                string
	InternalServiceKey string
}

type SystemManagerConfig struct {
	URL                string
	InternalServiceKey string
}

type ResourceManagerConfig struct {
	URL                string
	InternalServiceKey string
}

type ActionManagerConfig struct {
	URL                string
	InternalServiceKey string
}

type ToolManagerConfig struct {
	URL                string
	InternalServiceKey string
}

type LogConfig struct {
	FilePath  string
	ToConsole bool
	ToFile    bool
	Level     string
}

// FileConfig represents the structure of config.yaml
type FileConfig struct {
	AssetManager struct {
		URL                string `yaml:"url"`
		InternalServiceKey string `yaml:"internal_service_key"`
	} `yaml:"assetmanager"`
	SystemManager struct {
		URL                string `yaml:"url"`
		InternalServiceKey string `yaml:"internal_service_key"`
	} `yaml:"systemmanager"`
	ResourceManager struct {
		URL                string `yaml:"url"`
		InternalServiceKey string `yaml:"internal_service_key"`
	} `yaml:"resourcemanager"`
	ActionManager struct {
		URL                string `yaml:"url"`
		InternalServiceKey string `yaml:"internal_service_key"`
	} `yaml:"actionmanager"`
	ToolManager struct {
		URL                string `yaml:"url"`
		InternalServiceKey string `yaml:"internal_service_key"`
	} `yaml:"toolmanager"`
	AIManager struct {
		URL                string `yaml:"url"`
		InternalServiceKey string `yaml:"internal_service_key"`
	} `yaml:"ai_manager"`
	AI struct {
		TimeoutSeconds int `yaml:"timeout_seconds"`
	} `yaml:"ai"`
	Heartbeat struct {
		IntervalSeconds int `yaml:"interval_seconds"`
	} `yaml:"heartbeat"`
	Execution struct {
		TimeoutSeconds int `yaml:"timeout_seconds"`
	} `yaml:"execution"`
	Log struct {
		FilePath  string `yaml:"file_path"`
		ToConsole bool   `yaml:"to_console"`
		ToFile    bool   `yaml:"to_file"`
		Level     string `yaml:"level"`
	} `yaml:"log"`
}

// Load loads configuration with priority: env vars > config.yaml > defaults
func Load() *Config {
	cfg := &Config{
		AssetManager: AssetManagerConfig{
			URL: "http://localhost:8081",
		},
		SystemManager: SystemManagerConfig{
			URL: "http://localhost:8083",
		},
		ResourceManager: ResourceManagerConfig{
			URL: "http://localhost:8080",
		},
		ActionManager: ActionManagerConfig{
			URL: "http://localhost:8082",
		},
		ToolManager: ToolManagerConfig{
			URL: "http://localhost:8086",
		},
		AIManager: AIManagerConfig{
			URL: "http://localhost:8085",
		},
		AI: AIConfig{
			TimeoutSeconds: 300,
		},
		Heartbeat: HeartbeatConfig{
			IntervalSeconds: 10,
		},
		Execution: ExecutionConfig{
			TimeoutSeconds: 280,
		},
		Log: LogConfig{
			FilePath:  "./actionexecutor.log",
			ToConsole: true,
			ToFile:    true,
			Level:     "INFO",
		},
	}

	var fileCfg *FileConfig
	configPath := getEnv("ACTIONEXECUTOR_CONFIG", "./config.yaml")
	if fileData, err := os.ReadFile(configPath); err == nil {
		var fc FileConfig
		if err := yaml.Unmarshal(fileData, &fc); err == nil {
			fileCfg = &fc
		}
	}

	if fileCfg != nil {
		applyFileConfig(cfg, fileCfg)
	}

	applyEnvOverrides(cfg)

	return cfg
}

func applyFileConfig(cfg *Config, fileCfg *FileConfig) {
	if fileCfg.AssetManager.URL != "" {
		cfg.AssetManager.URL = fileCfg.AssetManager.URL
	}
	if fileCfg.AssetManager.InternalServiceKey != "" {
		cfg.AssetManager.InternalServiceKey = fileCfg.AssetManager.InternalServiceKey
	}
	if fileCfg.SystemManager.URL != "" {
		cfg.SystemManager.URL = fileCfg.SystemManager.URL
	}
	if fileCfg.SystemManager.InternalServiceKey != "" {
		cfg.SystemManager.InternalServiceKey = fileCfg.SystemManager.InternalServiceKey
	}
	if fileCfg.ResourceManager.URL != "" {
		cfg.ResourceManager.URL = fileCfg.ResourceManager.URL
	}
	if fileCfg.ResourceManager.InternalServiceKey != "" {
		cfg.ResourceManager.InternalServiceKey = fileCfg.ResourceManager.InternalServiceKey
	}
	if fileCfg.ActionManager.URL != "" {
		cfg.ActionManager.URL = fileCfg.ActionManager.URL
	}
	if fileCfg.ActionManager.InternalServiceKey != "" {
		cfg.ActionManager.InternalServiceKey = fileCfg.ActionManager.InternalServiceKey
	}
	if fileCfg.ToolManager.URL != "" {
		cfg.ToolManager.URL = fileCfg.ToolManager.URL
	}
	if fileCfg.ToolManager.InternalServiceKey != "" {
		cfg.ToolManager.InternalServiceKey = fileCfg.ToolManager.InternalServiceKey
	}
	if fileCfg.AIManager.URL != "" {
		cfg.AIManager.URL = fileCfg.AIManager.URL
	}
	if fileCfg.AIManager.InternalServiceKey != "" {
		cfg.AIManager.InternalServiceKey = fileCfg.AIManager.InternalServiceKey
	}
	if fileCfg.AI.TimeoutSeconds > 0 {
		cfg.AI.TimeoutSeconds = fileCfg.AI.TimeoutSeconds
	}
	if fileCfg.Heartbeat.IntervalSeconds > 0 {
		cfg.Heartbeat.IntervalSeconds = fileCfg.Heartbeat.IntervalSeconds
	}
	if fileCfg.Execution.TimeoutSeconds > 0 {
		cfg.Execution.TimeoutSeconds = fileCfg.Execution.TimeoutSeconds
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

func applyEnvOverrides(cfg *Config) {
	if url := os.Getenv("ASSETMANAGER_URL"); url != "" {
		cfg.AssetManager.URL = url
	}
	if key := os.Getenv("ASSETMANAGER_INTERNAL_SERVICE_KEY"); key != "" {
		cfg.AssetManager.InternalServiceKey = key
	}
	if url := os.Getenv("SYSTEMMANAGER_URL"); url != "" {
		cfg.SystemManager.URL = url
	}
	if key := os.Getenv("SYSTEMMANAGER_INTERNAL_SERVICE_KEY"); key != "" {
		cfg.SystemManager.InternalServiceKey = key
	}
	if url := os.Getenv("RESOURCEMANAGER_URL"); url != "" {
		cfg.ResourceManager.URL = url
	}
	if key := os.Getenv("RESOURCEMANAGER_INTERNAL_SERVICE_KEY"); key != "" {
		cfg.ResourceManager.InternalServiceKey = key
	}
	if url := os.Getenv("ACTIONMANAGER_URL"); url != "" {
		cfg.ActionManager.URL = url
	}
	if key := os.Getenv("ACTIONMANAGER_INTERNAL_SERVICE_KEY"); key != "" {
		cfg.ActionManager.InternalServiceKey = key
	}
	if url := os.Getenv("TOOLMANAGER_URL"); url != "" {
		cfg.ToolManager.URL = url
	}
	if key := os.Getenv("TOOLMANAGER_INTERNAL_SERVICE_KEY"); key != "" {
		cfg.ToolManager.InternalServiceKey = key
	}
	if aiMgrURL := os.Getenv("ACTIONEXECUTOR_AI_MANAGER_URL"); aiMgrURL != "" {
		cfg.AIManager.URL = aiMgrURL
	}
	if aiMgrKey := os.Getenv("AIMANAGER_INTERNAL_SERVICE_KEY"); aiMgrKey != "" {
		cfg.AIManager.InternalServiceKey = aiMgrKey
	}
	if timeoutStr := os.Getenv("ACTIONEXECUTOR_AI_TIMEOUT_SECONDS"); timeoutStr != "" {
		if v, err := strconv.Atoi(timeoutStr); err == nil && v > 0 {
			cfg.AI.TimeoutSeconds = v
		}
	}
	if interval := os.Getenv("ACTIONEXECUTOR_HEARTBEAT_INTERVAL_SECONDS"); interval != "" {
		if n, err := strconv.Atoi(interval); err == nil && n > 0 {
			cfg.Heartbeat.IntervalSeconds = n
		}
	}
	if v := os.Getenv("ACTIONEXECUTOR_EXECUTION_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Execution.TimeoutSeconds = n
		}
	}
	if logPath := os.Getenv("ACTIONEXECUTOR_LOG_FILE"); logPath != "" {
		cfg.Log.FilePath = logPath
	}
	if logConsole := os.Getenv("ACTIONEXECUTOR_LOG_TO_CONSOLE"); logConsole != "" {
		cfg.Log.ToConsole = logConsole == "true" || logConsole == "1"
	}
	if logFile := os.Getenv("ACTIONEXECUTOR_LOG_TO_FILE"); logFile != "" {
		cfg.Log.ToFile = logFile == "true" || logFile == "1"
	}
	if logLevel := os.Getenv("ACTIONEXECUTOR_LOG_LEVEL"); logLevel != "" {
		cfg.Log.Level = logLevel
	}
}

// ExecutionTimeout is the time limit of one routine run.
func (c *Config) ExecutionTimeout() time.Duration {
	return time.Duration(c.Execution.TimeoutSeconds) * time.Second
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
