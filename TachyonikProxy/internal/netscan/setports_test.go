// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests reconfiguring the sweep's port list at runtime — how a port list
// configured centrally in TachyonikCSM reaches a proxy that is already
// running, without restarting a machine in someone else's network.

package netscan

import (
	"reflect"
	"testing"
)

func newTestScanner(t *testing.T) *Scanner {
	t.Helper()
	s, err := New(Config{Network: "192.168.178.0/24"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestSetPorts(t *testing.T) {
	s := newTestScanner(t)
	if got := s.Ports(); !reflect.DeepEqual(got, DefaultPorts) {
		t.Fatalf("a fresh scanner sweeps %v, want the shipped default %v", got, DefaultPorts)
	}

	s.SetPorts([]int{443, 8443, 9392})
	if got := s.Ports(); !reflect.DeepEqual(got, []int{443, 8443, 9392}) {
		t.Errorf("Ports() = %v after SetPorts, want 443 8443 9392", got)
	}
}

// "Sweep nothing" is not a state any caller means, and accepting it would
// quietly turn the sweep off while still reporting itself as configured.
func TestSetPortsIgnoresEmpty(t *testing.T) {
	s := newTestScanner(t)
	s.SetPorts([]int{8443})

	s.SetPorts(nil)
	s.SetPorts([]int{})

	if got := s.Ports(); !reflect.DeepEqual(got, []int{8443}) {
		t.Errorf("Ports() = %v, want the previous list kept", got)
	}
}

// The caller's slice must not stay aliased to the scanner's: a caller that
// reuses its buffer would otherwise retarget a running sweep by accident.
func TestSetPortsCopies(t *testing.T) {
	s := newTestScanner(t)
	ports := []int{443, 8443}
	s.SetPorts(ports)

	ports[0] = 22
	if got := s.Ports(); got[0] != 443 {
		t.Errorf("Ports()[0] = %d after the caller mutated its slice, want 443", got[0])
	}

	// And the other way: what Ports() hands back is a copy too.
	out := s.Ports()
	out[0] = 22
	if again := s.Ports(); again[0] != 443 {
		t.Errorf("Ports()[0] = %d after a caller mutated the returned slice, want 443", again[0])
	}
}

// The port list feeds the target list, which is what a sweep actually probes:
// hosts x ports. This is the cost model the cap in TachyonikCSM exists for.
func TestTargetsFollowPorts(t *testing.T) {
	s := newTestScanner(t)

	oneProbe := len(s.targets())
	if oneProbe == 0 {
		t.Fatal("a /24 produced no targets")
	}

	s.SetPorts([]int{443, 8443, 9392})
	if got, want := len(s.targets()), oneProbe*3; got != want {
		t.Errorf("three ports produced %d targets, want %d (hosts x ports)", got, want)
	}
}

// A nil scanner is the "netscan disabled" path — held behind an interface that
// may carry a nil pointer, so a push must not panic on an installation that
// never enabled the sweep.
func TestSetPortsNilReceiver(t *testing.T) {
	var s *Scanner
	s.SetPorts([]int{443})
}
