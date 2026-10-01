// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// End-to-end checks of the GMP built-ins against a real appliance, skipped
// unless one is named. The flow under test is the one a user goes through:
// test with no key confirmed, confirm the key the appliance offered, test
// again, then fetch the hosts.
//
//	GMP_LIVE_HOST=192.168.178.162 GMP_LIVE_USER=demo GMP_LIVE_PASSWORD=… \
//	go test ./internal/tools/ -run Live -v

package tools

import (
	"encoding/json"
	"os"
	"testing"

	"tachyonik/tachyonikproxy/internal/config"
)

func TestLiveGMPFlow(t *testing.T) {
	host := os.Getenv("GMP_LIVE_HOST")
	if host == "" {
		t.Skip("set GMP_LIVE_HOST to run this against a real appliance")
	}
	args := map[string]interface{}{"host": host}
	secrets := Secrets{Login: os.Getenv("GMP_LIVE_USER"), Password: os.Getenv("GMP_LIVE_PASSWORD")}

	// 1. No key confirmed: the fingerprint comes back, the password does not go out.
	first := TestGMPCredential(args, secrets)
	t.Logf("1st test: %+v", first)
	if first.OK || !first.HostKeyUnconfirmed || first.HostKeyFingerprint == "" {
		t.Fatalf("expected an unconfirmed key with its fingerprint, got %+v", first)
	}

	// 2. Confirmed: the credential is tested.
	secrets.HostKey = first.HostKeyFingerprint
	second := TestGMPCredential(args, secrets)
	t.Logf("2nd test: %+v", second)
	if !second.OK {
		t.Fatalf("credential test failed after confirming the key: %+v", second)
	}

	// 3. A wrong GMP password is told apart from everything else.
	bad := TestGMPCredential(args, Secrets{Login: secrets.Login, Password: "definitely-wrong", HostKey: secrets.HostKey})
	t.Logf("wrong password: %+v", bad)
	if bad.OK || !bad.AuthFailed {
		t.Errorf("a refused password was not reported as an auth failure: %+v", bad)
	}

	// 4. A pinned key that does not match stops the connection.
	other := TestGMPCredential(args, Secrets{Login: secrets.Login, Password: secrets.Password, HostKey: "SHA256:not-this-appliance"})
	t.Logf("wrong key: %+v", other)
	if other.OK || !other.HostKeyMismatch {
		t.Errorf("a mismatched host key was not refused: %+v", other)
	}

	// 5. The tool itself, through the same dispatch a tools/call takes.
	reg := NewRegistry([]config.ToolConfig{{
		Name: "openvas get host assets", Command: builtinGMPGetHosts, Timeout: 60, MaxOutputBytes: 2 << 20,
	}}, nil)
	res, err := reg.CallTool("openvas get host assets", args, secrets)
	if err != nil || res.IsError {
		t.Fatalf("gmp-get-hosts: err=%v result=%+v", err, res)
	}
	var out struct {
		Count int `json:"count"`
		Hosts []struct {
			IP, Hostname, OS string
		} `json:"hosts"`
	}
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	t.Logf("gmp-get-hosts: %d hosts", out.Count)
	if out.Count == 0 || len(out.Hosts) != out.Count {
		t.Errorf("count %d does not match %d hosts", out.Count, len(out.Hosts))
	}
}
