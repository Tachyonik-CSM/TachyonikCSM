// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the service settings in AssetManager's layout — aimanager: and
// SOURCEANALYSER_<SERVICE>_URL / _INTERNAL_SERVICE_KEY — and the names before
// 2026-11-20, read for one release with a warning: the ai_manager: section,
// the unprefixed variables, and SystemManager falling back to ResourceManager's
// key (SA-OPS-17, SA-OPS-19, SA-OPS-20; docs/plans/sourceanalyser-service-sections.md).

package config

import (
	"strings"
	"testing"
)

const newLayout = `resourcemanager:
  url: http://rm:8080
  internal_service_key: rm-key
systemmanager:
  url: http://sm:8083
  internal_service_key: sm-key
aimanager:
  url: http://aim:8085
  internal_service_key: aim-key
`

func wantServices(t *testing.T, cfg *Config, rm, sm, aim ServiceConfig) {
	t.Helper()
	if cfg.ResourceManager != rm || cfg.SystemManager != sm || cfg.AIManager != aim {
		t.Errorf("services = %+v %+v %+v,\nwant %+v %+v %+v", cfg.ResourceManager, cfg.SystemManager, cfg.AIManager, rm, sm, aim)
	}
}

func wantWarnings(t *testing.T, cfg *Config, parts ...string) {
	t.Helper()
	if len(cfg.Warnings) != len(parts) {
		t.Fatalf("warnings = %q, want %d", cfg.Warnings, len(parts))
	}
	for i, p := range parts {
		if !strings.Contains(cfg.Warnings[i], p) {
			t.Errorf("warning %d = %q, want %q", i, cfg.Warnings[i], p)
		}
	}
}

func TestTheNewLayoutFromTheFile(t *testing.T) {
	withConfig(t, newLayout)
	cfg := mustLoad(t)
	wantServices(t, cfg, ServiceConfig{"http://rm:8080", "rm-key"}, ServiceConfig{"http://sm:8083", "sm-key"}, ServiceConfig{"http://aim:8085", "aim-key"})
	wantWarnings(t, cfg)
}

func TestTheNewLayoutFromTheEnvironment(t *testing.T) {
	withConfig(t, newLayout)
	for name, value := range map[string]string{
		"SOURCEANALYSER_RESOURCEMANAGER_URL":                  "http://rm2",
		"SOURCEANALYSER_RESOURCEMANAGER_INTERNAL_SERVICE_KEY": "rm2",
		"SOURCEANALYSER_SYSTEMMANAGER_URL":                    "http://sm2",
		"SOURCEANALYSER_SYSTEMMANAGER_INTERNAL_SERVICE_KEY":   "sm2",
		"SOURCEANALYSER_AIMANAGER_URL":                        "http://aim2",
		"SOURCEANALYSER_AIMANAGER_INTERNAL_SERVICE_KEY":       "aim2",
	} {
		t.Setenv(name, value)
	}
	cfg := mustLoad(t)
	wantServices(t, cfg, ServiceConfig{"http://rm2", "rm2"}, ServiceConfig{"http://sm2", "sm2"}, ServiceConfig{"http://aim2", "aim2"})
	wantWarnings(t, cfg)
}

func TestTheOldAIManagerSectionIsReadWithAWarning(t *testing.T) {
	withConfig(t, "ai_manager:\n  url: http://old:8085\n  internal_service_key: old-key\n")
	cfg := mustLoad(t)
	if cfg.AIManager != (ServiceConfig{"http://old:8085", "old-key"}) {
		t.Errorf("AIManager = %+v, want the old section's", cfg.AIManager)
	}
	wantWarnings(t, cfg, "ai_manager is deprecated: rename it to aimanager")

	withConfig(t, "ai_manager:\n  url: http://old:8085\naimanager:\n  url: http://new:8085\n")
	cfg = mustLoad(t)
	if cfg.AIManager.URL != "http://new:8085" {
		t.Errorf("AIManager = %+v, want the new section's", cfg.AIManager)
	}
	wantWarnings(t, cfg, "ai_manager is ignored, as aimanager is set")
}

func TestTheOldVariablesAreReadWithAWarning(t *testing.T) {
	withConfig(t, "")
	t.Setenv("RESOURCEMANAGER_URL", "http://rm-old")
	t.Setenv("RESOURCEMANAGER_INTERNAL_SERVICE_KEY", "rm-old")
	t.Setenv("SYSTEMMANAGER_INTERNAL_SERVICE_KEY", "sm-old")
	t.Setenv("SOURCEANALYSER_AI_MANAGER_URL", "http://aim-old")
	t.Setenv("AIMANAGER_INTERNAL_SERVICE_KEY", "aim-old")
	cfg := mustLoad(t)
	wantServices(t, cfg, ServiceConfig{"http://rm-old", "rm-old"}, ServiceConfig{"http://localhost:8083", "sm-old"}, ServiceConfig{"http://aim-old", "aim-old"})
	wantWarnings(t, cfg,
		"RESOURCEMANAGER_URL is deprecated: set SOURCEANALYSER_RESOURCEMANAGER_URL",
		"RESOURCEMANAGER_INTERNAL_SERVICE_KEY is deprecated",
		"SYSTEMMANAGER_INTERNAL_SERVICE_KEY is deprecated",
		"SOURCEANALYSER_AI_MANAGER_URL is deprecated: set SOURCEANALYSER_AIMANAGER_URL",
		"AIMANAGER_INTERNAL_SERVICE_KEY is deprecated",
	)
}

func TestANewVariableWinsOverAnOldOne(t *testing.T) {
	withConfig(t, newLayout)
	t.Setenv("SOURCEANALYSER_AIMANAGER_URL", "http://new")
	t.Setenv("SOURCEANALYSER_AI_MANAGER_URL", "http://old")
	t.Setenv("RESOURCEMANAGER_URL", "http://rm-a")
	t.Setenv("RESOURCEMANAGER_API_BASE_URL", "http://rm-b")
	cfg := mustLoad(t)
	if cfg.AIManager.URL != "http://new" || cfg.ResourceManager.URL != "http://rm-a" {
		t.Errorf("URLs = %q %q, want the new AIManager variable and the first old ResourceManager one", cfg.AIManager.URL, cfg.ResourceManager.URL)
	}
	wantWarnings(t, cfg,
		"RESOURCEMANAGER_URL is deprecated",
		"RESOURCEMANAGER_API_BASE_URL is ignored, as an older name is set too",
		"SOURCEANALYSER_AI_MANAGER_URL is ignored, as SOURCEANALYSER_AIMANAGER_URL is set",
	)
}

// SA-OPS-17: for one release, with a warning.
func TestSystemManagerFallsBackToResourceManagersKeyWithAWarning(t *testing.T) {
	withConfig(t, "resourcemanager:\n  internal_service_key: rm-key\n")
	cfg := mustLoad(t)
	if cfg.SystemManager.InternalServiceKey != "rm-key" {
		t.Errorf("SystemManager key = %q, want ResourceManager's", cfg.SystemManager.InternalServiceKey)
	}
	wantWarnings(t, cfg, "systemmanager.internal_service_key")
}

func TestTheDockerTemplateLoadsWithoutAWarning(t *testing.T) {
	t.Setenv("SOURCEANALYSER_CONFIG", "../../../deployment/docker/config-templates/sourceanalyser.yaml")
	wantWarnings(t, mustLoad(t))
}
