// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that a configuration AssetManager cannot use stops it (AM-OPS-16)
// instead of running it on defaults: a file that cannot be read or parsed, a
// key it does not know. A missing file still runs a development setup on the
// defaults. The instance mode is SystemManager's (AM-OPS-03); see
// instance_test.go for the old setting.

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
	t.Setenv("ASSETMANAGER_CONFIG", path)
	t.Setenv("ASSETMANAGER_INSTANCE_MODE", "")
	return path
}

func TestAnUnusableConfigurationStopsTheService(t *testing.T) {
	for _, c := range []struct {
		what, content, env, want string
	}{
		{"a file that cannot be parsed", "server:\n  port: [8081\n", "", "cannot be used"},
		{"an unknown key", "instanse:\n  mode: saas\n", "", "instanse"},
		{"an unknown nested key", "server:\n  prot: 8081\n", "", "prot"},
	} {
		withConfig(t, c.content)
		t.Setenv("ASSETMANAGER_INSTANCE_MODE", c.env)
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error mentioning %q", c.what, err, c.want)
		}
	}
}

func TestAnUnreadableFileStopsTheService(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	path := withConfig(t, "instance:\n  mode: saas\n")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Errorf("an unreadable file: %v, want it refused", err)
	}
}

func TestWhatStillStarts(t *testing.T) {
	t.Setenv("ASSETMANAGER_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	t.Setenv("ASSETMANAGER_INSTANCE_MODE", "")
	mustLoad(t)
	withConfig(t, "")
	mustLoad(t) // an empty file is an empty configuration
}
