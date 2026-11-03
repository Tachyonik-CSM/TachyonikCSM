// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the instance-mode resolution order: appliance by default, overridden by
// config.yaml, overridden in turn by the environment.

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Absent config + env, the instance mode defaults to appliance.
func TestInstanceModeDefaultsAppliance(t *testing.T) {
	t.Setenv("ASSETMANAGER_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	t.Setenv("ASSETMANAGER_INSTANCE_MODE", "")
	if cfg := Load(); cfg.Instance.Mode != ModeAppliance {
		t.Errorf("default instance.mode = %q, want %q", cfg.Instance.Mode, ModeAppliance)
	}
}

// instance.mode is read from the config file (catches yaml-tag typos).
func TestInstanceModeFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("instance:\n  mode: saas\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASSETMANAGER_CONFIG", path)
	t.Setenv("ASSETMANAGER_INSTANCE_MODE", "")
	if cfg := Load(); cfg.Instance.Mode != ModeSaaS {
		t.Errorf("instance.mode = %q, want %q", cfg.Instance.Mode, ModeSaaS)
	}
}

// The env var overrides the config file.
func TestInstanceModeEnvOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("instance:\n  mode: appliance\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASSETMANAGER_CONFIG", path)
	t.Setenv("ASSETMANAGER_INSTANCE_MODE", "saas")
	if cfg := Load(); cfg.Instance.Mode != ModeSaaS {
		t.Errorf("env-overridden instance.mode = %q, want %q", cfg.Instance.Mode, ModeSaaS)
	}
}
