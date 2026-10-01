// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that a pushed tool below the security floor is refused on its own.
//
// It used to refuse the whole push. One rule without allowed_chars then kept
// every other tool off the proxy, and each of them failed with "tool not
// found" — a message that pointed nowhere near the rule actually at fault.

package mcpserver

import (
	"encoding/json"
	"testing"
)

func TestOneFaultyToolDoesNotBlockTheOthers(t *testing.T) {
	s, cfg := newTestServer(t, nil)

	resp := update(t, s, `{"tools":[
		{"name":"nmap quick scan","command":"nmap","allowedChars":"a-zA-Z0-9.:/\\-_, "},
		{"name":"openvas get host assets","command":"builtin:gmp-get-hosts"},
		{"name":"broken pattern","command":"x","allowedChars":"a-z\\"}
	]}`)
	if resp.Error != nil {
		t.Fatalf("the push was refused whole: %+v", resp.Error)
	}

	raw, _ := json.Marshal(resp.Result)
	var result ConfigUpdateResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	refused := map[string]string{}
	for _, r := range result.RejectedTools {
		refused[r.Name] = r.Reason
	}
	if len(refused) != 2 || refused["openvas get host assets"] == "" || refused["broken pattern"] == "" {
		t.Errorf("refused %v, want exactly the two faulty tools named with a reason", refused)
	}

	listed := s.registry.ListTools()
	if len(listed) != 1 {
		t.Fatalf("registry holds %d tools, want only the valid one", len(listed))
	}
	if len(cfg.Tools) != 1 || cfg.Tools[0].Name != "nmap quick scan" {
		t.Errorf("config holds %+v, want only the valid tool persisted", cfg.Tools)
	}
}

func TestAPushOfValidToolsRefusesNothing(t *testing.T) {
	s, _ := newTestServer(t, nil)
	resp := update(t, s, `{"tools":[{"name":"t","command":"c","allowedChars":"a-z"}]}`)
	if resp.Error != nil {
		t.Fatalf("refused: %+v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	var result ConfigUpdateResult
	_ = json.Unmarshal(raw, &result)
	if result.Status != "updated" || len(result.RejectedTools) != 0 {
		t.Errorf("got %+v, want updated with nothing refused", result)
	}
}
