// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for the JSON binding: that content is sniffed rather than trusted by
// extension, and that unparseable content reaches a routine as null rather than
// failing the import.

package jsruntime

import "testing"

func TestLooksLikeJSON(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"object", `{"a":1}`, true},
		{"array", `[1,2,3]`, true},
		{"leading ws", "   \n  {\"a\":1}", true},
		{"BOM + object", "\uFEFF{\"a\":1}", true},
		{"xml", "<?xml version='1.0'?><r/>", false},
		{"text", "hello", false},
		{"csv", "a,b,c\n1,2,3", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LooksLikeJSON(tc.content); got != tc.want {
				t.Fatalf("LooksLikeJSON(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

const jsonImportJS = `
var importFunction = function(ctx) {
  if (!ctx.json) {
    return { assets: [], vulnerabilities: [], detections: [] };
  }
  var findings = ctx.json.findings || [];
  var vulns = [];
  for (var i = 0; i < findings.length; i++) {
    var f = findings[i];
    vulns.push({
      name: f.name,
      host: f.host,
      port: f.port,
      severity: Math.round(f.cvss * 10),
      lastSeen: f.timestamp
    });
  }
  return { assets: [], vulnerabilities: vulns, detections: [] };
};`

func TestExecute_WithJSONBinding(t *testing.T) {
	const content = `{
  "findings": [
    {"name":"A","host":"10.0.0.1","port":"443/tcp","cvss":7.5,"timestamp":"2026-04-20T00:00:00Z"},
    {"name":"B","host":"10.0.0.2","port":"22/tcp","cvss":4.3,"timestamp":"2026-04-20T00:00:00Z"}
  ]
}`
	exec := New()
	if err := exec.LoadFromString(jsonImportJS); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	r, err := exec.Execute(ImportContext{FileContent: content, FileName: "f.json"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(r.Vulnerabilities) != 2 {
		t.Fatalf("vulnerabilities: got %d, want 2", len(r.Vulnerabilities))
	}
	if r.Vulnerabilities[0].Severity != 75 || r.Vulnerabilities[1].Severity != 43 {
		t.Errorf("severity: got %d/%d, want 75/43", r.Vulnerabilities[0].Severity, r.Vulnerabilities[1].Severity)
	}
	if r.Vulnerabilities[0].Host != "10.0.0.1" {
		t.Errorf("host: got %q, want 10.0.0.1", r.Vulnerabilities[0].Host)
	}
}

func TestExecute_JSONExclusiveWithCSV(t *testing.T) {
	// JSON content must not also set ctx.csv — only one binding per file.
	const js = `
var importFunction = function(ctx) {
  var name = "none";
  if (ctx.json && !ctx.csv && !ctx.xml) name = "json-only";
  else if (ctx.csv && !ctx.json) name = "csv-also";
  return { assets: [{ name: name, type: "Host", lastSeen: "" }], vulnerabilities: [], detections: [] };
};`
	exec := New()
	if err := exec.LoadFromString(js); err != nil {
		t.Fatal(err)
	}
	r, err := exec.Execute(ImportContext{FileContent: `{"a":1}`, FileName: "x.json"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Assets[0].Name != "json-only" {
		t.Fatalf("expected json-only, got %q", r.Assets[0].Name)
	}
}
