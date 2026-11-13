// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests which configurations stop the proxy and which it runs on: a file that
// cannot be read or parsed, or an unknown connection_mode, is refused; a key
// it does not know is reported and the proxy keeps running, because its own
// newer versions write keys an older one may read after a rollback; a missing
// file is a fresh install. The packaged default configuration must load.

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustLoadFrom(t *testing.T, path string) *Config {
	t.Helper()
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom(%s): %v", path, err)
	}
	return cfg
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAnUnusableConfigurationStopsTheProxy(t *testing.T) {
	for _, c := range []struct{ what, content, want string }{
		{"a file that cannot be parsed", "server:\n  port: [9100\n", "cannot be used"},
		{"a value of the wrong kind", "netscan:\n  concurrency: lots\n", "cannot be used"},
		{"an unknown connection mode", "connection_mode: inbund\n", `connection_mode in`},
	} {
		if _, err := LoadFrom(writeConfig(t, c.content)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error mentioning %q", c.what, err, c.want)
		}
	}
}

func TestAnUnreadableFileStopsTheProxy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	path := writeConfig(t, "connection_mode: inbound\n")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFrom(path); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Errorf("an unreadable file: %v, want it refused", err)
	}
}

// A newer proxy saves keys an older one does not know; after a rollback the
// older one must still start, saying what it ignored.
func TestAnUnknownKeyIsReportedNotRefused(t *testing.T) {
	cfg := mustLoadFrom(t, writeConfig(t, "connection_mode: inbound\nfuture_feature:\n  enabled: true\nnetscan:\n  newer_knob: 3\n"))
	keys := strings.Join(cfg.UnknownKeys(), " | ")
	if !strings.Contains(keys, "future_feature") || !strings.Contains(keys, "newer_knob") {
		t.Errorf("unknown keys = %q, want both reported", keys)
	}
	if cfg.ConnectionMode != ConnectionInbound {
		t.Errorf("the known settings were not read: connection_mode %q", cfg.ConnectionMode)
	}
}

func TestWhatTheProxyRunsOn(t *testing.T) {
	if cfg := mustLoadFrom(t, filepath.Join(t.TempDir(), "missing.yaml")); cfg.ConnectionMode != "" {
		t.Errorf("no file: connection_mode %q, want unset (outbound)", cfg.ConnectionMode)
	}
	mustLoadFrom(t, writeConfig(t, "")) // an empty file is an empty configuration
	if cfg := mustLoadFrom(t, writeConfig(t, "connection_mode: ' Inbound '\n")); cfg.ConnectionMode != ConnectionInbound {
		t.Errorf("connection_mode ' Inbound ' read as %q", cfg.ConnectionMode)
	}
	if cfg := mustLoadFrom(t, writeConfig(t, "server:\n  port: \"9100\"\n")); len(cfg.UnknownKeys()) != 0 {
		t.Errorf("a file of known keys reported %v", cfg.UnknownKeys())
	}
}

// What the installer and the packages put in place must load, without
// unknown keys.
func TestThePackagedDefaultConfigurationLoads(t *testing.T) {
	cfg := mustLoadFrom(t, "../../packaging/config.yaml.default")
	if len(cfg.UnknownKeys()) != 0 {
		t.Errorf("packaging/config.yaml.default has keys the proxy does not know: %v", cfg.UnknownKeys())
	}
}
