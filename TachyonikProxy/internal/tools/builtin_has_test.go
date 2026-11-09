// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests TachyonikHAS as a tool: reached like any other through the registry, with
// its arguments checked, and answering with the document the platform's analysis
// and import rules read.

package tools

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"tachyonik/tachyonikproxy/internal/config"
)

// The rule as the catalogue has it.
func hasTool() config.ToolConfig {
	return config.ToolConfig{
		Name:         "tachyonikhas",
		Command:      builtinHASScan,
		AllowedChars: `a-zA-Z0-9.:/,\- `,
		Timeout:      30,
		ArgsSchema:   `{"type":"object","properties":{"target":{"type":"string"}},"required":["target"]}`,
	}
}

func callHAS(t *testing.T, target string) *ToolResult {
	t.Helper()
	reg := NewRegistry([]config.ToolConfig{hasTool()}, nil)
	res, err := reg.CallTool("tachyonikhas", map[string]interface{}{"target": target}, Secrets{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestHASRefusesWhatItMayNotScan(t *testing.T) {
	for target, want := range map[string]string{
		"8.8.8.8":        "not a private",
		"10.0.0.0/8":     "wider than",
		"example.org":    "hostnames",
		"192.168.1.1;ls": "disallowed", // the rule's character allowlist, before the built-in
	} {
		res := callHAS(t, target)
		if !res.IsError || !strings.Contains(res.Content, want) {
			t.Errorf("%q: result = %+v, want an error saying %q", target, res, want)
		}
		if len(res.OutputFiles) != 0 {
			t.Errorf("%q: a refused scan produced a file", target)
		}
	}
}

func TestHASRequiresATarget(t *testing.T) {
	reg := NewRegistry([]config.ToolConfig{hasTool()}, nil)
	res, err := reg.CallTool("tachyonikhas", map[string]interface{}{}, Secrets{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Errorf("result = %+v, want an error", res)
	}
}

// The whole way, on loopback: 127.0.0.1 answers one way or another (its echo, or
// a refused connection on a closed port).
func TestHASFindsAHostAndReportsItAsAFile(t *testing.T) {
	res := callHAS(t, "127.0.0.1")
	if res.IsError {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.Content, "1 alive host among 1 address") {
		t.Errorf("summary = %q", res.Content)
	}
	if len(res.OutputFiles) != 1 {
		t.Fatalf("files = %d", len(res.OutputFiles))
	}
	f := res.OutputFiles[0]
	if f.MimeType != "application/json" || !strings.HasPrefix(f.Filename, "tachyonikhas-hosts-") {
		t.Errorf("file = %s %s", f.Filename, f.MimeType)
	}
	raw, err := base64.StdEncoding.DecodeString(f.Data)
	if err != nil {
		t.Fatal(err)
	}

	// Field names the import rule is written against. Renaming one is a change
	// to schemaVersion and to that rule, so it should fail here first.
	var doc struct {
		Tool            string   `json:"tool"`
		SchemaVersion   int      `json:"schemaVersion"`
		Target          string   `json:"target"`
		AddressesProbed int      `json:"addressesProbed"`
		Complete        *bool    `json:"complete"`
		Warnings        []string `json:"warnings"`
		Hosts           []struct {
			IP         string   `json:"ip"`
			DetectedBy []string `json:"detectedBy"`
		} `json:"hosts"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%v in %s", err, raw)
	}
	if doc.Tool != "TachyonikHAS" || doc.SchemaVersion != 1 || doc.Target != "127.0.0.1" || doc.AddressesProbed != 1 {
		t.Errorf("document = %s", raw)
	}
	if doc.Complete == nil || !*doc.Complete || doc.Warnings == nil {
		t.Errorf("complete and warnings must always be present: %s", raw)
	}
	if len(doc.Hosts) != 1 || doc.Hosts[0].IP != "127.0.0.1" || len(doc.Hosts[0].DetectedBy) == 0 {
		t.Errorf("hosts = %+v", doc.Hosts)
	}
}

func TestHASRunsOneScanAtATime(t *testing.T) {
	hasRunning.Lock()
	defer hasRunning.Unlock()
	res := callHAS(t, "127.0.0.1")
	if !res.IsError || !strings.Contains(res.Content, "another TachyonikHAS scan") {
		t.Errorf("result = %+v", res)
	}
}

func TestHASFilename(t *testing.T) {
	got := hasFilename(time.Date(2026, 11, 9, 14, 5, 6, 0, time.UTC))
	if got != "tachyonikhas-hosts-20261109T140506Z.json" {
		t.Errorf("filename = %q", got)
	}
}

func TestBuiltinNamesListTheScan(t *testing.T) {
	names := BuiltinNames()
	found := false
	for _, n := range names {
		found = found || n == "has-scan"
		if strings.HasPrefix(n, BuiltinPrefix) {
			t.Errorf("%q keeps its prefix", n)
		}
	}
	if !found {
		t.Errorf("names = %v", names)
	}
}
