// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that a true/false or log-level value AssetManager cannot use stops it
// at startup, from the environment as from the file, naming the setting
// (AM-OPS-19), and that every such fault is reported at once.

package config

import (
	"strings"
	"testing"
)

func TestAnUnusableEnvironmentValueStopsTheService(t *testing.T) {
	for _, c := range []struct{ env, value string }{
		{"ASSETMANAGER_LOG_TO_CONSOLE", "yes"},
		{"ASSETMANAGER_LOG_TO_FILE", "off"},
		{"ASSETMANAGER_LOG_LEVEL", "verbose"},
		{"ASSETMANAGER_LOG_LEVEL", "WARNING"},
	} {
		t.Run(c.env+"="+c.value, func(t *testing.T) {
			withConfig(t, "")
			t.Setenv(c.env, c.value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), c.env) {
				t.Errorf("error = %v, want one naming %s", err, c.env)
			}
		})
	}
}

func TestTheAcceptedSpellingsLoad(t *testing.T) {
	withConfig(t, "log:\n  to_console: true\n  to_file: false\n")
	t.Setenv("ASSETMANAGER_LOG_TO_CONSOLE", "FALSE")
	t.Setenv("ASSETMANAGER_LOG_TO_FILE", "1")
	t.Setenv("ASSETMANAGER_LOG_LEVEL", "debug")
	cfg := mustLoad(t)
	if cfg.Log.ToConsole || !cfg.Log.ToFile || cfg.Log.Level != "DEBUG" {
		t.Errorf("log = %+v", cfg.Log)
	}

	withConfig(t, "log:\n  level: warn\n")
	t.Setenv("ASSETMANAGER_LOG_LEVEL", "") // empty counts as unset
	if cfg := mustLoad(t); cfg.Log.Level != "WARN" {
		t.Errorf("file log.level warn read as %q, want WARN", cfg.Log.Level)
	}
}

func TestAnUnusableFileValueStopsTheService(t *testing.T) {
	for _, c := range []struct{ content, key string }{
		{"log:\n  level: loud\n", "log.level"},
	} {
		t.Run(c.key, func(t *testing.T) {
			path := withConfig(t, c.content)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), c.key) || !strings.Contains(err.Error(), path) {
				t.Errorf("error = %v, want one naming %s and the file", err, c.key)
			}
		})
	}
}

func TestEveryFaultIsReportedAtOnce(t *testing.T) {
	withConfig(t, "log:\n  level: loud\n")
	t.Setenv("ASSETMANAGER_LOG_TO_FILE", "yes")
	_, err := Load()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, s := range []string{"log.level", "ASSETMANAGER_LOG_TO_FILE"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("%s missing from %v", s, err)
		}
	}
}

func TestAnUnusablePortStopsTheService(t *testing.T) {
	withConfig(t, "")
	t.Setenv("ASSETMANAGER_PORT", "80a")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ASSETMANAGER_PORT") {
		t.Errorf("error = %v, want one naming ASSETMANAGER_PORT", err)
	}
	t.Setenv("ASSETMANAGER_PORT", "")
	path := withConfig(t, "server:\n  port: 70000\n")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "server.port") || !strings.Contains(err.Error(), path) {
		t.Errorf("error = %v, want one naming server.port and the file", err)
	}
	withConfig(t, "server:\n  port: 9000\n")
	t.Setenv("ASSETMANAGER_PORT", " 9100 ")
	if cfg := mustLoad(t); cfg.Server.Port != "9100" {
		t.Errorf("port = %q, want 9100", cfg.Server.Port)
	}
}
