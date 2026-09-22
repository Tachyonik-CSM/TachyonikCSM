// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests sweeping several networks: which ranges may be added, how the
// selection is changed at runtime, and what happens when nothing is selected.

package netscan

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func boolPtr(v bool) *bool { return &v }

// An added network faces every rule the default one does, and a tighter size
// limit on top.
func TestResolveExtraNetwork(t *testing.T) {
	cases := []struct {
		name    string
		cidr    string
		want    string
		wantErr string
	}{
		{name: "a /24", cidr: "10.0.5.0/24", want: "10.0.5.0/24"},
		{name: "host address is masked", cidr: "10.0.5.42/24", want: "10.0.5.0/24"},
		{name: "smaller than /24 is fine", cidr: "192.168.1.0/28", want: "192.168.1.0/28"},
		{name: "a /23 is too large", cidr: "10.0.4.0/23", wantErr: "larger than /24"},
		{name: "a /22 is too large", cidr: "10.0.4.0/22", wantErr: "larger than /24"},
		{name: "public refused", cidr: "8.8.8.0/24", wantErr: "outside the sweepable ranges"},
		{name: "ipv6 refused", cidr: "fd00::/120", wantErr: "not IPv4"},
		{name: "malformed", cidr: "not-a-cidr", wantErr: "invalid"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveExtraNetwork(c.cidr)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("resolveExtraNetwork(%q) = %v, want a refusal", c.cidr, got)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Errorf("error = %q, want it to mention %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveExtraNetwork(%q): %v", c.cidr, err)
			}
			if got.String() != c.want {
				t.Errorf("got %s, want %s", got, c.want)
			}
		})
	}
}

