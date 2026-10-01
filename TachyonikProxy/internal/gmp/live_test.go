// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// An end-to-end check against a real appliance, skipped unless one is named.
//
// The fixtures cover the protocol; this covers the half that cannot be faked —
// that the SSH account opens a gvmd channel at all, that an empty command is
// the right way to ask, and that a session survives three commands in a row.
//
//	GMP_LIVE_HOST=192.168.178.162 GMP_LIVE_SSH_USER=gmp GMP_LIVE_SSH_PASSWORD=… \
//	GMP_LIVE_USER=demo GMP_LIVE_PASSWORD=… go test ./internal/gmp/ -run Live -v
//
// GMP_LIVE_SAVE=<path> additionally writes the document received to that file.

package gmp

import (
	"os"
	"testing"
)

func TestLiveAppliance(t *testing.T) {
	host := os.Getenv("GMP_LIVE_HOST")
	if host == "" {
		t.Skip("set GMP_LIVE_HOST to run this against a real appliance")
	}

	client, err := DialSSH(SSHConfig{
		Host:     host,
		User:     os.Getenv("GMP_LIVE_SSH_USER"),
		Password: os.Getenv("GMP_LIVE_SSH_PASSWORD"),
		// The point of the run is the protocol, not the key policy, which the
		// unit tests cover.
		AllowUnknownHostKey: true,
	})
	if err != nil {
		t.Fatalf("DialSSH: %v", err)
	}
	defer client.Close()

	version, err := client.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	t.Logf("GMP version: %s", version)

	if err := client.Authenticate(os.Getenv("GMP_LIVE_USER"), os.Getenv("GMP_LIVE_PASSWORD")); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	assets, err := client.HostAssets()
	if err != nil {
		t.Fatalf("HostAssets: %v", err)
	}
	t.Logf("%d host assets in a %d-byte document", assets.Count, len(assets.Document))
	if assets.Count == 0 {
		t.Error("the appliance returned no host assets")
	}
	if out := os.Getenv("GMP_LIVE_SAVE"); out != "" {
		if err := os.WriteFile(out, assets.Document, 0o600); err != nil {
			t.Errorf("save: %v", err)
		}
	}
}
