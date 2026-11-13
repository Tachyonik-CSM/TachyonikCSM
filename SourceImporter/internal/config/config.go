// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package config loads the daemon's configuration from, in increasing order of
// precedence, built-in defaults, config.yaml and environment variables.
//
// It covers the poll interval, the four service endpoints SourceImporter talks
// to (ResourceManager, AssetManager, AIManager, SystemManager) with their
// internal service keys, the SystemManager heartbeat, and logging. The AI
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
)

type Config struct {
	ResourceManager ResourceManagerConfig
	AssetManager    AssetManagerConfig
	SystemManager   SystemManagerConfig
	AIManager       AIManagerConfig
	Importer        ImporterConfig
	AI              AIConfig
	Heartbeat       HeartbeatConfig
	Log             LogConfig
}

// HeartbeatConfig controls the liveness signal sent to SystemManager.
type HeartbeatConfig struct {
	IntervalSeconds int
}

type ImporterConfig struct {
	PollInterval int
}

type ResourceManagerConfig struct {
	URL                string
	InternalServiceKey string
}

type AssetManagerConfig struct {
	URL                string
	InternalServiceKey string
}

type SystemManagerConfig struct {
	URL                string
	InternalServiceKey string
}

type AIManagerConfig struct {
	URL                string
	InternalServiceKey string
}

type AIConfig struct {
	AIName       string // resolved from AIManager at runtime
	Model        string // resolved from AIManager at runtime
	SystemPrompt string // resolved from AIManager at runtime
}

type LogConfig struct {
	FilePath  string
	ToConsole bool
	ToFile    bool
	Level     string
}

