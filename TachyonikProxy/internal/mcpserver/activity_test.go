// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests activity/get (docs/plans/proxy-activity.md): it answers the sweep's
// activity as the scanner reports it, and no sweep when there is none.

package mcpserver

import (
	"encoding/json"
	"testing"
	"time"

	"tachyonik/tachyonikproxy/internal/netscan"
)

// activeSweep stands in for a sweep that is running.
type activeSweep struct{ a netscan.Activity }

func (s *activeSweep) Snapshot() netscan.Snapshot { return netscan.Snapshot{Ready: true} }
func (s *activeSweep) Activity() netscan.Activity { return s.a }

func activity(t *testing.T, s *Server) ActivityResult {
	t.Helper()
	resp := s.HandleRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"activity/get"}`))
	if resp.Error != nil {
		t.Fatalf("activity/get failed: %+v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	var out ActivityResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestActivityGetAnswersTheRunningSweep(t *testing.T) {
	started := time.Now().Add(-time.Minute).Truncate(time.Second)
	s, _ := newTestServer(t, &activeSweep{a: netscan.Activity{
		Scanning: true, Networks: []string{"192.168.178.0/24"}, Ports: []int{443}, StartedAt: started, Done: 100, Total: 254,
	}})
	got := activity(t, s).NetworkSweep
	if !got.Scanning || got.Done != 100 || got.Total != 254 || !got.StartedAt.Equal(started) || got.Networks[0] != "192.168.178.0/24" {
		t.Fatalf("activity %+v", got)
	}
}

func TestActivityGetWithoutASweep(t *testing.T) {
	s, _ := newTestServer(t, nil)
	if got := activity(t, s).NetworkSweep; got.Scanning {
		t.Fatalf("activity %+v without a sweep", got)
	}
}
