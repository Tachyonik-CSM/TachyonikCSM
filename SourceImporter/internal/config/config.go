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
	"tachyonik/lib/settings"
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
// the defaults and the environment run a local development setup. A
// number, true/false or log-level value it cannot use — from the file or
// the environment — is refused as well, every one of them in one error.
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

	// 3. Apply config.yaml on top of defaults, then 4. the environment on
	// top (overrides everything). The reader collects every value that
	// cannot be used, from both, and stops the daemon with the whole list.
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
// A 0 reads as an absent key (YAML cannot tell them apart); a negative number
// or an unknown log level is recorded in r.
func applyFileConfig(cfg *Config, fileCfg *FileConfig, r *settings.Reader) {
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
	r.FileInt("importer.poll_interval", fileCfg.Importer.PollInterval, &cfg.Importer.PollInterval, settings.Min(1))
	r.FileInt("heartbeat.interval_seconds", fileCfg.Heartbeat.IntervalSeconds, &cfg.Heartbeat.IntervalSeconds, settings.Min(1))
	if fileCfg.AIManager.URL != "" {
		cfg.AIManager.URL = fileCfg.AIManager.URL
	}
	if fileCfg.AIManager.InternalServiceKey != "" {
		cfg.AIManager.InternalServiceKey = fileCfg.AIManager.InternalServiceKey
	}
	if fileCfg.Log.FilePath != "" {
		cfg.Log.FilePath = fileCfg.Log.FilePath
	}
	r.FileLevel("log.level", fileCfg.Log.Level, &cfg.Log.Level)
	cfg.Log.ToConsole = fileCfg.Log.ToConsole
	cfg.Log.ToFile = fileCfg.Log.ToFile
}

// applyEnvOverrides applies environment variable overrides onto cfg. An empty
// variable counts as unset; a value it cannot use is recorded in r.
func applyEnvOverrides(cfg *Config, r *settings.Reader) {
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
	r.Int("SOURCEIMPORTER_POLL_INTERVAL", &cfg.Importer.PollInterval, settings.Min(1))
	r.Int("SOURCEIMPORTER_HEARTBEAT_INTERVAL_SECONDS", &cfg.Heartbeat.IntervalSeconds, settings.Min(1))
	if aiMgrURL := os.Getenv("SOURCEIMPORTER_AI_MANAGER_URL"); aiMgrURL != "" {
		cfg.AIManager.URL = aiMgrURL
	}
	if aiMgrKey := os.Getenv("AIMANAGER_INTERNAL_SERVICE_KEY"); aiMgrKey != "" {
		cfg.AIManager.InternalServiceKey = aiMgrKey
	}
	if logPath := os.Getenv("SOURCEIMPORTER_LOG_FILE"); logPath != "" {
		cfg.Log.FilePath = logPath
	}
	r.Bool("SOURCEIMPORTER_LOG_TO_CONSOLE", &cfg.Log.ToConsole)
	r.Bool("SOURCEIMPORTER_LOG_TO_FILE", &cfg.Log.ToFile)
	r.Level("SOURCEIMPORTER_LOG_LEVEL", &cfg.Log.Level)
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
