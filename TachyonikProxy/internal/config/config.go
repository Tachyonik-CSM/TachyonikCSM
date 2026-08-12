// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// systemConfigPath is the canonical system-wide config location, matching
// the .deb/.rpm packaging and the systemd/launchd unit environment.
const systemConfigPath = "/etc/tachyonik/tachyonikproxy/config.yaml"

type Config struct {
	Server            ServerConfig         `yaml:"server"`
	TLS               TLSConfig            `yaml:"tls"`
	Proxy             ProxyConfig          `yaml:"proxy"`
	Log               LogConfig            `yaml:"log"`
	Tools             []ToolConfig         `yaml:"tools"`
	MCPServers        []MCPServerConfig    `yaml:"mcp_servers"`
	AllowRemoteConfig bool                 `yaml:"allow_remote_config"`
	ConnectionMode    string               `yaml:"connection_mode"` // "outbound" (default) or "inbound"
	ReverseConnect    ReverseConnectConfig `yaml:"reverse_connect"`
	AutoUpdate        AutoUpdateConfig     `yaml:"auto_update"`
	NetScan           NetScanConfig        `yaml:"netscan"`

	// baseDir is the absolute directory of the config file this Config was
	// loaded from (or will be written to). Relative paths inside the config
	// — TLS certs, reverse-connect cert/key, log file_path — resolve
	// against it via AbsPath, so the proxy works from any working
	// directory and a config+certs bundle can be moved as a unit.
	// Unexported: yaml.Marshal skips it, so SaveConfig never persists it.
	baseDir string
}

// AbsPath resolves a path from the config file against the config file's
// own directory. Absolute paths and empty strings pass through untouched;
// relative paths are joined onto the config dir. When no config location
// is known (baseDir empty) the path is returned as-is, preserving the old
// CWD-relative behaviour.
func (c *Config) AbsPath(p string) string {
	if p == "" || filepath.IsAbs(p) || c.baseDir == "" {
		return p
	}
	return filepath.Join(c.baseDir, p)
}

// BaseDir returns the directory relative config paths resolve against
// (empty if no config location is known).
func (c *Config) BaseDir() string {
	return c.baseDir
}

// NetScanConfig controls the periodic HTTPS sweep of the proxy's own local
// network, whose results tool-detection routines match against instead of
// probing hosts themselves.
//
// Concurrency and TimeoutSeconds are the two knobs that matter: a /24 is 254
// probes, so at the defaults a sweep where nothing answers costs roughly
// 254/32 × 3s ≈ 24 seconds. Raising Concurrency shortens that but risks
// exhausting file descriptors on a small appliance and tripping rate limits on
// a monitored network; lowering it is the conservative choice. The remaining
// fields are safety rails rather than tuning.
//
// Network is normally left empty, deriving the /24 around the proxy's primary
// IPv4. Either way the range must be private (or loopback) — see
// internal/netscan.
type NetScanConfig struct {
	Enabled                bool   `yaml:"enabled"`
	IntervalMinutes        int    `yaml:"interval_minutes"`
	Network                string `yaml:"network"`
	Ports                  []int  `yaml:"ports"`
	Concurrency            int    `yaml:"concurrency"`
	TimeoutSeconds         int    `yaml:"timeout_seconds"`
	MaxBodyBytes           int    `yaml:"max_body_bytes"`
	MaxScanDurationMinutes int    `yaml:"max_scan_duration_minutes"`
}

// AutoUpdateConfig controls the proxy's self-update behaviour.
type AutoUpdateConfig struct {
	Enabled             bool   `yaml:"enabled"`
	Channel             string `yaml:"channel"`
	CheckIntervalHours  int    `yaml:"check_interval_hours"`
	HealthWindowSeconds int    `yaml:"health_window_seconds"`
	ManifestURL         string `yaml:"manifest_url"`
	// KeepVersions is the cap on how many version directories survive
	// pruning. The active version, the previous version, and every entry
	// in state.RolledBack[] are always kept on disk regardless of this
	// cap (they are operationally required or part of the forensics
	// record). 0 means "keep only the safety overrides".
	KeepVersions int `yaml:"keep_versions"`
}

type ReverseConnectConfig struct {
	ToolManagerURL string `yaml:"toolmanager_url"`
	ClientCert     string `yaml:"client_cert"`
	ClientKey      string `yaml:"client_key"`
}

type ServerConfig struct {
	Port string `yaml:"port"`
	Host string `yaml:"host"`
}

type TLSConfig struct {
	CACert         string   `yaml:"ca_cert"`
	ServerCert     string   `yaml:"server_cert"`
	ServerKey      string   `yaml:"server_key"`
	AllowedClients []string `yaml:"allowed_clients"`
}

