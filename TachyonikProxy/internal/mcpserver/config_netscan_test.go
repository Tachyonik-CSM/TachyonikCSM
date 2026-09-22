// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the netscan half of a remote config/update: what a pushed port list is
// allowed to be, and that a push carrying no port list leaves the local one
// alone.
//
// That last part is the one worth guarding. config/update arrives every few
// minutes carrying tool configuration; if an absent port list were read as
// "clear it", every sync would quietly wipe a proxy's local netscan setting.

package mcpserver

import (
	"encoding/json"
	"reflect"
	"testing"

	"tachyonik/tachyonikproxy/internal/config"
	"tachyonik/tachyonikproxy/internal/netscan"
	"tachyonik/tachyonikproxy/internal/tools"
	"tachyonik/tachyonikproxy/internal/toolscan"
)

// recordingSetter stands in for the running sweep.
type recordingSetter struct {
	got  []int
	call int
}

func (r *recordingSetter) Snapshot() netscan.Snapshot { return netscan.Snapshot{} }
func (r *recordingSetter) SetPorts(ports []int) {
	r.got = append([]int(nil), ports...)
	r.call++
}

func newTestServer(t *testing.T, netScan toolscan.NetScanProvider) (*Server, *config.Config) {
	t.Helper()
	cfg := &config.Config{AllowRemoteConfig: true}
	cfg.NetScan.Ports = []int{443}
	registry := tools.NewRegistry(nil, nil)
	s := NewServer(cfg, registry, "test", netScan)
	return s, cfg
}

func update(t *testing.T, s *Server, params string) *Response {
	t.Helper()
	raw := []byte(`{"jsonrpc":"2.0","id":1,"method":"config/update","params":` + params + `}`)
	return s.HandleRequest(raw)
}

func TestConfigUpdateSetsNetScanPorts(t *testing.T) {
	setter := &recordingSetter{}
	s, cfg := newTestServer(t, setter)

	resp := update(t, s, `{"netScanPorts":[443,8443,9392]}`)
	if resp.Error != nil {
		t.Fatalf("push refused: %+v", resp.Error)
	}
	if !reflect.DeepEqual(setter.got, []int{443, 8443, 9392}) {
		t.Errorf("sweep was set to %v, want 443 8443 9392", setter.got)
	}
	if !reflect.DeepEqual(cfg.NetScan.Ports, []int{443, 8443, 9392}) {
		t.Errorf("config holds %v, want the pushed list persisted", cfg.NetScan.Ports)
	}
}

// The regression this file exists for.
func TestConfigUpdateWithoutPortsLeavesThemAlone(t *testing.T) {
	setter := &recordingSetter{}
	s, cfg := newTestServer(t, setter)

	resp := update(t, s, `{"tools":[]}`)
	if resp.Error != nil {
		t.Fatalf("push refused: %+v", resp.Error)
	}
	if setter.call != 0 {
		t.Errorf("the sweep was reconfigured by a push that named no ports (called %d times)", setter.call)
	}
	if !reflect.DeepEqual(cfg.NetScan.Ports, []int{443}) {
		t.Errorf("config holds %v, want the local 443 untouched", cfg.NetScan.Ports)
	}
}

func TestConfigUpdateRejectsBadPorts(t *testing.T) {
	cases := []struct {
		name   string
		params string
	}{
		{"zero", `{"netScanPorts":[0]}`},
		{"too high", `{"netScanPorts":[65536]}`},
		{"negative", `{"netScanPorts":[-1]}`},
		{"duplicate", `{"netScanPorts":[443,443]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setter := &recordingSetter{}
			s, cfg := newTestServer(t, setter)

			resp := update(t, s, c.params)
			if resp.Error == nil {
				t.Fatalf("%s was accepted, want a refusal", c.params)
			}
			if setter.call != 0 {
				t.Error("the sweep was reconfigured despite the refusal")
			}
			if !reflect.DeepEqual(cfg.NetScan.Ports, []int{443}) {
				t.Errorf("config holds %v, want it untouched after a refusal", cfg.NetScan.Ports)
			}
		})
	}
}

// A proxy with no sweep running still records what it was told, so a restart
// picks it up. It must not fail the push for want of something to retarget.
func TestConfigUpdateWithoutRunningSweep(t *testing.T) {
	s, cfg := newTestServer(t, nil)

	resp := update(t, s, `{"netScanPorts":[8443]}`)
	if resp.Error != nil {
		t.Fatalf("push refused with no sweep running: %+v", resp.Error)
	}
	if !reflect.DeepEqual(cfg.NetScan.Ports, []int{8443}) {
		t.Errorf("config holds %v, want the pushed list stored anyway", cfg.NetScan.Ports)
	}
}

// config/get reports what the sweep is set to, so an operator can read back
// what a proxy is actually doing.
func TestConfigGetReportsNetScanPorts(t *testing.T) {
	s, _ := newTestServer(t, &recordingSetter{})

	resp := s.HandleRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"config/get"}`))
	if resp.Error != nil {
		t.Fatalf("config/get failed: %+v", resp.Error)
	}
	encoded, _ := json.Marshal(resp.Result)
	var got ConfigGetResult
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got.NetScanPorts, []int{443}) {
		t.Errorf("config/get reported ports %v, want 443", got.NetScanPorts)
	}
}
