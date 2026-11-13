// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Keeps config.yaml.example complete and correct: every key FileConfig reads
// appears in it, nothing in it is a key FileConfig does not read, and loading
// it yields the values it states.

package config

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const examplePath = "../../config.yaml.example"

// yamlKeys lists the dotted keys of a struct type by its yaml tags.
func yamlKeys(t reflect.Type, prefix string) []string {
	var keys []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if name == "" {
			continue
		}
		if f.Type.Kind() == reflect.Struct {
			keys = append(keys, yamlKeys(f.Type, prefix+name+".")...)
		} else {
			keys = append(keys, prefix+name)
		}
	}
	return keys
}

// mapKeys lists the dotted leaf keys of a parsed YAML document.
func mapKeys(m map[string]interface{}, prefix string) []string {
	var keys []string
	for k, v := range m {
		if sub, ok := v.(map[string]interface{}); ok {
			keys = append(keys, mapKeys(sub, prefix+k+".")...)
		} else {
			keys = append(keys, prefix+k)
		}
	}
	return keys
}

func TestExampleListsEveryOption(t *testing.T) {
	data, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("example is not valid YAML: %v", err)
	}
	want := yamlKeys(reflect.TypeOf(FileConfig{}), "")
	got := mapKeys(doc, "")
	sort.Strings(want)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("example keys\n  %v\nconfig keys\n  %v", got, want)
	}
}

func TestExampleLoads(t *testing.T) {
	t.Setenv("ASSETMANAGER_CONFIG", examplePath)
	for _, v := range []string{"INSTANCE_MODE", "PORT", "HOST", "DB_PATH", "JWT_SECRET", "INTERNAL_SERVICE_KEY",
		"LOG_FILE", "LOG_TO_CONSOLE", "LOG_TO_FILE", "LOG_LEVEL",
		"RESOURCEMANAGER_URL", "RESOURCEMANAGER_INTERNAL_SERVICE_KEY", "SYSTEMMANAGER_URL",
		"SYSTEMMANAGER_INTERNAL_SERVICE_KEY", "AIMANAGER_URL", "AIMANAGER_INTERNAL_SERVICE_KEY"} {
		t.Setenv("ASSETMANAGER_"+v, "")
	}
	cfg := mustLoad(t)
	key := "CHANGE-THIS-INTERNAL-SERVICE-KEY"
	for name, c := range map[string]struct{ got, want interface{} }{
		"server.port":          {cfg.Server.Port, "8081"},
		"server.host":          {cfg.Server.Host, "localhost"},
		"instance.mode":        {cfg.Instance.Mode, ModeAppliance},
		"database.path":        {cfg.Database.Path, "/home/tachyon/data/AssetManager.db"},
		"auth.jwt_secret":      {cfg.Auth.JWTSecret, PlaceholderJWTSecret},
		"auth.internal_key":    {cfg.Auth.InternalServiceKey, key},
		"cors.allowed_origins": {strings.Join(cfg.CORS.AllowedOrigins, ","), "http://localhost:5173,http://localhost:3000"},
		"rscmanager":           {cfg.ResourceManager, ServiceConfig{"http://localhost:8080", key}},
		"systemmanager":        {cfg.SystemManager, ServiceConfig{"http://localhost:8083", key}},
		"aimanager":            {cfg.AIManager, ServiceConfig{"", key}},
		"log.file_path":        {cfg.Log.FilePath, "/home/tachyon/log/tachyonik-assetmanager.log"},
		"log.to_console":       {cfg.Log.ToConsole, false},
		"log.to_file":          {cfg.Log.ToFile, true},
		"log.level":            {cfg.Log.Level, "INFO"},
	} {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", name, c.got, c.want)
		}
	}
}
