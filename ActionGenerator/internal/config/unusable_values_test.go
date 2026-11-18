// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests AG-OPS-15: a value ActionGenerator cannot use, from the environment or
// the file, stops it at startup with an error naming the setting; the
// accepted spellings load, and an empty variable counts as unset.

package config

import (
	"strings"
	"testing"
)

func TestAnUnusableEnvironmentValueStopsTheService(t *testing.T) {
	for _, c := range []struct{ env, value string }{
		{"ACTIONGENERATOR_AI_TIMEOUT_SECONDS", "abc"},
		{"ACTIONGENERATOR_AI_TIMEOUT_SECONDS", "0"},
		{"ACTIONGENERATOR_AI_TIMEOUT_SECONDS", "-5"},
		{"ACTIONGENERATOR_AI_TIMEOUT_SECONDS", "1.5"},
		{"ACTIONGENERATOR_JS_EXEC_TIMEOUT_SECONDS", "-1"},
		{"ACTIONGENERATOR_JS_EXEC_TIMEOUT_SECONDS", "5s"},
		{"ACTIONGENERATOR_HEARTBEAT_INTERVAL_SECONDS", "0"},
		{"ACTIONGENERATOR_LOG_TO_CONSOLE", "yes"},
		{"ACTIONGENERATOR_LOG_TO_FILE", "on"},
		{"ACTIONGENERATOR_LOG_LEVEL", "verbose"},
	} {
		t.Run(c.env+"="+c.value, func(t *testing.T) {
			t.Setenv("ACTIONGENERATOR_CONFIG", "/nonexistent/config.yaml")
			t.Setenv(c.env, c.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), c.env) {
				t.Errorf("error = %v, want one naming %s", err, c.env)
			}
		})
	}
}

func TestTheAcceptedEnvironmentSpellingsLoad(t *testing.T) {
	t.Setenv("ACTIONGENERATOR_CONFIG", "/nonexistent/config.yaml")
	t.Setenv("ACTIONGENERATOR_AI_TIMEOUT_SECONDS", " 120 ")
	t.Setenv("ACTIONGENERATOR_JS_EXEC_TIMEOUT_SECONDS", "0")
	t.Setenv("ACTIONGENERATOR_HEARTBEAT_INTERVAL_SECONDS", "")
	t.Setenv("ACTIONGENERATOR_LOG_TO_CONSOLE", "FALSE")
	t.Setenv("ACTIONGENERATOR_LOG_TO_FILE", "1")
	t.Setenv("ACTIONGENERATOR_LOG_LEVEL", "debug")
	cfg := mustLoad(t)
	if cfg.AI.TimeoutSeconds != 120 || cfg.AI.JSExecTimeoutSeconds != 0 || cfg.Heartbeat.IntervalSeconds != 10 ||
		cfg.Log.ToConsole || !cfg.Log.ToFile || cfg.Log.Level != "DEBUG" {
		t.Errorf("config = %+v", cfg)
	}
}

func TestAnUnusableFileValueStopsTheService(t *testing.T) {
	for _, c := range []struct{ content, key string }{
		{"ai:\n  timeout_seconds: -5\n", "ai.timeout_seconds"},
		{"ai:\n  js_exec_timeout_seconds: -1\n", "ai.js_exec_timeout_seconds"},
		{"heartbeat:\n  interval_seconds: -10\n", "heartbeat.interval_seconds"},
		{"log:\n  level: verbose\n", "log.level"},
		{"log:\n  to_file: yes please\n", "to_file"},
	} {
		t.Run(c.key, func(t *testing.T) {
			path := withConfig(t, c.content)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), c.key) || !strings.Contains(err.Error(), path) {
				t.Errorf("error = %v, want one naming %s and the file", err, c.key)
			}
		})
	}
	withConfig(t, "log:\n  level: warn\n  to_console: true\n  to_file: false\n")
	if got := mustLoad(t).Log.Level; got != "WARN" {
		t.Errorf("level = %q, want WARN", got)
	}
}

func TestEveryFaultIsReportedAtOnce(t *testing.T) {
	withConfig(t, "heartbeat:\n  interval_seconds: -10\n")
	t.Setenv("ACTIONGENERATOR_LOG_TO_FILE", "yes")
	t.Setenv("ACTIONGENERATOR_AI_TIMEOUT_SECONDS", "x")
	_, err := Load()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, s := range []string{"heartbeat.interval_seconds", "ACTIONGENERATOR_LOG_TO_FILE", "ACTIONGENERATOR_AI_TIMEOUT_SECONDS"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("%s missing from %v", s, err)
		}
	}
}
