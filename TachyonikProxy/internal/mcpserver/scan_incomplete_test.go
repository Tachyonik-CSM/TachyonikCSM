// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that tools/scan says when it could not look everywhere.
//
// A scan made before the network sweep has finished its first pass sees no
// network-detected tools at all. Reported as an ordinary scan, the platform
// takes that as the tools having been uninstalled; the incomplete flag is what
// tells it otherwise.

package mcpserver

import (
	"encoding/json"
	"testing"

	"tachyonik/tachyonikproxy/internal/netscan"
)

// pendingSweep stands in for a sweep that has or has not completed.
type pendingSweep struct{ pending bool }

func (p *pendingSweep) Snapshot() netscan.Snapshot { return netscan.Snapshot{Ready: !p.pending} }
func (p *pendingSweep) Pending() bool              { return p.pending }

func scan(t *testing.T, s *Server) ToolsScanResult {
	t.Helper()
	resp := s.HandleRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/scan","params":{"routines":[]}}`))
	if resp.Error != nil {
		t.Fatalf("tools/scan failed: %+v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	var out ToolsScanResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestScanBeforeTheFirstSweepIsIncomplete(t *testing.T) {
	s, _ := newTestServer(t, &pendingSweep{pending: true})
	got := scan(t, s)
	if !got.Incomplete {
		t.Error("a scan made before the first sweep was reported complete")
	}
	if got.IncompleteReason == "" {
		t.Error("no reason given for the incomplete scan")
	}
}

func TestScanAfterTheFirstSweepIsComplete(t *testing.T) {
	s, _ := newTestServer(t, &pendingSweep{pending: false})
	if got := scan(t, s); got.Incomplete {
		t.Errorf("a scan after the sweep was reported incomplete: %s", got.IncompleteReason)
	}
}

// No sweep configured: nothing to wait for, so nothing is held back.
func TestScanWithoutASweepIsComplete(t *testing.T) {
	s, _ := newTestServer(t, nil)
	if got := scan(t, s); got.Incomplete {
		t.Error("a proxy with no sweep reported its scan incomplete")
	}
}
