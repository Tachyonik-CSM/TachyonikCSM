// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Keeps config.yaml.example loadable and complete: it must parse into
// FileConfig with every key known, and set every option the file can carry.

package config

import (
	"bytes"
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestTheExampleConfigLoadsWithEveryOption(t *testing.T) {
	data, err := os.ReadFile("../../config.yaml.example")
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	var fc FileConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // a misspelt key would otherwise be silently ignored
	if err := dec.Decode(&fc); err != nil {
		t.Fatalf("the example does not parse into FileConfig: %v", err)
	}

	for name, v := range map[string]string{
		"assetmanager.url": fc.AssetManager.URL, "assetmanager.internal_service_key": fc.AssetManager.InternalServiceKey,
		"systemmanager.url": fc.SystemManager.URL, "systemmanager.internal_service_key": fc.SystemManager.InternalServiceKey,
		"resourcemanager.url": fc.ResourceManager.URL, "resourcemanager.internal_service_key": fc.ResourceManager.InternalServiceKey,
		"actionmanager.url": fc.ActionManager.URL, "actionmanager.internal_service_key": fc.ActionManager.InternalServiceKey,
		"toolmanager.url": fc.ToolManager.URL, "toolmanager.internal_service_key": fc.ToolManager.InternalServiceKey,
		"ai_manager.url": fc.AIManager.URL, "ai_manager.internal_service_key": fc.AIManager.InternalServiceKey,
		"log.file_path": fc.Log.FilePath, "log.level": fc.Log.Level,
	} {
		if v == "" {
			t.Errorf("the example does not set %s", name)
		}
	}
	if fc.AI.TimeoutSeconds == 0 || fc.Heartbeat.IntervalSeconds == 0 || fc.Execution.TimeoutSeconds == 0 {
		t.Error("the example does not set ai.timeout_seconds, heartbeat.interval_seconds and execution.timeout_seconds")
	}
	if !fc.Log.ToConsole || !fc.Log.ToFile {
		t.Error("the example should spell out log.to_console and log.to_file")
	}
}
