// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the old instance.mode setting (AM-OPS-03): the mode is
// SystemManager's, learned at startup, so Load sets none. A setting from the
// file or the environment is still accepted for one release, whatever it
// says, and kept only to be warned about.

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSetsNoInstanceMode(t *testing.T) {
	t.Setenv("ASSETMANAGER_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	t.Setenv("ASSETMANAGER_INSTANCE_MODE", "")
	if cfg := mustLoad(t); cfg.Instance.Mode != "" || cfg.Instance.Configured != "" {
		t.Errorf("%+v, want no mode until SystemManager tells it", cfg.Instance)
	}
}

func TestTheOldModeSettingIsKeptOnlyToWarn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("instance:\n  mode: sass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASSETMANAGER_CONFIG", path)
	t.Setenv("ASSETMANAGER_INSTANCE_MODE", "")
	cfg := mustLoad(t)
	if cfg.Instance.Mode != "" || cfg.Instance.Configured != "sass" || !strings.Contains(cfg.Instance.ConfiguredFrom, path) {
		t.Errorf("from the file: %+v", cfg.Instance)
	}
	t.Setenv("ASSETMANAGER_INSTANCE_MODE", "appliance")
	cfg = mustLoad(t)
	if cfg.Instance.Mode != "" || cfg.Instance.Configured != "appliance" || cfg.Instance.ConfiguredFrom != "ASSETMANAGER_INSTANCE_MODE" {
		t.Errorf("from the environment: %+v", cfg.Instance)
	}
}