type ProxyConfig struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type LogConfig struct {
	FilePath  string `yaml:"file_path"`
	ToConsole bool   `yaml:"to_console"`
	ToFile    bool   `yaml:"to_file"`
	Level     string `yaml:"level"`
}

// JSONSchemaString stores a JSON schema as a plain string for clean YAML
// serialization, while marshaling/unmarshaling as a raw JSON object in JSON context.
type JSONSchemaString string

// MarshalJSON emits the stored JSON string as raw JSON (not a quoted string).
func (s JSONSchemaString) MarshalJSON() ([]byte, error) {
	if s == "" {
		return []byte("null"), nil
	}
	return []byte(s), nil
}

// UnmarshalJSON stores the raw JSON bytes as a string.
func (s *JSONSchemaString) UnmarshalJSON(data []byte) error {
	*s = JSONSchemaString(data)
	return nil
}

// RawMessage returns the schema as a json.RawMessage for MCP protocol use.
func (s JSONSchemaString) RawMessage() json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

type OutputFileConfig struct {
	ArgFlag   string `yaml:"arg_flag" json:"argFlag"`    // e.g. "-oX"
	Extension string `yaml:"extension" json:"extension"` // e.g. ".xml"
	MimeType  string `yaml:"mime_type" json:"mimeType"`  // e.g. "application/xml"
}

type ToolConfig struct {
	Name             string           `yaml:"name" json:"name"`
	Description      string           `yaml:"description" json:"description"`
	Command          string           `yaml:"command" json:"command"`
	ArgsSchema       JSONSchemaString `yaml:"args_schema" json:"argsSchema,omitempty"`
	ArgTemplate      string           `yaml:"arg_template" json:"argTemplate"`
	Timeout          int              `yaml:"timeout" json:"timeout"`
	AllowedExitCodes []int            `yaml:"allowed_exit_codes" json:"allowedExitCodes,omitempty"`
	AllowedChars     string           `yaml:"allowed_chars" json:"allowedChars,omitempty"`
	MaxOutputBytes   int              `yaml:"max_output_bytes" json:"maxOutputBytes,omitempty"`
	MaxMemoryMB      int              `yaml:"max_memory_mb" json:"maxMemoryMb,omitempty"`
	MaxCPUSeconds    int              `yaml:"max_cpu_seconds" json:"maxCpuSeconds,omitempty"`
	// MaxProcesses caps RLIMIT_NPROC for the spawned tool (fork-bomb guard).
	// 0 = unlimited. NOTE: RLIMIT_NPROC is enforced per real UID by the
	// kernel, so the cap counts ALL processes of the user the proxy runs as,
	// not just this tool's children — set it with headroom for the daemon
	// itself. Linux-only; ignored on other platforms.
	MaxProcesses int `yaml:"max_processes" json:"maxProcesses,omitempty"`
	// MaxFileSizeMB caps RLIMIT_FSIZE for the spawned tool (disk-fill guard).
	// 0 = unlimited. Applies to any single file the tool writes, including
	// configured output_files, so size it above the largest expected report.
	// Linux-only; ignored on other platforms.
	MaxFileSizeMB int                `yaml:"max_file_size_mb" json:"maxFileSizeMb,omitempty"`
	OutputFiles   []OutputFileConfig `yaml:"output_files" json:"outputFiles,omitempty"`
}

type MCPServerConfig struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	URL         string `yaml:"url" json:"url"`
	APIKey      string `yaml:"api_key" json:"apiKey,omitempty"`
}

func Load() *Config {
	return LoadFrom(GetConfigPath())
}