// FileConfig represents the structure of config.yaml
type FileConfig struct {
	ResourceManager struct {
		URL                string `yaml:"url"`
		InternalServiceKey string `yaml:"internal_service_key"`
	} `yaml:"rscmanager"`
	AssetManager struct {
		URL                string `yaml:"url"`
		InternalServiceKey string `yaml:"internal_service_key"`
	} `yaml:"assetmanager"`
	SystemManager struct {
		URL                string `yaml:"url"`
		InternalServiceKey string `yaml:"internal_service_key"`
	} `yaml:"systemmanager"`
	Importer struct {
		PollInterval int `yaml:"poll_interval"`
	} `yaml:"importer"`
	Heartbeat struct {
		IntervalSeconds int `yaml:"interval_seconds"`
	} `yaml:"heartbeat"`
	AIManager struct {
		URL                string `yaml:"url"`
		InternalServiceKey string `yaml:"internal_service_key"`
	} `yaml:"ai_manager"`
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
// the defaults and the environment run a local development setup.
func Load() (*Config, error) {
	// 1. Set built-in defaults
	cfg := &Config{
		ResourceManager: ResourceManagerConfig{
			URL:                "http://localhost:8080",
			InternalServiceKey: "",
		},
		AssetManager: AssetManagerConfig{
			URL:                "http://localhost:8081",
			InternalServiceKey: "",
		},
		SystemManager: SystemManagerConfig{
			URL:                "http://localhost:8083",
			InternalServiceKey: "",
		},
		Importer: ImporterConfig{
			PollInterval: 5,
		},
		Heartbeat: HeartbeatConfig{
			IntervalSeconds: 10,
		},
		AIManager: AIManagerConfig{
			URL: "http://localhost:8085",
		},
		AI: AIConfig{
			AIName:       "",
			Model:        "",
			SystemPrompt: "",
		},
		Log: LogConfig{
			FilePath:  "./sourceimporter.log",
			ToConsole: true,
			ToFile:    true,
			Level:     "INFO",
		},
	}

	// 2. Parse config.yaml
	var fileCfg *FileConfig
	configPath := getEnv("SOURCEIMPORTER_CONFIG", "./config.yaml")
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

	// 3. Apply config.yaml on top of defaults
	if fileCfg != nil {
		applyFileConfig(cfg, fileCfg)
	}

	// 4. Apply env var overrides on top (overrides everything)
	applyEnvOverrides(cfg)

	return cfg, nil
}

// applyFileConfig applies non-zero values from the parsed config.yaml onto cfg.
func applyFileConfig(cfg *Config, fileCfg *FileConfig) {
	if fileCfg.ResourceManager.URL != "" {
		cfg.ResourceManager.URL = fileCfg.ResourceManager.URL
	}
	if fileCfg.ResourceManager.InternalServiceKey != "" {
		cfg.ResourceManager.InternalServiceKey = fileCfg.ResourceManager.InternalServiceKey
	}
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
	if fileCfg.Importer.PollInterval > 0 {
		cfg.Importer.PollInterval = fileCfg.Importer.PollInterval
	}
	if fileCfg.Heartbeat.IntervalSeconds > 0 {
		cfg.Heartbeat.IntervalSeconds = fileCfg.Heartbeat.IntervalSeconds
	}
	if fileCfg.AIManager.URL != "" {
		cfg.AIManager.URL = fileCfg.AIManager.URL
	}
	if fileCfg.AIManager.InternalServiceKey != "" {
		cfg.AIManager.InternalServiceKey = fileCfg.AIManager.InternalServiceKey
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
	if url := os.Getenv("RESOURCEMANAGER_URL"); url != "" {
		cfg.ResourceManager.URL = url
	}
	if internalKey := os.Getenv("RESOURCEMANAGER_INTERNAL_SERVICE_KEY"); internalKey != "" {
		cfg.ResourceManager.InternalServiceKey = internalKey
	}
	if url := os.Getenv("ASSETMANAGER_URL"); url != "" {
		cfg.AssetManager.URL = url
	}
	if internalKey := os.Getenv("ASSETMANAGER_INTERNAL_SERVICE_KEY"); internalKey != "" {
		cfg.AssetManager.InternalServiceKey = internalKey
	}
	if url := os.Getenv("SYSTEMMANAGER_URL"); url != "" {
		cfg.SystemManager.URL = url
	}
	if key := os.Getenv("SYSTEMMANAGER_INTERNAL_SERVICE_KEY"); key != "" {
		cfg.SystemManager.InternalServiceKey = key
	}
	if pollInterval := os.Getenv("SOURCEIMPORTER_POLL_INTERVAL"); pollInterval != "" {
		if interval, err := parseInt(pollInterval); err == nil && interval > 0 {
			cfg.Importer.PollInterval = interval
		}
	}
	if interval := os.Getenv("SOURCEIMPORTER_HEARTBEAT_INTERVAL_SECONDS"); interval != "" {
		if n, err := parseInt(interval); err == nil && n > 0 {
			cfg.Heartbeat.IntervalSeconds = n
		}
	}
	if aiMgrURL := os.Getenv("SOURCEIMPORTER_AI_MANAGER_URL"); aiMgrURL != "" {
		cfg.AIManager.URL = aiMgrURL
	}
	if aiMgrKey := os.Getenv("AIMANAGER_INTERNAL_SERVICE_KEY"); aiMgrKey != "" {
		cfg.AIManager.InternalServiceKey = aiMgrKey
	}
	if logPath := os.Getenv("SOURCEIMPORTER_LOG_FILE"); logPath != "" {
		cfg.Log.FilePath = logPath
	}
	if logConsole := os.Getenv("SOURCEIMPORTER_LOG_TO_CONSOLE"); logConsole != "" {
		cfg.Log.ToConsole = logConsole == "true" || logConsole == "1"
	}
	if logFile := os.Getenv("SOURCEIMPORTER_LOG_TO_FILE"); logFile != "" {
		cfg.Log.ToFile = logFile == "true" || logFile == "1"
	}
	if logLevel := os.Getenv("SOURCEIMPORTER_LOG_LEVEL"); logLevel != "" {
		cfg.Log.Level = logLevel
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func parseInt(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
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
