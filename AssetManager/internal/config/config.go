// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package config loads the service's configuration from, in increasing order of
// precedence, built-in defaults, config.yaml and environment variables.
//
// It covers the HTTP listener, the SQLite database path, authentication and the
// internal service key, CORS, logging, the instance identity, and the endpoints
// of the other services AssetManager talks to.
package config

import (
	"fmt"
	"log"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server          ServerConfig
	Database        DatabaseConfig
	Auth            AuthConfig
	CORS            CORSConfig
	ResourceManager ServiceConfig
	SystemManager   ServiceConfig
	// AIManager points at the local AIManager, which serves this appliance's
	// per-appliance license (licensed host-asset cap). Set only on an
	// appliance (mode: appliance); leave URL empty on the SaaS to disable the
	// instance-wide cap. AIManager owns the pairing token and fetches the
	// license from the SaaS.
	AIManager ServiceConfig
	Log       LogConfig
	Instance  InstanceConfig
}

// Operating mode. Appliance = one shared workspace (all users see/edit one
// dataset); saas = per-user isolation. Default appliance. Mirrors SystemManager's
// authoritative instance.mode.
const (
	ModeAppliance = "appliance"
	ModeSaaS      = "saas"
)

type InstanceConfig struct {
	Mode string
}

type ServerConfig struct {
	Port string
	Host string
}

type DatabaseConfig struct {
	Path string
}

// PlaceholderJWTSecret is the built-in jwt_secret, also the one
// config.yaml.example shows. It is public, so a deployment still using it
// accepts tokens anyone can sign.
const PlaceholderJWTSecret = "CHANGE-THIS-SECRET-KEY-MIN-32-CHARS"

// MinJWTSecretLength is the shortest jwt_secret not reported as weak.
const MinJWTSecretLength = 32

type AuthConfig struct {
	JWTSecret          string
	InternalServiceKey string
}

type CORSConfig struct {
	AllowedOrigins []string
}

// ServiceConfig is the endpoint of another service AssetManager calls, and
// the internal service key it presents there.
type ServiceConfig struct {
	URL                string
	InternalServiceKey string
}

type LogConfig struct {
	FilePath  string
	ToConsole bool
	ToFile    bool
	Level     string
}

// FileServiceConfig is a service endpoint as config.yaml spells it.
type FileServiceConfig struct {
	URL                string `yaml:"url"`
	InternalServiceKey string `yaml:"internal_service_key"`
}

// FileConfig represents the structure of config.yaml
type FileConfig struct {
	Server struct {
		Port int    `yaml:"port"`
		Host string `yaml:"host"`
	} `yaml:"server"`
	Database struct {
		Path string `yaml:"path"`
	} `yaml:"database"`
	Auth struct {
		JWTSecret          string `yaml:"jwt_secret"`
		InternalServiceKey string `yaml:"internal_service_key"`
	} `yaml:"auth"`
	CORS struct {
		AllowedOrigins []string `yaml:"allowed_origins"`
	} `yaml:"cors"`
	ResourceManager FileServiceConfig `yaml:"rscmanager"`
	SystemManager   FileServiceConfig `yaml:"systemmanager"`
	AIManager       FileServiceConfig `yaml:"aimanager"`
	Log             struct {
		FilePath  string `yaml:"file_path"`
		ToConsole bool   `yaml:"to_console"`
		ToFile    bool   `yaml:"to_file"`
		Level     string `yaml:"level"`
	} `yaml:"log"`
	Instance struct {
		Mode string `yaml:"mode"`
	} `yaml:"instance"`
}

