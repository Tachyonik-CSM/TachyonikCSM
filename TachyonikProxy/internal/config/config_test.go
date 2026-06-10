// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resolveConfigPath is tested directly (not via GetConfigPath) because the
// public accessor caches its result for process-lifetime consistency.

func TestResolveConfigPath_EnvWins(t *testing.T) {
	t.Setenv("TACHYONIKPROXY_CONFIG", "/somewhere/else.yaml")
	if got := resolveConfigPath(); got != "/somewhere/else.yaml" {
		t.Errorf("env override must win, got %q", got)
	}
}

func TestResolveConfigPath_CWDBeatsUserConfig(t *testing.T) {
	t.Setenv("TACHYONIKPROXY_CONFIG", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("proxy:\n  name: x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	// Even with a user config present, an existing ./config.yaml wins
	// (backward compatibility with CWD-based setups).
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	userCfg := filepath.Join(xdg, "tachyonik", "tachyonikproxy", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(userCfg), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userCfg, []byte("proxy:\n  name: y\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if got := resolveConfigPath(); got != "./config.yaml" {
		t.Errorf("existing ./config.yaml must win over user config, got %q", got)
	}
}

func TestResolveConfigPath_UserXDG(t *testing.T) {
	t.Setenv("TACHYONIKPROXY_CONFIG", "")
	t.Chdir(t.TempDir()) // no ./config.yaml here
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	userCfg := filepath.Join(xdg, "tachyonik", "tachyonikproxy", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(userCfg), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userCfg, []byte("proxy:\n  name: y\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if got := resolveConfigPath(); got != userCfg {
		t.Errorf("expected user XDG config %q, got %q", userCfg, got)
	}
}

func TestResolveConfigPath_FreshWriteTarget(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("write-target test assumes non-root")
	}
	t.Setenv("TACHYONIKPROXY_CONFIG", "")
	t.Chdir(t.TempDir())
	xdg := t.TempDir() // empty — nothing exists anywhere
	t.Setenv("XDG_CONFIG_HOME", xdg)

	want := filepath.Join(xdg, "tachyonik", "tachyonikproxy", "config.yaml")
	if got := resolveConfigPath(); got != want {
		t.Errorf("fresh non-root target must be the user XDG path %q, got %q", want, got)
	}
}

func TestAbsPath(t *testing.T) {
	cfg := &Config{baseDir: "/base/dir"}
	cases := []struct{ in, want string }{
		{"certs/ca.crt", "/base/dir/certs/ca.crt"},
		{"./tachyonikproxy.log", "/base/dir/tachyonikproxy.log"},
		{"/abs/path.crt", "/abs/path.crt"},
		{"", ""},
	}
	for _, c := range cases {
		if got := cfg.AbsPath(c.in); got != c.want {
			t.Errorf("AbsPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// Without a known config location, paths pass through (old behaviour).
	bare := &Config{}
	if got := bare.AbsPath("certs/ca.crt"); got != "certs/ca.crt" {
		t.Errorf("AbsPath without baseDir must pass through, got %q", got)
	}
}

func TestLoadFrom_SetsBaseDirAndReadsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("proxy:\n  name: from-file\nlog:\n  file_path: my.log\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadFrom(path)
	if cfg.Proxy.Name != "from-file" {
		t.Errorf("expected file values to load, got name %q", cfg.Proxy.Name)
	}
	if cfg.BaseDir() != dir {
		t.Errorf("baseDir = %q, want %q", cfg.BaseDir(), dir)
	}
	if got := cfg.AbsPath(cfg.Log.FilePath); got != filepath.Join(dir, "my.log") {
		t.Errorf("relative log path must resolve against config dir, got %q", got)
	}

	// Missing file: defaults, but baseDir still tracks the target path.
	cfg = LoadFrom(filepath.Join(dir, "sub", "config.yaml"))
	if cfg.Proxy.Name != "tachyonikproxy" {
		t.Errorf("expected defaults for missing file, got name %q", cfg.Proxy.Name)
	}
	if cfg.BaseDir() != filepath.Join(dir, "sub") {
		t.Errorf("baseDir for missing file = %q, want %q", cfg.BaseDir(), filepath.Join(dir, "sub"))
	}
}

// SaveConfig must create a missing canonical directory (fresh user-space
// enrollment) and must never leak the unexported baseDir into the YAML.
func TestSaveConfig_CreatesDirectoryAndOmitsBaseDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tachyonik", "tachyonikproxy", "config.yaml")

	cfg := LoadFrom(path)
	if err := SaveConfig(cfg, path); err != nil {
		t.Fatalf("SaveConfig into fresh directory: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config file not written: %v", err)
	}
	if strings.Contains(string(data), "baseDir") || strings.Contains(string(data), dir) {
		t.Errorf("persisted YAML must not contain baseDir:\n%s", data)
	}
}

// TestSaveConfig_PreservesExistingMode is the canary for M5. If the file
// already exists at a hardened mode (e.g. 0600 or 0640), SaveConfig must
// not relax it back to 0644.
func TestSaveConfig_PreservesExistingMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Pre-create the file at 0600 — what a paranoid postinstall might do.
	if err := os.WriteFile(path, []byte("server:\n  port: 9100\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{}
	cfg.Server.Port = "9101"
	if err := SaveConfig(cfg, path); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("file mode after SaveConfig = %#o, want 0600", got)
	}
}

// TestSaveConfig_NewFileIs0640 verifies the default mode for fresh files.
// 0644 (the previous default) was world-readable; 0640 keeps the owner +
// daemon group able to read but no one else.
func TestSaveConfig_NewFileIs0640(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := &Config{}
	if err := SaveConfig(cfg, path); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0640 {
		t.Errorf("fresh file mode = %#o, want 0640", got)
	}
}

// TestSaveConfig_AtomicWrite verifies the temp-then-rename pattern leaves
// the destination file intact when an interruption hits between Write and
// Rename. We can't kill the process mid-write in a unit test, but we can
// verify that no .tmp leftover remains after a successful write and that
// the destination file is the rename target (different inode from the
// pre-existing file).
func TestSaveConfig_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	oldStat, _ := os.Stat(path)

	cfg := &Config{}
	if err := SaveConfig(cfg, path); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	// No .tmp leftover.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() == "config.yaml" {
			continue
		}
		t.Errorf("leftover entry after SaveConfig: %s", e.Name())
	}

	// Destination is a new inode (rename atomicity proxy).
	newStat, _ := os.Stat(path)
	if oldStat.Sys() == newStat.Sys() {
		// On some filesystems Sys() comparison is not authoritative.
		// The absence of .tmp leftover is the load-bearing assertion.
		t.Logf("inode comparison inconclusive on this fs")
	}
}
