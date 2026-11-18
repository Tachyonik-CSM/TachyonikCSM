// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that a number, true/false or log-level value ActionExecutor cannot use
// stops it at startup, from the environment as from the file, naming the
// setting; that the accepted spellings load; and that every fault is reported
// at once (docs/plans/strict-setting-values.md).

package config

import (
	"strings"
	"testing"
)

func TestAnUnusableEnvironmentValueStopsTheService(t *testing.T) {
	for _, c := range []struct{ env, value string }{
		{"ACTIONEXECUTOR_AI_TIMEOUT_SECONDS", "abc"},
		{"ACTIONEXECUTOR_AI_TIMEOUT_SECONDS", "0"},
		{"ACTIONEXECUTOR_EXECUTION_TIMEOUT_SECONDS", "-1"},
		{"ACTIONEXECUTOR_EXECUTION_TIMEOUT_SECONDS", "280s"},
		{"ACTIONEXECUTOR_HEARTBEAT_INTERVAL_SECONDS", "1.5"},
		{"ACTIONEXECUTOR_LOG_TO_CONSOLE", "yes"},
		{"ACTIONEXECUTOR_LOG_TO_FILE", "on"},
		{"ACTIONEXECUTOR_LOG_LEVEL", "verbose"},
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
	withConfig(t, "")
	t.Setenv("ACTIONEXECUTOR_AI_TIMEOUT_SECONDS", " 120 ")
	t.Setenv("ACTIONEXECUTOR_EXECUTION_TIMEOUT_SECONDS", "60")
	t.Setenv("ACTIONEXECUTOR_HEARTBEAT_INTERVAL_SECONDS", "")
	t.Setenv("ACTIONEXECUTOR_LOG_TO_CONSOLE", "FALSE")
	t.Setenv("ACTIONEXECUTOR_LOG_TO_FILE", "1")
	t.Setenv("ACTIONEXECUTOR_LOG_LEVEL", "debug")
	cfg := mustLoad(t)
	if cfg.AI.TimeoutSeconds != 120 || cfg.Execution.TimeoutSeconds != 60 || cfg.Heartbeat.IntervalSeconds != 10 ||
		cfg.Log.ToConsole || !cfg.Log.ToFile || cfg.Log.Level != "DEBUG" {
		t.Errorf("config = %+v %+v %+v %+v", cfg.AI, cfg.Execution, cfg.Heartbeat, cfg.Log)
	}
}

func TestAnUnusableFileValueStopsTheService(t *testing.T) {
	for _, c := range []struct{ content, key string }{
		{"ai:\n  timeout_seconds: -5\n", "ai.timeout_seconds"},
		{"execution:\n  timeout_seconds: -1\n", "execution.timeout_seconds"},
		{"heartbeat:\n  interval_seconds: -10\n", "heartbeat.interval_seconds"},
		{"log:\n  level: verbose\n", "log.level"},
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
	withConfig(t, "execution:\n  timeout_seconds: -1\n")
	t.Setenv("ACTIONEXECUTOR_LOG_TO_FILE", "yes")
	t.Setenv("ACTIONEXECUTOR_AI_TIMEOUT_SECONDS", "x")
	_, err := Load()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, s := range []string{"execution.timeout_seconds", "ACTIONEXECUTOR_LOG_TO_FILE", "ACTIONEXECUTOR_AI_TIMEOUT_SECONDS"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("%s missing from %v", s, err)
		}
	}
}