// LoadFrom loads the configuration from an explicit path (e.g. an enroll
// --config override), applying defaults and env overrides the same way
// Load does. The file not existing is not an error — the returned Config
// carries the defaults and remembers the path's directory as its baseDir,
// so a subsequent SaveConfig lands in the right place.
func LoadFrom(configPath string) *Config {
	cfg := &Config{
		Server: ServerConfig{
			Port: "9100",
			Host: "0.0.0.0",
		},
		Proxy: ProxyConfig{
			Name:        "tachyonikproxy",
			Description: "Tachyonik TachyonikProxy",
		},
		Log: LogConfig{
			FilePath:  "./tachyonikproxy.log",
			ToConsole: true,
			ToFile:    true,
			Level:     "INFO",
		},
		AllowRemoteConfig: false,
		AutoUpdate: AutoUpdateConfig{
			Enabled:             true,
			Channel:             "stable",
			CheckIntervalHours:  24,
			HealthWindowSeconds: 30,
			ManifestURL:         "https://tachyonik.com/download/proxy/manifest.json",
			KeepVersions:        3,
		},
		NetScan: NetScanConfig{
			Enabled:                true,
			IntervalMinutes:        60,
			Network:                "", // derive the /24 from the primary IPv4
			Ports:                  []int{443},
			Concurrency:            32,
			TimeoutSeconds:         3,
			MaxBodyBytes:           64 << 10,
			MaxScanDurationMinutes: 10,
		},
	}

	if abs, err := filepath.Abs(filepath.Dir(configPath)); err == nil {
		cfg.baseDir = abs
	}

	if fileData, err := os.ReadFile(configPath); err == nil {
		if err := yaml.Unmarshal(fileData, cfg); err != nil {
			log.Printf("Warning: Failed to parse config file: %v", err)
		} else {
			log.Printf("Loaded configuration from: %s", configPath)
		}
	} else {
		log.Printf("No configuration file at %s — using defaults", configPath)
		// No config yet (fresh install / pre-enrollment): aim the log at
		// the canonical location for this privilege level instead of a
		// file relative to the config directory or CWD. Enrollment then
		// persists this value into the fresh config.
		if os.Geteuid() == 0 {
			cfg.Log.FilePath = "/var/log/tachyonik/tachyonikproxy.log"
		} else if p := UserLogPath(); p != "" {
			cfg.Log.FilePath = p
		}
	}

	if port := os.Getenv("TACHYONIKPROXY_PORT"); port != "" {
		cfg.Server.Port = port
	}
	if host := os.Getenv("TACHYONIKPROXY_HOST"); host != "" {
		cfg.Server.Host = host
	}
	if level := os.Getenv("TACHYONIKPROXY_LOG_LEVEL"); level != "" {
		cfg.Log.Level = level
	}

	return cfg
}

// SaveConfig writes the current configuration to the config file via an
// atomic temp+fsync+rename sequence. File mode is preserved when the
// target already exists (so a postinstall-hardened 0640 stays 0640
// across config/update pushes); fresh files are created 0640 because the
// config references TLS-key paths and other identity-relevant fields.
func SaveConfig(cfg *Config, path string) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	mode := os.FileMode(0640)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}

	dir := filepath.Dir(path)
	// A fresh enrollment may target a canonical directory that doesn't
	// exist yet (~/.config/tachyonik/tachyonikproxy). 0750: the config
	// references TLS-key paths; group-readable at most.
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("create config directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".config.*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write temp config file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("sync temp config file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp config file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp config file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename temp config file: %w", err)
	}
	return nil
}

var (
	resolveOnce  sync.Once
	resolvedPath string
)

// GetConfigPath returns the config file path the proxy uses, resolved once
// per process so every consumer (Load, SaveConfig callers, enrollment)
// agrees on the same file. Search order:
//
//  1. TACHYONIKPROXY_CONFIG env var — explicit always wins
//  2. ./config.yaml, if it exists — backward compatibility with
//     CWD-based setups
//  3. ${XDG_CONFIG_HOME:-~/.config}/tachyonik/tachyonikproxy/config.yaml,
//     if it exists — what install-tachyonikproxy.sh creates for
//     user-space installs
//  4. /etc/tachyonik/tachyonikproxy/config.yaml, if it exists — the
//     .deb/.rpm system layout
//
// When no config exists anywhere the returned path is the canonical
// WRITE target for a fresh enrollment: the system path for root, the
// user XDG path otherwise (./config.yaml as a last resort when no home
// directory can be determined).
func GetConfigPath() string {
	resolveOnce.Do(func() {
		resolvedPath = resolveConfigPath()
	})
	return resolvedPath
}

func resolveConfigPath() string {
	if env := os.Getenv("TACHYONIKPROXY_CONFIG"); env != "" {
		return env
	}
	if fileExists("./config.yaml") {
		return "./config.yaml"
	}
	userPath := UserConfigPath()
	if userPath != "" && fileExists(userPath) {
		return userPath
	}
	if fileExists(systemConfigPath) {
		return systemConfigPath
	}
	// Nothing exists yet — pick the canonical write target.
	if os.Geteuid() == 0 {
		return systemConfigPath
	}
	if userPath != "" {
		return userPath
	}
	return "./config.yaml"
}

// UserConfigPath returns the canonical user-space config location
// (${XDG_CONFIG_HOME:-~/.config}/tachyonik/tachyonikproxy/config.yaml),
// or "" if no home directory can be determined. This matches the path
// install-tachyonikproxy.sh seeds for user-space installs.
func UserConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "tachyonik", "tachyonikproxy", "config.yaml")
}

// UserLogPath returns the canonical user-space log location
// (~/.local/share/tachyonik/logs/tachyonikproxy.log — the directory
// install-tachyonikproxy.sh creates and its uninstaller removes), or ""
// if no home directory can be determined.
func UserLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share", "tachyonik", "logs", "tachyonikproxy.log")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
