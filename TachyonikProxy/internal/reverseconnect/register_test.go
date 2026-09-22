// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the shape of the register frame — the one message an inbound proxy
// sends about itself, and now the only channel by which the platform learns
// its address after enrollment.

package reverseconnect

import (
	"encoding/json"
	"testing"
)

// The frame must carry the address under the name ToolManager reads, and must
// omit it rather than send an empty string when the host has none. The two ends
// are separate structs in separate modules, so the wire names are the contract:
// a rename on one side alone is silent, and the symptom is an IP column that
// stays empty for no visible reason.
func TestRegisterMessageCarriesIPAddress(t *testing.T) {
	raw, err := json.Marshal(registerMessage{
		Type:      "register",
		ProxyName: "Branch Office",
		IPAddress: "192.168.178.54",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["type"] != "register" || got["proxyName"] != "Branch Office" {
		t.Errorf("register frame = %s, want type/proxyName unchanged", raw)
	}
	if got["ipAddress"] != "192.168.178.54" {
		t.Errorf("ipAddress = %v, want 192.168.178.54 (ToolManager reads this key)", got["ipAddress"])
	}
}

// A host that cannot work out its own address sends no key at all, which an
// older ToolManager and the current one treat the same way: leave the stored
// address alone.
func TestRegisterMessageOmitsEmptyIPAddress(t *testing.T) {
	raw, err := json.Marshal(registerMessage{Type: "register", ProxyName: "Branch Office"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := got["ipAddress"]; present {
		t.Errorf("register frame = %s, want no ipAddress key when there is no address", raw)
	}
}