// Load loads configuration from config file and environment variables
// Priority: Environment Variables > Config File > Defaults
func Load() *Config {
	// Set defaults
	cfg := &Config{
		Server: ServerConfig{
			Port: "8081",
			Host: "localhost",
		},
		Database: DatabaseConfig{
			Path: "/home/tachyon/data/AssetManager.db",
		},
		Auth: AuthConfig{
			JWTSecret:          PlaceholderJWTSecret,
			InternalServiceKey: "",
		},
		CORS: CORSConfig{
			AllowedOrigins: []string{
				"http://localhost:5173", // Vite default
				"http://localhost:3000", // React default
			},
		},
		ResourceManager: ServiceConfig{URL: "http://localhost:8080"},
		SystemManager:   ServiceConfig{URL: "http://localhost:8083"},
		// Empty by default: the instance-wide license cap is off unless an
		// appliance points this at its local AIManager.
		AIManager: ServiceConfig{},
		Log: LogConfig{
			FilePath:  "./assetmanager.log",
			ToConsole: true,
			ToFile:    true,
			Level:     "INFO",
		},
		Instance: InstanceConfig{
			Mode: ModeAppliance,
		},
	}

	// Try to load from config file
	configPath := getEnv("ASSETMANAGER_CONFIG", "./config.yaml")
	if fileData, err := os.ReadFile(configPath); err == nil {
		var fileCfg FileConfig
		if err := yaml.Unmarshal(fileData, &fileCfg); err == nil {
			// Apply config file values
			if fileCfg.Server.Port != 0 {
				cfg.Server.Port = strconv.Itoa(fileCfg.Server.Port)
			}
			if fileCfg.Server.Host != "" {
				cfg.Server.Host = fileCfg.Server.Host
			}
			if fileCfg.Database.Path != "" {
				cfg.Database.Path = fileCfg.Database.Path
			}
			if fileCfg.Auth.JWTSecret != "" {
				cfg.Auth.JWTSecret = fileCfg.Auth.JWTSecret
			}
			if fileCfg.Auth.InternalServiceKey != "" {
				cfg.Auth.InternalServiceKey = fileCfg.Auth.InternalServiceKey
			}
			if len(fileCfg.CORS.AllowedOrigins) > 0 {
				cfg.CORS.AllowedOrigins = fileCfg.CORS.AllowedOrigins
			}
			for _, svc := range []struct {
				dst *ServiceConfig
				src FileServiceConfig
			}{
				{&cfg.ResourceManager, fileCfg.ResourceManager},
				{&cfg.SystemManager, fileCfg.SystemManager},
				{&cfg.AIManager, fileCfg.AIManager},
			} {
				if svc.src.URL != "" {
					svc.dst.URL = svc.src.URL
				}
				if svc.src.InternalServiceKey != "" {
					svc.dst.InternalServiceKey = svc.src.InternalServiceKey
				}
			}
			if fileCfg.Log.FilePath != "" {
				cfg.Log.FilePath = fileCfg.Log.FilePath
			}
			if fileCfg.Log.Level != "" {
				cfg.Log.Level = fileCfg.Log.Level
			}
			// Note: YAML booleans default to false, so we check if explicitly set
			cfg.Log.ToConsole = fileCfg.Log.ToConsole
			cfg.Log.ToFile = fileCfg.Log.ToFile
			if fileCfg.Instance.Mode != "" {
				cfg.Instance.Mode = fileCfg.Instance.Mode
			}
			log.Printf("Loaded configuration from: %s", configPath)
		} else {
			log.Printf("Warning: Failed to parse config file: %v", err)
		}
	}

	// Environment variables override config file and defaults
	if mode := os.Getenv("ASSETMANAGER_INSTANCE_MODE"); mode != "" {
		cfg.Instance.Mode = mode
	}
	if port := os.Getenv("ASSETMANAGER_PORT"); port != "" {
		cfg.Server.Port = port
	}
	if host := os.Getenv("ASSETMANAGER_HOST"); host != "" {
		cfg.Server.Host = host
	}
	if dbPath := os.Getenv("ASSETMANAGER_DB_PATH"); dbPath != "" {
		cfg.Database.Path = dbPath
	}
	if jwtSecret := os.Getenv("ASSETMANAGER_JWT_SECRET"); jwtSecret != "" {
		cfg.Auth.JWTSecret = jwtSecret
	}
	if internalKey := os.Getenv("ASSETMANAGER_INTERNAL_SERVICE_KEY"); internalKey != "" {
		cfg.Auth.InternalServiceKey = internalKey
	}
	if logPath := os.Getenv("ASSETMANAGER_LOG_FILE"); logPath != "" {
		cfg.Log.FilePath = logPath
	}
	if logConsole := os.Getenv("ASSETMANAGER_LOG_TO_CONSOLE"); logConsole != "" {
		cfg.Log.ToConsole = logConsole == "true" || logConsole == "1"
	}
	if logFile := os.Getenv("ASSETMANAGER_LOG_TO_FILE"); logFile != "" {
		cfg.Log.ToFile = logFile == "true" || logFile == "1"
	}
	if logLevel := os.Getenv("ASSETMANAGER_LOG_LEVEL"); logLevel != "" {
		cfg.Log.Level = logLevel
	}
	// ASSETMANAGER_<SERVICE>_URL and ASSETMANAGER_<SERVICE>_INTERNAL_SERVICE_KEY.
	for _, svc := range []struct {
		dst    *ServiceConfig
		prefix string
	}{
		{&cfg.ResourceManager, "ASSETMANAGER_RESOURCEMANAGER_"},
		{&cfg.SystemManager, "ASSETMANAGER_SYSTEMMANAGER_"},
		{&cfg.AIManager, "ASSETMANAGER_AIMANAGER_"},
	} {
		if url := os.Getenv(svc.prefix + "URL"); url != "" {
			svc.dst.URL = url
		}
		if key := os.Getenv(svc.prefix + "INTERNAL_SERVICE_KEY"); key != "" {
			svc.dst.InternalServiceKey = key
		}
	}

	return cfg
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// JWTSecretProblem says what is wrong with the configured JWT secret, or ""
// when nothing is: it is still the public placeholder, or shorter than
// MinJWTSecretLength. The service starts regardless — a development setup
// runs on the placeholder — and the caller warns.
func (c *Config) JWTSecretProblem() string {
	switch {
	case c.Auth.JWTSecret == PlaceholderJWTSecret:
		return "auth.jwt_secret is still the public placeholder, so anyone can sign tokens this service accepts"
	case len(c.Auth.JWTSecret) < MinJWTSecretLength:
		return fmt.Sprintf("auth.jwt_secret is shorter than %d characters", MinJWTSecretLength)
	}
	return ""
}
