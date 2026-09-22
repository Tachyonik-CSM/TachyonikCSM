// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the host's self-description. These run against whatever interfaces the
// machine actually has, so they assert invariants rather than values: a CI
// runner, a laptop on wifi and a proxy in a customer's rack all differ.

package netinfo

import (
	"net"
	"testing"
)

// Loopback is never reported. Every host has one, so reporting it would put an
// address in front of an operator that identifies nothing.
func TestLocalIPsExcludeLoopback(t *testing.T) {
	for _, s := range LocalIPs() {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Errorf("LocalIPs returned %q, which is not an IP", s)
			continue
		}
		if ip.IsLoopback() {
			t.Errorf("LocalIPs returned loopback %q", s)
		}
	}
}

// Whatever comes back must be an address, must be one of the host's own, and
// must prefer IPv4 — the family the local-network sweep and the topology view
// both work in.
func TestPrimaryIPv4(t *testing.T) {
	got := PrimaryIPv4()
	if got == "" {
		t.Skip("host has no non-loopback address; nothing to assert")
	}
	ip := net.ParseIP(got)
	if ip == nil {
		t.Fatalf("PrimaryIPv4 = %q, which is not an IP", got)
	}

	var sawIPv4 bool
	var mine bool
	for _, s := range LocalIPs() {
		if s == got {
			mine = true
		}
		if p := net.ParseIP(s); p != nil && p.To4() != nil {
			sawIPv4 = true
		}
	}
	if !mine {
		t.Errorf("PrimaryIPv4 = %q, which is not among LocalIPs %v", got, LocalIPs())
	}
	if sawIPv4 && ip.To4() == nil {
		t.Errorf("PrimaryIPv4 = %q (IPv6) while the host has an IPv4 address", got)
	}
}
