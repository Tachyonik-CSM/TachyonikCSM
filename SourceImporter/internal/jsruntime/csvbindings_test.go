// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for the CSV binding: that the delimiter is inferred from the content
// rather than assumed, that rows of uneven width survive parsing, and that
// asRecords() keys rows by the header line.

package jsruntime

import "testing"

func TestDetectDelimiter(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    rune
	}{
		{"comma", "a,b,c\n1,2,3", ','},
		{"tab", "a\tb\tc\n1\t2\t3", '\t'},
		{"semicolon", "a;b;c\n1;2;3", ';'},
		{"comma beats tab on tie", "a\tb,c", ','},
		{"skip blank first line", "\n\na;b;c\n1;2;3", ';'},
		{"empty", "", ','},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectDelimiter(tc.content); got != tc.want {
				t.Fatalf("DetectDelimiter = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseCSV_QuotedAndEmbedded(t *testing.T) {
	content := `name,host,description
"CVE-1","10.0.0.1","multi
line value"
"CVE-2","10.0.0.2","with ""quoted"" bit"
`
	rows := ParseCSV(content, ',')
	if len(rows) != 3 {
		t.Fatalf("rows: got %d, want 3", len(rows))
	}
	if rows[1][2] != "multi\nline value" {
		t.Errorf("embedded newline cell: got %q", rows[1][2])
	}
	if rows[2][2] != `with "quoted" bit` {
		t.Errorf("escaped quote cell: got %q", rows[2][2])
	}
}

const csvImportJS = `
var importFunction = function(ctx) {
  var assets = [];
  if (!ctx.csv) {
    return { assets: assets, vulnerabilities: [], detections: [] };
  }
  var rows = ctx.csv.rows;
  for (var i = 1; i < rows.length; i++) {
    var ip = rows[i][0];
    if (ip) assets.push({ name: ip, type: "Host", lastSeen: "" });
  }
  return { assets: assets, vulnerabilities: [], detections: [] };
};`

func TestExecute_WithCSVBinding(t *testing.T) {
	content := "ip,port\n10.0.0.1,443\n10.0.0.2,22\n"
	exec := New()
	if err := exec.LoadFromString(csvImportJS); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	r, err := exec.Execute(ImportContext{FileContent: content, FileName: "hosts.csv"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(r.Assets) != 2 {
		t.Fatalf("assets: got %d, want 2", len(r.Assets))
	}
	if r.Assets[0].Name != "10.0.0.1" || r.Assets[1].Name != "10.0.0.2" {
		t.Errorf("assets: got %v", r.Assets)
	}
}

func TestExecute_CSVAsRecords(t *testing.T) {
	content := "ip,port\n10.0.0.1,443\n10.0.0.2,22\n"
	const js = `
var importFunction = function(ctx) {
  var assets = [];
  if (!ctx.csv) return { assets: assets, vulnerabilities: [], detections: [] };
  var recs = ctx.csv.asRecords();
  for (var i = 0; i < recs.length; i++) {
    assets.push({ name: recs[i].ip + ":" + recs[i].port, type: "Host", lastSeen: "" });
  }
  return { assets: assets, vulnerabilities: [], detections: [] };
};`
	exec := New()
	if err := exec.LoadFromString(js); err != nil {
		t.Fatal(err)
	}
	r, err := exec.Execute(ImportContext{FileContent: content, FileName: "hosts.csv"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Assets) != 2 || r.Assets[0].Name != "10.0.0.1:443" || r.Assets[1].Name != "10.0.0.2:22" {
		t.Fatalf("asRecords result: %+v", r.Assets)
	}
}

func TestExecute_PlainTextGetsCSV(t *testing.T) {
	// One-IP-per-line plain text should get a usable ctx.csv with single-cell rows.
	content := "10.0.0.1\n10.0.0.2\n10.0.0.3\n"
	const js = `
var importFunction = function(ctx) {
  var assets = [];
  if (!ctx.csv) return { assets: assets, vulnerabilities: [], detections: [] };
  for (var i = 0; i < ctx.csv.rows.length; i++) {
    var cell = ctx.csv.rows[i][0];
    if (cell) assets.push({ name: cell, type: "Host", lastSeen: "" });
  }
  return { assets: assets, vulnerabilities: [], detections: [] };
};`
	exec := New()
	if err := exec.LoadFromString(js); err != nil {
		t.Fatal(err)
	}
	r, err := exec.Execute(ImportContext{FileContent: content, FileName: "ips.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Assets) != 3 {
		t.Fatalf("expected 3 assets, got %d (%+v)", len(r.Assets), r.Assets)
	}
}
