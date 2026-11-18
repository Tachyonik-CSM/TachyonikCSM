// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that a number, true/false or log-level value SourceImporter cannot use
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
		{"SOURCEIMPORTER_POLL_INTERVAL", "abc"},
		{"SOURCEIMPORTER_POLL_INTERVAL", "0"},
		{"SOURCEIMPORTER_POLL_INTERVAL", "5s"},
		{"SOURCEIMPORTER_HEARTBEAT_INTERVAL_SECONDS", "-10"},
		{"SOURCEIMPORTER_HEARTBEAT_INTERVAL_SECONDS", "1.5"},
		{"SOURCEIMPORTER_LOG_TO_CONSOLE", "yes"},
		{"SOURCEIMPORTER_LOG_TO_FILE", "on"},
		{"SOURCEIMPORTER_LOG_LEVEL", "verbose"},
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
	t.Setenv("SOURCEIMPORTER_POLL_INTERVAL", " 7 ")
	t.Setenv("SOURCEIMPORTER_HEARTBEAT_INTERVAL_SECONDS", "")
	t.Setenv("SOURCEIMPORTER_LOG_TO_CONSOLE", "FALSE")
	t.Setenv("SOURCEIMPORTER_LOG_TO_FILE", "1")
	t.Setenv("SOURCEIMPORTER_LOG_LEVEL", "debug")
	cfg := mustLoad(t)
	if cfg.Importer.PollInterval != 7 || cfg.Heartbeat.IntervalSeconds != 10 ||
		cfg.Log.ToConsole || !cfg.Log.ToFile || cfg.Log.Level != "DEBUG" {
		t.Errorf("config = %+v %+v %+v", cfg.Importer, cfg.Heartbeat, cfg.Log)
	}
}

func TestAnUnusableFileValueStopsTheService(t *testing.T) {
	for _, c := range []struct{ content, key string }{
		{"importer:\n  poll_interval: -5\n", "importer.poll_interval"},
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
	withConfig(t, "importer:\n  poll_interval: -5\n")
	t.Setenv("SOURCEIMPORTER_LOG_TO_FILE", "yes")
	t.Setenv("SOURCEIMPORTER_HEARTBEAT_INTERVAL_SECONDS", "x")
	_, err := Load()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, s := range []string{"importer.poll_interval", "SOURCEIMPORTER_LOG_TO_FILE", "SOURCEIMPORTER_HEARTBEAT_INTERVAL_SECONDS"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("%s missing from %v", s, err)
		}
	}
}
