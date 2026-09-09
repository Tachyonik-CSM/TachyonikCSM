// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
	"testing"
)

func TestFlagValue(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		flag      string
		want      string
		wantFound bool
		wantErr   string
	}{
		{name: "separate value", args: []string{"netscan", "--network", "10.0.0.0/24"}, flag: "network", want: "10.0.0.0/24", wantFound: true},
		{name: "equals form", args: []string{"netscan", "--network=10.0.0.0/24"}, flag: "network", want: "10.0.0.0/24", wantFound: true},
		{name: "absent", args: []string{"netscan", "--json"}, flag: "network", wantFound: false},
		// Flags may appear on either side of the subcommand; main() tolerates
		// that ordering, so this must too.
		{name: "before the subcommand", args: []string{"--network", "10.0.0.0/24", "netscan"}, flag: "network", want: "10.0.0.0/24", wantFound: true},
		{name: "alongside other flags", args: []string{"netscan", "--json", "--ports", "443,8443"}, flag: "ports", want: "443,8443", wantFound: true},

		// A missing value must be an error, not a silent misread: without this
		// "netscan --network --json" would sweep a network called "--json".
		{name: "no value at end", args: []string{"netscan", "--network"}, flag: "network", wantFound: true, wantErr: "needs a value"},
		{name: "next arg is a flag", args: []string{"netscan", "--network", "--json"}, flag: "network", wantFound: true, wantErr: "needs a value"},
		{name: "empty equals value", args: []string{"netscan", "--network="}, flag: "network", wantFound: true, wantErr: "needs a value"},

		// A flag whose name merely starts with another's must not match it.
		{name: "prefix collision", args: []string{"netscan", "--networking", "x"}, flag: "network", wantFound: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found, err := flagValue(tt.args, tt.flag)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if found != tt.wantFound {
				t.Fatalf("found = %v, want %v", found, tt.wantFound)
			}
			if got != tt.want {
				t.Fatalf("value = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParsePorts(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []int
		wantErr string
	}{
		{name: "single", in: "443", want: []int{443}},
		{name: "several", in: "443,8443,9392", want: []int{443, 8443, 9392}},
		{name: "sorted and deduplicated", in: "9392,443,443,8443", want: []int{443, 8443, 9392}},
		{name: "whitespace tolerated", in: " 443 , 8443 ", want: []int{443, 8443}},
		{name: "trailing comma ignored", in: "443,", want: []int{443}},

		{name: "not a number", in: "443,https", wantErr: `invalid port "https"`},
		{name: "zero", in: "0", wantErr: "out of range"},
		{name: "above 65535", in: "70000", wantErr: "out of range"},
		{name: "negative", in: "-1", wantErr: "out of range"},
		{name: "empty", in: "", wantErr: "no ports given"},
		{name: "only commas", in: ",,", wantErr: "no ports given"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePorts(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ports = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("ports = %v, want %v", got, tt.want)
				}
			}
		})
	}
}