// The default network comes first, then the added ones in the order given.
func TestNetworksOrderAndContent(t *testing.T) {
	s, err := New(Config{
		Network:       "192.168.178.0/24",
		ExtraNetworks: []string{"10.0.5.0/24", "172.16.9.0/25"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := []string{"192.168.178.0/24", "10.0.5.0/24", "172.16.9.0/25"}
	if got := s.Networks(); !reflect.DeepEqual(got, want) {
		t.Errorf("Networks() = %v, want %v", got, want)
	}
	// Network() is the proxy's own, regardless of what was added.
	if got := s.Network(); got != "192.168.178.0/24" {
		t.Errorf("Network() = %q, want the default network", got)
	}
}

// One unusable range must not take the others with it — least of all the
// proxy's own network. A pushed typo would otherwise switch scanning off.
func TestBadExtraNetworkIsSkippedNotFatal(t *testing.T) {
	s, err := New(Config{
		Network:       "192.168.178.0/24",
		ExtraNetworks: []string{"10.0.4.0/22", "10.0.5.0/24", "8.8.8.0/24", "nonsense"},
	})
	if err != nil {
		t.Fatalf("New refused the whole config over a bad added network: %v", err)
	}
	want := []string{"192.168.178.0/24", "10.0.5.0/24"}
	if got := s.Networks(); !reflect.DeepEqual(got, want) {
		t.Errorf("Networks() = %v, want %v", got, want)
	}
}

// Disabling the proxy's own network leaves the added ones swept. It can be
// switched off but never removed: Network() still reports it.
func TestDefaultNetworkCanBeDisabled(t *testing.T) {
	s, err := New(Config{
		Network:               "192.168.178.0/24",
		DefaultNetworkEnabled: boolPtr(false),
		ExtraNetworks:         []string{"10.0.5.0/24"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := s.Networks(), []string{"10.0.5.0/24"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Networks() = %v, want %v", got, want)
	}
	if got := s.Network(); got != "192.168.178.0/24" {
		t.Errorf("Network() = %q — the default is disabled, not forgotten", got)
	}
}

// An absent flag means enabled: a config written before the flag existed must
// not be read as "stop sweeping".
func TestDefaultNetworkEnabledWhenUnset(t *testing.T) {
	s, err := New(Config{Network: "192.168.178.0/24"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := s.Networks(), []string{"192.168.178.0/24"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Networks() = %v, want %v", got, want)
	}
}

func TestSetNetworkSelection(t *testing.T) {
	s, err := New(Config{Network: "192.168.178.0/24"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := s.SetNetworkSelection(true, []string{"10.0.5.0/24"}); err != nil {
		t.Fatalf("SetNetworkSelection: %v", err)
	}
	want := []string{"192.168.178.0/24", "10.0.5.0/24"}
	if got := s.Networks(); !reflect.DeepEqual(got, want) {
		t.Errorf("Networks() = %v, want %v", got, want)
	}

	// Clearing the added networks is a thing an operator can ask for.
	if err := s.SetNetworkSelection(true, nil); err != nil {
		t.Fatalf("SetNetworkSelection(nil): %v", err)
	}
	if got, want := s.Networks(), []string{"192.168.178.0/24"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Networks() = %v, want %v", got, want)
	}
}

// A refused selection changes nothing: the sweep keeps running on what it had.
func TestSetNetworkSelectionIsAllOrNothing(t *testing.T) {
	s, err := New(Config{Network: "192.168.178.0/24", ExtraNetworks: []string{"10.0.5.0/24"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	before := s.Networks()

	err = s.SetNetworkSelection(true, []string{"10.0.6.0/24", "8.8.8.0/24"})
	if err == nil {
		t.Fatal("a public range was accepted")
	}
	if got := s.Networks(); !reflect.DeepEqual(got, before) {
		t.Errorf("Networks() = %v after a refused push, want it unchanged (%v)", got, before)
	}
}

// Targets are hosts x networks x ports — the cost model the caps exist for.
func TestTargetsAcrossNetworks(t *testing.T) {
	s, err := New(Config{
		Network:       "192.168.178.0/24",
		ExtraNetworks: []string{"10.0.5.0/24"},
		Ports:         []int{443, 8443},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := len(s.targets()), 254*2*2; got != want {
		t.Errorf("targets = %d, want %d (hosts x networks x ports)", got, want)
	}

	// Every target says which network it came from, so a Host can too.
	seen := map[string]bool{}
	for _, tg := range s.targets() {
		seen[tg.network] = true
	}
	if !seen["192.168.178.0/24"] || !seen["10.0.5.0/24"] {
		t.Errorf("targets carry networks %v, want both ranges", seen)
	}
}

// Nothing selected is a legitimate state, not an error: the sweep idles and
// reports itself not ready, so a routine knows it has no basis to judge.
func TestNothingSelectedPublishesNotReady(t *testing.T) {
	s, err := New(Config{Network: "192.168.178.0/24", DefaultNetworkEnabled: boolPtr(false)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := s.Networks(); len(got) != 0 {
		t.Fatalf("Networks() = %v, want none", got)
	}

	s.ScanOnce(context.Background())
	snap := s.Snapshot()
	if snap.Ready {
		t.Error("snapshot is ready with nothing swept — a routine would read an empty host list as 'not present'")
	}
	if len(snap.Hosts) != 0 {
		t.Errorf("snapshot carries %d host(s) with nothing swept", len(snap.Hosts))
	}
	if snap.Network != "192.168.178.0/24" {
		t.Errorf("snapshot.Network = %q, want the default still reported", snap.Network)
	}
}

// Turning every network off after a sweep must clear the results rather than
// leave the last ones looking current.
func TestDeselectingEverythingClearsStaleHosts(t *testing.T) {
	s, err := New(Config{Network: "192.168.199.0/30", Ports: []int{1}, TimeoutSeconds: 1, Concurrency: 4})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.ScanOnce(context.Background())
	if !s.Snapshot().Ready {
		t.Fatal("first sweep did not complete")
	}

	if err := s.SetNetworkSelection(false, nil); err != nil {
		t.Fatalf("SetNetworkSelection: %v", err)
	}
	s.ScanOnce(context.Background())
	if snap := s.Snapshot(); snap.Ready || len(snap.Hosts) != 0 {
		t.Errorf("snapshot after deselecting: ready=%v hosts=%d, want not ready and empty",
			snap.Ready, len(snap.Hosts))
	}
}

// A nil scanner is the "netscan disabled" path and must stay panic-free.
func TestNilScannerNetworkAccessors(t *testing.T) {
	var s *Scanner
	if got := s.Network(); got != "" {
		t.Errorf("Network() = %q on a nil scanner", got)
	}
	if got := s.Networks(); got != nil && len(got) != 0 {
		t.Errorf("Networks() = %v on a nil scanner", got)
	}
	if err := s.SetNetworkSelection(true, []string{"10.0.5.0/24"}); err != nil {
		t.Errorf("SetNetworkSelection on a nil scanner: %v", err)
	}
}
