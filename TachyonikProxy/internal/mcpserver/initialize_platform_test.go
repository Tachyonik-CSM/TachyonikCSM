// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that the initialize reply carries where and how the proxy is
// installed, beside the MCP fields, and leaves the block out until it is set.

package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"tachyonik/tachyonikproxy/internal/platforminfo"
)

func initialize(t *testing.T, s *Server) map[string]interface{} {
	t.Helper()
	resp := s.HandleRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInitializeReportsThePlatform(t *testing.T) {
	s, _ := newTestServer(t, nil)
	s.SetPlatform(platforminfo.Platform{OS: "linux", Arch: "arm64", OSName: "Debian GNU/Linux 13 (trixie)",
		InstallMethod: platforminfo.MethodDeb})

	out := initialize(t, s)
	raw, _ := json.Marshal(out["tachyonik"])
	for _, want := range []string{`"os":"linux"`, `"arch":"arm64"`, `"installMethod":"deb"`, `"osName":"Debian GNU/Linux 13 (trixie)"`, `"autoUpdate":false`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("tachyonik block %s lacks %s", raw, want)
		}
	}
	if info, _ := out["serverInfo"].(map[string]interface{}); info["version"] != "test" {
		t.Errorf("serverInfo = %v", out["serverInfo"])
	}
}

func TestInitializeOmitsAnUnsetPlatform(t *testing.T) {
	s, _ := newTestServer(t, nil)
	if _, ok := initialize(t, s)["tachyonik"]; ok {
		t.Error("the tachyonik block is sent before the platform is known")
	}
}
