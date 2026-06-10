// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package enroll

import (
	"os"
	"path/filepath"
	"testing"

	"tachyonik/tachyonikproxy/internal/config"
)

// A fresh enrollment must store cert paths relative to the config file's
// directory (so the bundle is relocatable) and point the log at the
// canonical user-space location instead of the CWD-relative default.
func TestPersistEnrollment_RelativePathsAndLogDefault(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("log-default assertion assumes non-root")
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	certDir := filepath.Join(dir, "certs")

	cfg := config.LoadFrom(configPath) // file doesn't exist yet → fresh enroll
	resp := &Response{
		ProxyName:      "My Proxy",
		ConnectionMode: "outbound",
		CACert:         "CA PEM",
		ServerCert:     "CERT PEM",
		ServerKey:      "KEY PEM",
	}

	res, err := PersistEnrollment(cfg, resp, certDir, configPath, "csm.example.com")
	if err != nil {
		t.Fatalf("PersistEnrollment: %v", err)
	}
	if res.ConnectionMode != "outbound" {
		t.Errorf("connection mode = %q, want outbound", res.ConnectionMode)
	}

	// Stored paths are config-dir-relative…
	if cfg.TLS.CACert != filepath.Join("certs", "ca.crt") {
		t.Errorf("ca_cert stored as %q, want certs/ca.crt", cfg.TLS.CACert)
	}
	if cfg.TLS.ServerCert != filepath.Join("certs", "server.crt") {
		t.Errorf("server_cert stored as %q, want certs/server.crt", cfg.TLS.ServerCert)
	}
	// …and resolve back to the real files via the config's baseDir.
	for _, p := range []string{cfg.TLS.CACert, cfg.TLS.ServerCert, cfg.TLS.ServerKey} {
		if _, err := os.Stat(cfg.AbsPath(p)); err != nil {
			t.Errorf("resolved cert path %q not on disk: %v", cfg.AbsPath(p), err)
		}
	}

	// Fresh config: log goes to the canonical user-space location.
	if want := config.UserLogPath(); want != "" && cfg.Log.FilePath != want {
		t.Errorf("fresh-enroll log path = %q, want %q", cfg.Log.FilePath, want)
	}

	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config not written: %v", err)
	}
}

// An explicit --cert-dir outside the config directory is stored absolute,
// and an existing config keeps its operator-chosen log path.
func TestPersistEnrollment_ExternalCertDirAndExistingLogKept(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("log:\n  file_path: ./tachyonikproxy.log\n"), 0640); err != nil {
		t.Fatal(err)
	}
	externalCerts := filepath.Join(t.TempDir(), "elsewhere")

	cfg := config.LoadFrom(configPath)
	resp := &Response{
		ProxyName:      "My Proxy",
		ConnectionMode: "inbound",
		CACert:         "CA PEM",
		ClientCert:     "CLIENT PEM",
		ClientKey:      "KEY PEM",
	}

	if _, err := PersistEnrollment(cfg, resp, externalCerts, configPath, "csm.example.com"); err != nil {
		t.Fatalf("PersistEnrollment: %v", err)
	}

	if !filepath.IsAbs(cfg.ReverseConnect.ClientCert) {
		t.Errorf("cert outside config dir must be stored absolute, got %q", cfg.ReverseConnect.ClientCert)
	}
	// Existing config: the operator's log path is preserved verbatim.
	if cfg.Log.FilePath != "./tachyonikproxy.log" {
		t.Errorf("existing log path must be kept, got %q", cfg.Log.FilePath)
	}
}
