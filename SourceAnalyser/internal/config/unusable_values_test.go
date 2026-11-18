// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that a number, true/false or log-level value SourceAnalyser cannot use
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
		{"SOURCEANALYSER_POLL_INTERVAL", "abc"},
		{"SOURCEANALYSER_POLL_INTERVAL", "0"},
		{"SOURCEANALYSER_JS_EXEC_TIMEOUT_SECONDS", "-1"},
		{"SOURCEANALYSER_JS_EXEC_TIMEOUT_SECONDS", "5s"},
		{"SOURCEANALYSER_MAX_CONTENT_BYTES", "64k"},
		{"SOURCEANALYSER_MAX_CONTENT_BYTES", "0"},
		{"SOURCEANALYSER_MAX_PDF_BYTES", "-1"},
		{"SOURCEANALYSER_HEARTBEAT_INTERVAL_SECONDS", "1.5"},
		{"SOURCEANALYSER_LOG_TO_CONSOLE", "yes"},
		{"SOURCEANALYSER_LOG_TO_FILE", "on"},
		{"SOURCEANALYSER_LOG_LEVEL", "verbose"},
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
	t.Setenv("SOURCEANALYSER_POLL_INTERVAL", " 7 ")
	t.Setenv("SOURCEANALYSER_JS_EXEC_TIMEOUT_SECONDS", "0")
	t.Setenv("SOURCEANALYSER_MAX_PDF_BYTES", "1048576")
	t.Setenv("SOURCEANALYSER_HEARTBEAT_INTERVAL_SECONDS", "")
	t.Setenv("SOURCEANALYSER_LOG_TO_CONSOLE", "FALSE")
	t.Setenv("SOURCEANALYSER_LOG_TO_FILE", "1")
	t.Setenv("SOURCEANALYSER_LOG_LEVEL", "debug")
	cfg := mustLoad(t)
	if cfg.Analyzer.PollInterval != 7 || cfg.Analyzer.JSExecTimeoutSeconds != 0 || cfg.Analyzer.MaxPDFBytes != 1048576 ||
		cfg.Heartbeat.IntervalSeconds != 10 || cfg.Log.ToConsole || !cfg.Log.ToFile || cfg.Log.Level != "DEBUG" {
		t.Errorf("config = %+v %+v %+v", cfg.Analyzer, cfg.Heartbeat, cfg.Log)
	}
}

func TestAnUnusableFileValueStopsTheService(t *testing.T) {
	for _, c := range []struct{ content, key string }{
		{"analyzer:\n  poll_interval: -5\n", "analyzer.poll_interval"},
		{"analyzer:\n  js_exec_timeout_seconds: -1\n", "analyzer.js_exec_timeout_seconds"},
		{"analyzer:\n  max_content_bytes: -1\n", "analyzer.max_content_bytes"},
		{"analyzer:\n  max_pdf_bytes: -1\n", "analyzer.max_pdf_bytes"},
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
	withConfig(t, "analyzer:\n  poll_interval: -5\n")
	t.Setenv("SOURCEANALYSER_LOG_TO_FILE", "yes")
	t.Setenv("SOURCEANALYSER_MAX_PDF_BYTES", "x")
	_, err := Load()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, s := range []string{"analyzer.poll_interval", "SOURCEANALYSER_LOG_TO_FILE", "SOURCEANALYSER_MAX_PDF_BYTES"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("%s missing from %v", s, err)
		}
	}
}
