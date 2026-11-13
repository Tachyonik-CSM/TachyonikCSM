// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package config loads SourceAnalyser's configuration, layering sources in
// order of precedence: environment variables override config.yaml, which
// overrides the built-in defaults.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ResourceManager ResourceManagerConfig
	SystemManager   SystemManagerConfig
	AIManager       AIManagerConfig
	AI              AIConfig
	Analyzer        AnalyzerConfig
	Heartbeat       HeartbeatConfig
	Log             LogConfig
}

// HeartbeatConfig controls the liveness signal sent to SystemManager.
// IntervalSeconds is the cadence; SystemManager applies its own grace
// period on top before declaring the service offline.
type HeartbeatConfig struct {
	IntervalSeconds int
}

type AIConfig struct {
	AIName       string // resolved from AIManager at runtime
	Model        string // resolved from AIManager at runtime
	SystemPrompt string // resolved from AIManager at runtime
}

type AIManagerConfig struct {
	URL                string
	InternalServiceKey string
}

type ResourceManagerConfig struct {
	URL                string
	InternalServiceKey string
}

type SystemManagerConfig struct {
	URL                string
	InternalServiceKey string
}

type AnalyzerConfig struct {
	PollInterval int // in seconds
	// JSExecTimeoutSeconds bounds how long a single JS routine execution
	// (load or analyze) may run before it is interrupted. Guards the daemon
	// against infinite loops / catastrophic regex in AI- or feed-sourced
	// routines. A value <= 0 disables the guard.
	JSExecTimeoutSeconds int
	// MaxContentBytes bounds the bytes downloaded and matched for a non-PDF
	// source (and the initial sniff read for any source).
	MaxContentBytes int64
	// MaxPDFBytes bounds the full download of a PDF source before its text is
	// extracted, guarding against memory exhaustion on very large PDFs.
	MaxPDFBytes int64
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
	} `yaml:"resourcemanager"`
	SystemManager struct {
		URL                string `yaml:"url"`
		InternalServiceKey string `yaml:"internal_service_key"`
	} `yaml:"systemmanager"`
	AIManager struct {
		URL                string `yaml:"url"`
		InternalServiceKey string `yaml:"internal_service_key"`
	} `yaml:"ai_manager"`
	Analyzer struct {
		PollInterval         int   `yaml:"poll_interval"`
		JSExecTimeoutSeconds int   `yaml:"js_exec_timeout_seconds"`
		MaxContentBytes      int64 `yaml:"max_content_bytes"`
		MaxPDFBytes          int64 `yaml:"max_pdf_bytes"`
	} `yaml:"analyzer"`
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
// the defaults and the environment run a local development setup.
func Load() (*Config, error) {
	// 1. Set built-in defaults
	cfg := &Config{
		ResourceManager: ResourceManagerConfig{
			URL:                "http://localhost:8080",
			InternalServiceKey: "",
		},
		SystemManager: SystemManagerConfig{
			URL:                "http://localhost:8083",
			InternalServiceKey: "",
		},
		AIManager: AIManagerConfig{
			URL: "http://localhost:8085",
		},
		AI: AIConfig{
			AIName:       "",
			Model:        "",
			SystemPrompt: "",
		},
		Analyzer: AnalyzerConfig{
			PollInterval:         5,
			JSExecTimeoutSeconds: 5,
			MaxContentBytes:      64 * 1024,
			MaxPDFBytes:          32 * 1024 * 1024,
		},
		Heartbeat: HeartbeatConfig{
			IntervalSeconds: 10,
		},
		Log: LogConfig{
			FilePath:  "./sourceanalyser.log",
			ToConsole: true,
			ToFile:    true,
			Level:     "INFO",
		},
	}

	// 2. Parse config.yaml
	var fileCfg *FileConfig
	configPath := getEnv("SOURCEANALYSER_CONFIG", "./config.yaml")
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

	if fileCfg.SystemManager.URL != "" {
		cfg.SystemManager.URL = fileCfg.SystemManager.URL
	}
	if fileCfg.SystemManager.InternalServiceKey != "" {
		cfg.SystemManager.InternalServiceKey = fileCfg.SystemManager.InternalServiceKey
	}
	if fileCfg.AIManager.URL != "" {
		cfg.AIManager.URL = fileCfg.AIManager.URL
	}
	if fileCfg.AIManager.InternalServiceKey != "" {
		cfg.AIManager.InternalServiceKey = fileCfg.AIManager.InternalServiceKey
	}
	if fileCfg.Analyzer.PollInterval != 0 {
		cfg.Analyzer.PollInterval = fileCfg.Analyzer.PollInterval
	}
	if fileCfg.Analyzer.JSExecTimeoutSeconds != 0 {
		cfg.Analyzer.JSExecTimeoutSeconds = fileCfg.Analyzer.JSExecTimeoutSeconds
	}
	if fileCfg.Analyzer.MaxContentBytes != 0 {
		cfg.Analyzer.MaxContentBytes = fileCfg.Analyzer.MaxContentBytes
	}
	if fileCfg.Analyzer.MaxPDFBytes != 0 {
		cfg.Analyzer.MaxPDFBytes = fileCfg.Analyzer.MaxPDFBytes
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
	// ResourceManager — support both new and legacy env vars
	if url := os.Getenv("RESOURCEMANAGER_URL"); url != "" {
		cfg.ResourceManager.URL = url
	} else if url := os.Getenv("RESOURCEMANAGER_API_BASE_URL"); url != "" {
		cfg.ResourceManager.URL = url
	}
	if key := os.Getenv("RESOURCEMANAGER_INTERNAL_SERVICE_KEY"); key != "" {
		cfg.ResourceManager.InternalServiceKey = key
	}

	if url := os.Getenv("SYSTEMMANAGER_URL"); url != "" {
		cfg.SystemManager.URL = url
	}
	if key := os.Getenv("SYSTEMMANAGER_INTERNAL_SERVICE_KEY"); key != "" {
		cfg.SystemManager.InternalServiceKey = key
	}
	if aiMgrURL := os.Getenv("SOURCEANALYSER_AI_MANAGER_URL"); aiMgrURL != "" {
		cfg.AIManager.URL = aiMgrURL
	}
	if aiMgrKey := os.Getenv("AIMANAGER_INTERNAL_SERVICE_KEY"); aiMgrKey != "" {
		cfg.AIManager.InternalServiceKey = aiMgrKey
	}
	if pollInterval := os.Getenv("SOURCEANALYSER_POLL_INTERVAL"); pollInterval != "" {
		if interval, err := strconv.Atoi(pollInterval); err == nil {
			cfg.Analyzer.PollInterval = interval
		}
	}
	if t := os.Getenv("SOURCEANALYSER_JS_EXEC_TIMEOUT_SECONDS"); t != "" {
		if secs, err := strconv.Atoi(t); err == nil {
			cfg.Analyzer.JSExecTimeoutSeconds = secs
		}
	}
	if v := os.Getenv("SOURCEANALYSER_MAX_CONTENT_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.Analyzer.MaxContentBytes = n
		}
	}
	if v := os.Getenv("SOURCEANALYSER_MAX_PDF_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.Analyzer.MaxPDFBytes = n
		}
	}
	if interval := os.Getenv("SOURCEANALYSER_HEARTBEAT_INTERVAL_SECONDS"); interval != "" {
		if n, err := strconv.Atoi(interval); err == nil && n > 0 {
			cfg.Heartbeat.IntervalSeconds = n
		}
	}
	if logPath := os.Getenv("SOURCEANALYSER_LOG_FILE"); logPath != "" {
		cfg.Log.FilePath = logPath
	}
	if logConsole := os.Getenv("SOURCEANALYSER_LOG_TO_CONSOLE"); logConsole != "" {
		cfg.Log.ToConsole = logConsole == "true" || logConsole == "1"
	}
	if logFile := os.Getenv("SOURCEANALYSER_LOG_TO_FILE"); logFile != "" {
		cfg.Log.ToFile = logFile == "true" || logFile == "1"
	}
	if logLevel := os.Getenv("SOURCEANALYSER_LOG_LEVEL"); logLevel != "" {
		cfg.Log.Level = logLevel
	}

	// Fall back to the ResourceManager internal service key if SystemManager key not explicitly set
	if cfg.SystemManager.InternalServiceKey == "" {
		cfg.SystemManager.InternalServiceKey = cfg.ResourceManager.InternalServiceKey
	}
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
