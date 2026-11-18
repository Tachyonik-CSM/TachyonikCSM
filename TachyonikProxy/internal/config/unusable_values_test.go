// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that a log level the proxy cannot use stops it at startup, from
// TACHYONIKPROXY_LOG_LEVEL as from the file, naming the setting, and that
// every such fault is reported at once (docs/plans/strict-setting-values.md).

package config

import (
	"strings"
	"testing"
)

func TestAnUnusableEnvironmentValueStopsTheProxy(t *testing.T) {
	for _, v := range []string{"verbose", "WARNING", "trace"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("TACHYONIKPROXY_LOG_LEVEL", v)
			_, err := LoadFrom(writeConfig(t, ""))
			if err == nil || !strings.Contains(err.Error(), "TACHYONIKPROXY_LOG_LEVEL") {
				t.Errorf("error = %v, want one naming TACHYONIKPROXY_LOG_LEVEL", err)
			}
		})
	}
}

func TestTheAcceptedSpellingsLoad(t *testing.T) {
	t.Setenv("TACHYONIKPROXY_LOG_LEVEL", " debug ")
	if cfg := mustLoadFrom(t, writeConfig(t, "log:\n  level: ERROR\n")); cfg.Log.Level != "DEBUG" {
		t.Errorf("env debug read as %q, want DEBUG", cfg.Log.Level)
	}
	t.Setenv("TACHYONIKPROXY_LOG_LEVEL", "") // empty counts as unset
	if cfg := mustLoadFrom(t, writeConfig(t, "log:\n  level: warn\n")); cfg.Log.Level != "WARN" {
		t.Errorf("file warn read as %q, want WARN", cfg.Log.Level)
	}
	if cfg := mustLoadFrom(t, writeConfig(t, "log:\n  level: \"\"\n")); cfg.Log.Level != "INFO" {
		t.Errorf("empty file level read as %q, want the default INFO", cfg.Log.Level)
	}
}

func TestAnUnusableFileValueStopsTheProxy(t *testing.T) {
	t.Setenv("TACHYONIKPROXY_LOG_LEVEL", "")
	path := writeConfig(t, "log:\n  level: loud\n")
	_, err := LoadFrom(path)
	if err == nil || !strings.Contains(err.Error(), "log.level") || !strings.Contains(err.Error(), path) {
		t.Errorf("error = %v, want one naming log.level and the file", err)
	}
}

func TestEveryFaultIsReportedAtOnce(t *testing.T) {
	t.Setenv("TACHYONIKPROXY_LOG_LEVEL", "loud")
	_, err := LoadFrom(writeConfig(t, "connection_mode: inbund\nlog:\n  level: loud\n"))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, s := range []string{"log.level", "TACHYONIKPROXY_LOG_LEVEL", "connection_mode"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("%s missing from %v", s, err)
		}
	}
}
