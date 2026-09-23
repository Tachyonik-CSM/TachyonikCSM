// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that configuration is read the way the three layers promise: built-in
// defaults, then config.yaml on top, then the environment on top of that.
//
// The service blocks are applied from a table rather than a repeated block per
// service, which is what makes a missing entry silent — the code still compiles
// and that one service quietly keeps its default. These tests are what notices.

package config

import "testing"

// The shipped example must be readable by the loader that reads it, and every
// value in it must land where it says. This is also what keeps
// config.yaml.example honest as settings are added.
func TestExampleConfigParses(t *testing.T) {
	t.Setenv("ACTIONGENERATOR_CONFIG", "../../config.yaml.example")
	cfg := Load()

	if cfg.AI.TimeoutSeconds != 300 {
		t.Errorf("ai.timeout_seconds = %d, want 300", cfg.AI.TimeoutSeconds)
	}
	if cfg.AI.JSExecTimeoutSeconds != 5 {
		t.Errorf("ai.js_exec_timeout_seconds = %d, want 5", cfg.AI.JSExecTimeoutSeconds)
	}
	if cfg.Heartbeat.IntervalSeconds != 10 {
		t.Errorf("heartbeat.interval_seconds = %d, want 10", cfg.Heartbeat.IntervalSeconds)
	}

	// Every service block, so a missing table entry cannot pass unnoticed.
	for _, svc := range []struct {
		name string
		got  ServiceConfig
		url  string
	}{
		{"assetmanager", cfg.AssetManager, "http://localhost:8081"},
		{"systemmanager", cfg.SystemManager, "http://localhost:8083"},
		{"resourcemanager", cfg.ResourceManager, "http://localhost:8080"},
		{"actionmanager", cfg.ActionManager, "http://localhost:8082"},
		{"ai_manager", cfg.AIManager, "http://localhost:8085"},
	} {
		if svc.got.URL != svc.url {
			t.Errorf("%s url = %q, want %q", svc.name, svc.got.URL, svc.url)
		}
		if svc.got.InternalServiceKey == "" {
			t.Errorf("%s internal_service_key did not survive the file override", svc.name)
		}
	}
}

// Defaults apply when there is no file at all, and the environment beats them.
func TestEnvOverridesBeatDefaults(t *testing.T) {
	t.Setenv("ACTIONGENERATOR_CONFIG", "/nonexistent/config.yaml")

	cfg := Load()
	// The ports, not just one of them: ActionManager and SystemManager sit next
	// to each other in the struct literal and were once transposed there, which
	// only shows in a deployment that configures neither.
	for _, svc := range []struct {
		name string
		got  string
		want string
	}{
		{"assetmanager", cfg.AssetManager.URL, "http://localhost:8081"},
		{"systemmanager", cfg.SystemManager.URL, "http://localhost:8083"},
		{"resourcemanager", cfg.ResourceManager.URL, "http://localhost:8080"},
		{"actionmanager", cfg.ActionManager.URL, "http://localhost:8082"},
		{"ai_manager", cfg.AIManager.URL, "http://localhost:8085"},
	} {
		if svc.got != svc.want {
			t.Errorf("default %s url = %q, want %q", svc.name, svc.got, svc.want)
		}
	}
	if cfg.AI.JSExecTimeoutSeconds != 5 {
		t.Errorf("default js exec budget = %d, want 5", cfg.AI.JSExecTimeoutSeconds)
	}

	t.Setenv("ASSETMANAGER_URL", "http://assets.example:9000")
	t.Setenv("ASSETMANAGER_INTERNAL_SERVICE_KEY", "from-env")
	// AIManager is the one service whose two variables do not share a prefix,
	// which is exactly the pair a prefix-derived table would get wrong.
	t.Setenv("ACTIONGENERATOR_AI_MANAGER_URL", "http://ai.example:9001")
	t.Setenv("AIMANAGER_INTERNAL_SERVICE_KEY", "ai-key")
	t.Setenv("ACTIONGENERATOR_JS_EXEC_TIMEOUT_SECONDS", "30")

	cfg = Load()
	if cfg.AssetManager.URL != "http://assets.example:9000" || cfg.AssetManager.InternalServiceKey != "from-env" {
		t.Errorf("assetmanager from env = %+v", cfg.AssetManager)
	}
	if cfg.AIManager.URL != "http://ai.example:9001" || cfg.AIManager.InternalServiceKey != "ai-key" {
		t.Errorf("aimanager from env = %+v", cfg.AIManager)
	}
	if cfg.AI.JSExecTimeoutSeconds != 30 {
		t.Errorf("js exec budget from env = %d, want 30", cfg.AI.JSExecTimeoutSeconds)
	}
}

// Zero is a meaningful value for the budget — "run unguarded" — and must be
// distinguishable from an unset or unparseable variable.
func TestJSExecBudgetCanBeDisabled(t *testing.T) {
	t.Setenv("ACTIONGENERATOR_CONFIG", "/nonexistent/config.yaml")
	t.Setenv("ACTIONGENERATOR_JS_EXEC_TIMEOUT_SECONDS", "0")
	if got := Load().AI.JSExecTimeoutSeconds; got != 0 {
		t.Errorf("budget = %d with an explicit 0, want the guard disabled", got)
	}

	t.Setenv("ACTIONGENERATOR_JS_EXEC_TIMEOUT_SECONDS", "not-a-number")
	if got := Load().AI.JSExecTimeoutSeconds; got != 5 {
		t.Errorf("budget = %d for an unparseable value, want the default 5", got)
	}
}
