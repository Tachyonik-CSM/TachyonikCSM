// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the hd-scan subcommand: its flags, that it refuses what the built-in
// refuses, and that a real scan of this machine is printed as a table and as
// the tool's own document.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"tachyonik/tachyonikproxy/internal/config"
	"tachyonik/tachyonikproxy/internal/tools"
)

// Without --target the command scans the network the netscan sweep uses, and a
// configured netscan.network decides it. Nothing is scanned here.
func TestOwnNetworkFollowsTheConfiguredSweep(t *testing.T) {
	cfg := &config.Config{}
	cfg.NetScan.Network = "10.1.2.0/24"
	if got, err := ownNetwork(cfg); err != nil || got != "10.1.2.0/24" {
		t.Errorf("ownNetwork = %q, %v; want 10.1.2.0/24", got, err)
	}
	cfg.NetScan.Network = "8.8.8.0/24"
	if _, err := ownNetwork(cfg); err == nil {
		t.Error("a public netscan.network must be refused")
	}
}

func TestParseHDScanArgs(t *testing.T) {
	opts, err := parseHDScanArgs([]string{"hd-scan"})
	if err != nil || opts.target != "" || opts.timeout != hdScanDefaultTimeout || opts.json {
		t.Fatalf("defaults = %+v, %v", opts, err)
	}

	// Flags on either side of the subcommand, in both forms.
	opts, err = parseHDScanArgs([]string{"--json", "hd-scan", "--target", "192.168.1.0/24", "--timeout=60"})
	if err != nil || opts.target != "192.168.1.0/24" || opts.timeout != time.Minute || !opts.json {
		t.Fatalf("got %+v, %v", opts, err)
	}

	for _, args := range [][]string{
		{"hd-scan", "--target"},
		{"hd-scan", "--target", "--json"},
		{"hd-scan", "--timeout", "0"},
		{"hd-scan", "--timeout", "-5"},
		{"hd-scan", "--timeout", "ten"},
	} {
		if _, err := parseHDScanArgs(args); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
}

// The command scans with the tool's code, so it refuses the same targets.
func TestHDScanRefusesWhatTheToolRefuses(t *testing.T) {
	for target, want := range map[string]string{
		"8.8.8.8":     "not a private",
		"10.0.0.0/8":  "wider than",
		"example.org": "hostnames",
	} {
		if _, err := tools.RunHDScan(context.Background(), target); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want one saying %q", target, err, want)
		}
	}
}

func TestHDScanOfThisMachine(t *testing.T) {
	scan, err := tools.RunHDScan(context.Background(), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Hosts) != 1 || scan.Hosts[0].IP != "127.0.0.1" {
		t.Fatalf("hosts = %+v, want 127.0.0.1", scan.Hosts)
	}

	var out bytes.Buffer
	printHDScan(&out, scan)
	text := out.String()
	for _, want := range []string{"Target 127.0.0.1", "1 alive among 1 address(es)", "HOST", "DETECTED BY", "127.0.0.1"} {
		if !strings.Contains(text, want) {
			t.Errorf("table lacks %q:\n%s", want, text)
		}
	}

	// --json is the tool's document, which the analysis rule recognises.
	doc, err := scan.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(doc, &got); err != nil {
		t.Fatal(err)
	}
	if got["tool"] != "TachyonikHD" || got["schemaVersion"] != float64(1) || got["target"] != "127.0.0.1" {
		t.Errorf("document = %s", doc)
	}
}

func TestPrintHDScanMarksAnIncompleteScan(t *testing.T) {
	var out bytes.Buffer
	printHDScan(&out, tools.HDScan{HDDocument: tools.HDDocument{Target: "10.0.0.0/24", AddressesProbed: 254}})
	if !strings.Contains(out.String(), "incomplete") || !strings.Contains(out.String(), "No host answered.") {
		t.Errorf("output = %q", out.String())
	}
}
