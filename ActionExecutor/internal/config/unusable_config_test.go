// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that a configuration ActionExecutor cannot use stops it instead of running it
// on defaults: a file that cannot be read or parsed, or a key it does not
// know. A missing or empty file still runs on the defaults and the
// environment.

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustLoad(t *testing.T) *Config {
	t.Helper()
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func withConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACTIONEXECUTOR_CONFIG", path)
	return path
}

func TestAnUnusableConfigurationStopsTheService(t *testing.T) {
	for _, c := range []struct{ what, content, want string }{
		{"a file that cannot be parsed", "log:\n  level: [INFO\n", "cannot be used"},
		{"an unknown key", "lgo:\n  level: INFO\n", "lgo"},
		{"an unknown nested key", "log:\n  levle: INFO\n", "levle"},
	} {
		withConfig(t, c.content)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error mentioning %q", c.what, err, c.want)
		}
	}
}

func TestAnUnreadableFileStopsTheService(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	path := withConfig(t, "log:\n  level: INFO\n")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Errorf("an unreadable file: %v, want it refused", err)
	}
}

func TestAMissingOrEmptyFileStillStarts(t *testing.T) {
	t.Setenv("ACTIONEXECUTOR_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	mustLoad(t)
	withConfig(t, "")
	mustLoad(t)
}

// config.yaml.example loads under the strict rules: every key in it is one
// the service reads.
func TestTheExampleLoadsStrictly(t *testing.T) {
	t.Setenv("ACTIONEXECUTOR_CONFIG", "../../config.yaml.example")
	mustLoad(t)
}
