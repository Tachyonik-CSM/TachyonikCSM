// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins how the endpoints of the services AssetManager calls are configured:
// their defaults, the config.yaml sections they are read from (ResourceManager's
// is "rscmanager"), and the environment variables that override both.

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServiceEndpoints(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	t.Setenv("ASSETMANAGER_CONFIG", path)

	// Defaults, with no file.
	cfg := Load()
	if cfg.ResourceManager.URL != "http://localhost:8080" || cfg.SystemManager.URL != "http://localhost:8083" || cfg.AIManager.URL != "" {
		t.Errorf("defaults: %+v %+v %+v", cfg.ResourceManager, cfg.SystemManager, cfg.AIManager)
	}

	yaml := `
rscmanager:
  url: http://rm:8080
  internal_service_key: rm-key
systemmanager:
  url: http://sm:8083
  internal_service_key: sm-key
aimanager:
  url: http://ai:8085
  internal_service_key: ai-key
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = Load()
	for name, got := range map[string][2]string{
		"resource": {cfg.ResourceManager.URL, cfg.ResourceManager.InternalServiceKey},
		"system":   {cfg.SystemManager.URL, cfg.SystemManager.InternalServiceKey},
		"ai":       {cfg.AIManager.URL, cfg.AIManager.InternalServiceKey},
	} {
		want := map[string][2]string{
			"resource": {"http://rm:8080", "rm-key"},
			"system":   {"http://sm:8083", "sm-key"},
			"ai":       {"http://ai:8085", "ai-key"},
		}[name]
		if got != want {
			t.Errorf("%s from file: %v, want %v", name, got, want)
		}
	}

	t.Setenv("ASSETMANAGER_RESOURCEMANAGER_URL", "http://rm-env")
	t.Setenv("ASSETMANAGER_RESOURCEMANAGER_INTERNAL_SERVICE_KEY", "rm-env-key")
	t.Setenv("ASSETMANAGER_SYSTEMMANAGER_URL", "http://sm-env")
	t.Setenv("ASSETMANAGER_SYSTEMMANAGER_INTERNAL_SERVICE_KEY", "sm-env-key")
	t.Setenv("ASSETMANAGER_AIMANAGER_URL", "http://ai-env")
	t.Setenv("ASSETMANAGER_AIMANAGER_INTERNAL_SERVICE_KEY", "ai-env-key")
	cfg = Load()
	if cfg.ResourceManager != (ServiceConfig{"http://rm-env", "rm-env-key"}) ||
		cfg.SystemManager != (ServiceConfig{"http://sm-env", "sm-env-key"}) ||
		cfg.AIManager != (ServiceConfig{"http://ai-env", "ai-env-key"}) {
		t.Errorf("env overrides: %+v %+v %+v", cfg.ResourceManager, cfg.SystemManager, cfg.AIManager)
	}
}
