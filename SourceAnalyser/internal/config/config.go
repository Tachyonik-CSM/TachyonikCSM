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
	"strings"

	"gopkg.in/yaml.v3"
	"tachyonik/lib/settings"
)

type Config struct {
	ResourceManager ServiceConfig
	SystemManager   ServiceConfig
	AIManager       ServiceConfig
	AI              AIConfig
	Analyzer        AnalyzerConfig
	Heartbeat       HeartbeatConfig
	Log             LogConfig
	// Warnings are settings that are accepted but deprecated or no longer do
	// anything, for the startup log (SA-OPS-06, SA-OPS-20).
	Warnings []string
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

// ServiceConfig is where a service is reached and the key sent to it
// (SA-OPS-19).
type ServiceConfig struct {
	URL                string
	InternalServiceKey string
}

// FileServiceConfig is a service's section in config.yaml.
type FileServiceConfig struct {
	URL                string `yaml:"url"`
	InternalServiceKey string `yaml:"internal_service_key"`
}

type AnalyzerConfig struct {
	// SweepIntervalSeconds is how often the whole source list is gone
	// through, as the safety net behind ResourceManager's messages (SA-ANA-19).
	SweepIntervalSeconds int
	// JSExecTimeoutSeconds bounds how long a single JS routine execution
	// (load or analyze) may run before it is interrupted. Guards the daemon
	// against infinite loops / catastrophic regex in AI- or feed-sourced
	// routines. 0 disables the guard (from the environment only).
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
	ResourceManager FileServiceConfig `yaml:"resourcemanager"`
	SystemManager   FileServiceConfig `yaml:"systemmanager"`
	AIManager       FileServiceConfig `yaml:"aimanager"`
	// OldAIManager is the section's name before 2026-11-20, read for one
	// release when aimanager is absent (SA-OPS-20).
	OldAIManager *FileServiceConfig `yaml:"ai_manager"`
	Analyzer     struct {
		// PollInterval is accepted for one release and ignored (SA-OPS-18).
		PollInterval         int   `yaml:"poll_interval"`
		SweepIntervalSeconds int   `yaml:"sweep_interval_seconds"`
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
// the defaults and the environment run a local development setup. A
// number, true/false or log-level value it cannot use — from the file or
// the environment — is refused as well, every one of them in one error.
func Load() (*Config, error) {
	// 1. Set built-in defaults
	cfg := &Config{
		ResourceManager: ServiceConfig{URL: "http://localhost:8080"},
		SystemManager:   ServiceConfig{URL: "http://localhost:8083"},
		AIManager:       ServiceConfig{URL: "http://localhost:8085"},
		AI: AIConfig{
			AIName:       "",
			Model:        "",
			SystemPrompt: "",
		},
		Analyzer: AnalyzerConfig{
			SweepIntervalSeconds: 300,
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

	// 3. Apply config.yaml on top of defaults, then 4. the environment on
	// top (overrides everything). The reader collects every value that
	// cannot be used, from both, and stops the service with the whole list.
	r := settings.NewReader(configPath)
	if fileCfg != nil {
		applyFileConfig(cfg, fileCfg, r)
	}
	applyEnvOverrides(cfg, r)
	if err := r.Err(); err != nil {
		return nil, err
	}
	// The warnings name the sweep interval in effect, known only now.
	for i, w := range cfg.Warnings {
		cfg.Warnings[i] = strings.ReplaceAll(w, sweepPlaceholder, strconv.Itoa(cfg.Analyzer.SweepIntervalSeconds))
	}

	return cfg, nil
}

// applyFileConfig applies non-zero values from the parsed config.yaml onto cfg.
// A 0 reads as an absent key (YAML cannot tell them apart); a number out of
// bounds or an unknown log level is recorded in r.
func applyFileConfig(cfg *Config, fileCfg *FileConfig, r *settings.Reader) {
	applyFileService(&cfg.ResourceManager, fileCfg.ResourceManager)
	applyFileService(&cfg.SystemManager, fileCfg.SystemManager)
	// The old ai_manager section, for one release (SA-OPS-20).
	if old := fileCfg.OldAIManager; old != nil {
		if fileCfg.AIManager != (FileServiceConfig{}) {
			cfg.Warnings = append(cfg.Warnings, r.File()+": the section ai_manager is ignored, as aimanager is set; remove ai_manager, the next release refuses it")
		} else {
			cfg.Warnings = append(cfg.Warnings, r.File()+": the section ai_manager is deprecated: rename it to aimanager; the next release refuses it")
			applyFileService(&cfg.AIManager, *old)
		}
	}
	applyFileService(&cfg.AIManager, fileCfg.AIManager)
	// A ticker cannot run on 0 or fewer seconds, and a 0-byte limit downloads
	// nothing to analyse, so every number here must be above 0. The JS budget
	// can be switched off only from the environment (0), as the file cannot
	// say 0.
	r.FileInt("analyzer.sweep_interval_seconds", fileCfg.Analyzer.SweepIntervalSeconds, &cfg.Analyzer.SweepIntervalSeconds, settings.Min(1))
	if fileCfg.Analyzer.PollInterval != 0 {
		cfg.Warnings = append(cfg.Warnings, r.File()+": analyzer.poll_interval is ignored: sources are analysed when ResourceManager announces them, and the whole list is gone through every analyzer.sweep_interval_seconds (now "+sweepPlaceholder+" s); remove poll_interval, it will be refused in the next release")
	}
	r.FileInt("analyzer.js_exec_timeout_seconds", fileCfg.Analyzer.JSExecTimeoutSeconds, &cfg.Analyzer.JSExecTimeoutSeconds, settings.Min(1))
	r.FileInt64("analyzer.max_content_bytes", fileCfg.Analyzer.MaxContentBytes, &cfg.Analyzer.MaxContentBytes, settings.Min(1))
	r.FileInt64("analyzer.max_pdf_bytes", fileCfg.Analyzer.MaxPDFBytes, &cfg.Analyzer.MaxPDFBytes, settings.Min(1))
	r.FileInt("heartbeat.interval_seconds", fileCfg.Heartbeat.IntervalSeconds, &cfg.Heartbeat.IntervalSeconds, settings.Min(1))
	if fileCfg.Log.FilePath != "" {
		cfg.Log.FilePath = fileCfg.Log.FilePath
	}
	r.FileLevel("log.level", fileCfg.Log.Level, &cfg.Log.Level)
	cfg.Log.ToConsole = fileCfg.Log.ToConsole
	cfg.Log.ToFile = fileCfg.Log.ToFile
}

// applyFileService takes a section's non-empty values.
func applyFileService(dst *ServiceConfig, f FileServiceConfig) {
	if f.URL != "" {
		dst.URL = f.URL
	}
	if f.InternalServiceKey != "" {
		dst.InternalServiceKey = f.InternalServiceKey
	}
}

// applyEnvOverrides applies environment variable overrides onto cfg. An empty
// variable counts as unset; a value it cannot use is recorded in r.
func applyEnvOverrides(cfg *Config, r *settings.Reader) {
	// SOURCEANALYSER_<SERVICE>_URL and SOURCEANALYSER_<SERVICE>_INTERNAL_SERVICE_KEY
	// (SA-OPS-19); the names before 2026-11-20 are read for one release when
	// the new one is not set, with a warning (SA-OPS-20).
	for _, svc := range []struct {
		dst     *ServiceConfig
		prefix  string
		oldURLs []string
		oldKey  string
	}{
		{&cfg.ResourceManager, "SOURCEANALYSER_RESOURCEMANAGER_", []string{"RESOURCEMANAGER_URL", "RESOURCEMANAGER_API_BASE_URL"}, "RESOURCEMANAGER_INTERNAL_SERVICE_KEY"},
		{&cfg.SystemManager, "SOURCEANALYSER_SYSTEMMANAGER_", []string{"SYSTEMMANAGER_URL"}, "SYSTEMMANAGER_INTERNAL_SERVICE_KEY"},
		{&cfg.AIManager, "SOURCEANALYSER_AIMANAGER_", []string{"SOURCEANALYSER_AI_MANAGER_URL"}, "AIMANAGER_INTERNAL_SERVICE_KEY"},
	} {
		envService(cfg, &svc.dst.URL, svc.prefix+"URL", svc.oldURLs...)
		envService(cfg, &svc.dst.InternalServiceKey, svc.prefix+"INTERNAL_SERVICE_KEY", svc.oldKey)
	}
	r.Int("SOURCEANALYSER_SWEEP_INTERVAL_SECONDS", &cfg.Analyzer.SweepIntervalSeconds, settings.Min(1))
	if os.Getenv("SOURCEANALYSER_POLL_INTERVAL") != "" {
		cfg.Warnings = append(cfg.Warnings, "SOURCEANALYSER_POLL_INTERVAL is ignored: sources are analysed when ResourceManager announces them, and the whole list is gone through every SOURCEANALYSER_SWEEP_INTERVAL_SECONDS (now "+sweepPlaceholder+" s); remove the variable")
	}
	r.Int("SOURCEANALYSER_JS_EXEC_TIMEOUT_SECONDS", &cfg.Analyzer.JSExecTimeoutSeconds, settings.Min(1), settings.ZeroMeansOff())
	r.Int64("SOURCEANALYSER_MAX_CONTENT_BYTES", &cfg.Analyzer.MaxContentBytes, settings.Min(1))
	r.Int64("SOURCEANALYSER_MAX_PDF_BYTES", &cfg.Analyzer.MaxPDFBytes, settings.Min(1))
	r.Int("SOURCEANALYSER_HEARTBEAT_INTERVAL_SECONDS", &cfg.Heartbeat.IntervalSeconds, settings.Min(1))
	if logPath := os.Getenv("SOURCEANALYSER_LOG_FILE"); logPath != "" {
		cfg.Log.FilePath = logPath
	}
	r.Bool("SOURCEANALYSER_LOG_TO_CONSOLE", &cfg.Log.ToConsole)
	r.Bool("SOURCEANALYSER_LOG_TO_FILE", &cfg.Log.ToFile)
	r.Level("SOURCEANALYSER_LOG_LEVEL", &cfg.Log.Level)

	// SystemManager without a key of its own gets ResourceManager's, for one
	// release, with a warning (SA-OPS-17).
	if cfg.SystemManager.InternalServiceKey == "" && cfg.ResourceManager.InternalServiceKey != "" {
		cfg.SystemManager.InternalServiceKey = cfg.ResourceManager.InternalServiceKey
		cfg.Warnings = append(cfg.Warnings, "systemmanager.internal_service_key (SOURCEANALYSER_SYSTEMMANAGER_INTERNAL_SERVICE_KEY) is not set: ResourceManager's key is sent to SystemManager instead; set SystemManager's own key, the next release requires it")
	}
}

// envService takes a service setting from the variable name, or else from
// the first old name that is set, warning about the old one.
func envService(cfg *Config, dst *string, name string, oldNames ...string) {
	value := os.Getenv(name)
	newSet := value != ""
	for _, old := range oldNames {
		if os.Getenv(old) == "" {
			continue
		}
		switch {
		case newSet:
			cfg.Warnings = append(cfg.Warnings, old+" is ignored, as "+name+" is set; remove it, the next release no longer reads it")
		case value != "":
			cfg.Warnings = append(cfg.Warnings, old+" is ignored, as an older name is set too; set "+name+" instead, the next release no longer reads either")
		default:
			cfg.Warnings = append(cfg.Warnings, old+" is deprecated: set "+name+" instead; the next release no longer reads it")
			value = os.Getenv(old)
		}
	}
	if value != "" {
		*dst = value
	}
}

// sweepPlaceholder stands for the sweep interval in a warning written before
// the interval is final.
const sweepPlaceholder = "{sweep}"

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
