// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package toolscan

import (
	"strings"
	"testing"
	"time"
)

// TestScanner_RoutineTimeout is the canary for H2. A routine with a tight
// infinite loop must not hang the scanner — goja.Interrupt must fire from
// the deadline timer and surface as an error result.
func TestScanner_RoutineTimeout(t *testing.T) {
	// Shorten the timeout for the test; restore on exit.
	prev := defaultRoutineTimeout
	defaultRoutineTimeout = 200 * time.Millisecond
	defer func() { defaultRoutineTimeout = prev }()

	hostile := `
		var rules = [{
			name: "evil",
			description: "infinite loop",
			detect: function() { while (true) {} }
		}];
	`
	scanner := New()

	start := time.Now()
	results := scanner.Scan([]RoutineInput{{Name: "evil-routine", Code: hostile}})
	elapsed := time.Since(start)

	// Timeout must fire well within an envelope of the constant. We
	// allow up to 5s for safety on very slow CI runners.
	if elapsed > 5*time.Second {
		t.Fatalf("routine ran for %s — Interrupt did not fire", elapsed)
	}

	// Results should contain at least one entry surfacing the timeout.
	if len(results) == 0 {
		t.Fatal("expected at least one result reflecting the interrupted routine")
	}
	foundTimeout := false
	for _, r := range results {
		if r.Error != "" && strings.Contains(strings.ToLower(r.Error), "timed out") {
			foundTimeout = true
		}
	}
	if !foundTimeout {
		t.Errorf("no timeout reflected in results: %+v", results)
	}
}

// TestScanner_StampsToolOverviewID is the canary for the per-row tool_id
// pass-through. The scanner must copy the routine's ToolOverviewID onto
// every emitted ToolResult — including each element when detect() returns
// an array of detections.
func TestScanner_StampsToolOverviewID(t *testing.T) {
	code := `
		var rules = [{
			name: "openvas",
			detect: function() {
				return [
					{ host: "10.0.0.5", version: "22.0" },
					{ host: "10.0.0.6", version: "22.1" }
				];
			}
		}];
	`
	oid := int64(7)
	scanner := New()
	results := scanner.Scan([]RoutineInput{{
		Name:           "openvas",
		Code:           code,
		ToolOverviewID: &oid,
	}})

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for _, r := range results {
		if r.ToolOverviewID == nil || *r.ToolOverviewID != 7 {
			t.Errorf("expected ToolOverviewID=7 stamped on result, got %+v", r.ToolOverviewID)
		}
	}
}

// TestScanner_NilToolOverviewID covers the data-bug case where the
// routine arrives without an overview link. Results carry nil through;
// ResourceManager surfaces them as unmanaged so the operator notices.
func TestScanner_NilToolOverviewID(t *testing.T) {
	code := `
		var rules = [{
			name: "nmap",
			detect: function() { return { version: "7.94" }; }
		}];
	`
	scanner := New()
	results := scanner.Scan([]RoutineInput{{
		Name: "nmap",
		Code: code,
		// ToolOverviewID intentionally nil
	}})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ToolOverviewID != nil {
		t.Errorf("expected nil ToolOverviewID, got %+v", results[0].ToolOverviewID)
	}
}

// TestScanner_DetectArrayShape verifies that detect() returning an array
// of objects produces one ToolResult per element, with the host field
// preserved verbatim. This is the new return shape for HTTP/network
// probes that find the same tool on multiple endpoints.
func TestScanner_DetectArrayShape(t *testing.T) {
	code := `
		var rules = [{
			name: "openvas",
			detect: function() {
				return [
					{ host: "10.0.0.5", version: "22.0" },
					{ host: "10.0.0.6", version: "22.1" }
				];
			}
		}];
	`
	scanner := New()
	results := scanner.Scan([]RoutineInput{{Name: "openvas", Code: code}})

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(results), results)
	}
	got := map[string]string{}
	for _, r := range results {
		if !r.Detected {
			t.Errorf("expected each entry to be Detected=true, got %+v", r)
		}
		got[r.Host] = r.Version
	}
	if got["10.0.0.5"] != "22.0" || got["10.0.0.6"] != "22.1" {
		t.Errorf("expected host→version map {10.0.0.5:22.0, 10.0.0.6:22.1}, got %v", got)
	}
}

// TestScanner_DetectObjectShape_HostAutofilled is the back-compat case:
// existing routines return a single object without a host. The scanner
// should fill in the proxy's primary IPv4 (or "" if unavailable on the
// test host — we just check the shape is preserved).
func TestScanner_DetectObjectShape_HostAutofilled(t *testing.T) {
	code := `
		var rules = [{
			name: "nmap",
			detect: function() { return { version: "7.94", path: "/usr/bin/nmap" }; }
		}];
	`
	scanner := New()
	results := scanner.Scan([]RoutineInput{{Name: "nmap", Code: code}})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if !r.Detected || r.Version != "7.94" || r.Path != "/usr/bin/nmap" {
		t.Errorf("unexpected result: %+v", r)
	}
	// Host is "" or a real IP depending on the runner's network — we
	// don't assert a specific value, just that the field exists.
}

func TestBoundedBuf_HonorsCap(t *testing.T) {
	b := &boundedBuf{cap: 10}
	n, err := b.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Write: n=%d err=%v", n, err)
	}
	// Write past the cap; result is silent truncation, but Write returns
	// the requested length so os/exec doesn't treat it as a short write.
	n, err = b.Write([]byte("worldworld"))
	if err != nil {
		t.Fatalf("Write past cap returned error: %v", err)
	}
	if n != 10 {
		t.Errorf("Write returned n=%d, want 10 (full requested length, not truncated)", n)
	}
	if got := b.String(); len(got) > 10 {
		t.Errorf("boundedBuf grew past cap: len=%d, content=%q", len(got), got)
	}
	if got := b.String(); got != "helloworld" {
		t.Errorf("got %q, want %q", got, "helloworld")
	}
}
