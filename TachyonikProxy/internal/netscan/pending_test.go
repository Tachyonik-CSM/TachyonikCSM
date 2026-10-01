// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests Pending: whether the sweep has yet to complete a first pass.
//
// A tools/scan made while it is pending reports itself incomplete, and the
// platform then removes no tools. Getting it wrong in one direction deletes a
// user's tool settings on every proxy restart; in the other, a proxy whose
// sweep can never finish would keep tools it has really lost for good.

package netscan

import (
	"context"
	"testing"
)

func TestPendingUntilTheFirstSweepCompletes(t *testing.T) {
	s, err := New(Config{Network: "192.168.199.0/30", Ports: []int{1}, TimeoutSeconds: 1, Concurrency: 4})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !s.Pending() {
		t.Fatal("a scanner that has not swept yet is not pending")
	}
	s.ScanOnce(context.Background())
	if s.Pending() {
		t.Error("still pending after a completed sweep")
	}
}

// Nothing selected is a sweep that will never complete. Waiting for it would
// mean never removing anything.
func TestNotPendingWhenNothingIsSelected(t *testing.T) {
	s, err := New(Config{Network: "192.168.199.0/30", DefaultNetworkEnabled: boolPtr(false)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s.Pending() {
		t.Error("pending with no network selected")
	}
}

func TestNilScannerIsNotPending(t *testing.T) {
	var s *Scanner
	if s.Pending() {
		t.Error("a nil scanner — netscan disabled — reports pending")
	}
}
