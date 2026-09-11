// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for the XML binding: the content sniffing, the XPath surface a routine
// sees, and the document-node wrapping that makes `//foo` search the whole
// document rather than only below the root element.

package jsruntime

import (
	"os"
	"testing"
)

func TestLooksLikeXML(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"declaration", `<?xml version="1.0"?><root/>`, true},
		{"bom+declaration", "\uFEFF<?xml version=\"1.0\"?><root/>", true},
		{"leading whitespace", "   \n<root/>", true},
		{"comment before root", "<!-- c --><root/>", true},
		{"doctype before root", "<!DOCTYPE html><root/>", true},
		{"plain text", "Hello, world", false},
		{"csv", "ip,port\n1.2.3.4,80", false},
		{"json", `{"a":1}`, false},
		{"bare angle bracket", "< not a tag", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LooksLikeXML(tc.content); got != tc.want {
				t.Fatalf("LooksLikeXML(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

const xmlSample = `<?xml version="1.0"?>
<report>
  <results>
    <result id="1">
      <host>192.168.1.1</host>
      <port>443/tcp</port>
      <name>SQL Injection</name>
      <severity>7.5</severity>
      <modification_time>2025-07-01T00:00:00Z</modification_time>
    </result>
    <result id="2">
      <host>192.168.1.1</host>
      <port>80/tcp</port>
      <name>HTTP Server Detection</name>
      <severity>0.0</severity>
      <modification_time>2025-07-01T00:00:00Z</modification_time>
    </result>
    <result id="3">
      <host>192.168.1.2</host>
      <port>22/tcp</port>
      <name>SSH Weak Algorithms</name>
      <severity>4.3</severity>
      <modification_time>2025-07-01T00:00:00Z</modification_time>
    </result>
  </results>
</report>`

const xmlImportJS = `
var importFunction = function(ctx) {
  var assets = [];
  var vulnerabilities = [];
  var detections = [];
  var seenHosts = {};

  if (!ctx.xml) {
    return { assets: assets, vulnerabilities: vulnerabilities, detections: detections };
  }

  var results = ctx.xml.find("//result");
  for (var i = 0; i < results.length; i++) {
    var r = results[i];
    var host = r.findOne("host").text();
    var port = r.findOne("port").text();
    var name = r.findOne("name").text();
    var sev = Math.round(parseFloat(r.findOne("severity").text()) * 10);
    var lastSeen = r.findOne("modification_time").text();

    if (!seenHosts[host]) {
      seenHosts[host] = true;
      assets.push({ name: host, type: "Host", lastSeen: lastSeen });
    }
    if (sev > 0) {
      vulnerabilities.push({ name: name, host: host, port: port, severity: sev, lastSeen: lastSeen });
    } else {
      detections.push({ name: name, host: host, port: port, lastSeen: lastSeen });
    }
  }

  return { assets: assets, vulnerabilities: vulnerabilities, detections: detections };
};`

func TestExecute_WithXMLBinding(t *testing.T) {
	exec := New()
	if err := exec.LoadFromString(xmlImportJS); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}

	result, err := exec.Execute(ImportContext{
		FileContent: xmlSample,
		FileName:    "report.xml",
		SourceType:  "Test Report",
		SourceRef:   "report.xml (ID: 1)",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(result.Assets) != 2 {
		t.Errorf("assets: got %d, want 2", len(result.Assets))
	}
	if len(result.Vulnerabilities) != 2 {
		t.Errorf("vulnerabilities: got %d, want 2", len(result.Vulnerabilities))
	}
	if len(result.Detections) != 1 {
		t.Errorf("detections: got %d, want 1", len(result.Detections))
	}
	var sqli *VulnerabilityResult
	for i := range result.Vulnerabilities {
		if result.Vulnerabilities[i].Name == "SQL Injection" {
			sqli = &result.Vulnerabilities[i]
			break
		}
	}
	if sqli == nil {
		t.Fatal("did not find SQL Injection vulnerability")
	}
	if sqli.Severity != 75 {
		t.Errorf("severity: got %d, want 75", sqli.Severity)
	}
	if sqli.Port != "443/tcp" {
		t.Errorf("port: got %q, want 443/tcp", sqli.Port)
	}
}

func TestExecute_NonXMLContextHasNullXML(t *testing.T) {
	// Sanity check: text content -> ctx.xml must be null, not throw.
	const js = `
var importFunction = function(ctx) {
  var hasXML = ctx.xml !== null;
  return {
    assets: [{ name: hasXML ? "xml" : "notxml", type: "Host", lastSeen: "" }],
    vulnerabilities: [],
    detections: []
  };
};`
	exec := New()
	if err := exec.LoadFromString(js); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	result, err := exec.Execute(ImportContext{
		FileContent: "ip,port\n1.2.3.4,80\n",
		FileName:    "hosts.csv",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.Assets) != 1 || result.Assets[0].Name != "notxml" {
		t.Fatalf("expected ctx.xml to be null for CSV content, got %+v", result.Assets)
	}
}

// TestExecute_MixedContentText locks down the documented difference between
// .text() (direct text children only) and .innerText() (all descendant text).
// The OpenVAS <host> element has mixed content — an IP followed by sibling
// <asset/> and <hostname>…</hostname> — and a rule author saying
// "use element host" means the IP, not the concatenation.
func TestExecute_MixedContentText(t *testing.T) {
	const js = `
var importFunction = function(ctx) {
  if (!ctx.xml) {
    return { assets: [{ name: "no-xml", type: "Host", lastSeen: "" }], vulnerabilities: [], detections: [] };
  }
  var host = ctx.xml.findOne("//host");
  return {
    assets: [
      { name: host.text(), type: "direct", lastSeen: "" },
      { name: host.innerText(), type: "inner", lastSeen: "" }
    ],
    vulnerabilities: [],
    detections: []
  };
};`
	const xml = `<?xml version="1.0"?><r><host>192.168.1.1<asset asset_id="x"/><hostname>example.host</hostname></host></r>`

	exec := New()
	if err := exec.LoadFromString(js); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	result, err := exec.Execute(ImportContext{FileContent: xml, FileName: "mixed.xml"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.Assets) != 2 {
		t.Fatalf("expected 2 assets, got %d", len(result.Assets))
	}
	if result.Assets[0].Name != "192.168.1.1" {
		t.Errorf(".text() = %q, want %q", result.Assets[0].Name, "192.168.1.1")
	}
	if result.Assets[1].Name != "192.168.1.1example.host" {
		t.Errorf(".innerText() = %q, want %q", result.Assets[1].Name, "192.168.1.1example.host")
	}
}

// TestExecute_XPathUsesDocumentContext verifies that absolute-ish XPath
// expressions like //a/b/c resolve against the document (not the root
// element), which is the intuitive XPath behaviour. Regression for a past
// bug where ctx.xml wrapped the root element and //report/report/... came
// back empty on real OpenVAS files.
func TestExecute_XPathUsesDocumentContext(t *testing.T) {
	const js = `
var importFunction = function(ctx) {
  if (!ctx.xml) {
    return { assets: [], vulnerabilities: [], detections: [] };
  }
  var rs = ctx.xml.find("//outer/inner/results/result");
  var out = [];
  for (var i = 0; i < rs.length; i++) {
    out.push({ name: rs[i].findOne("name").text(), type: "Host", lastSeen: "" });
  }
  return { assets: out, vulnerabilities: [], detections: [] };
};`
	const xml = `<?xml version="1.0"?>
<outer>
  <inner>
    <results>
      <result><name>a</name></result>
      <result><name>b</name></result>
    </results>
  </inner>
</outer>`

	exec := New()
	if err := exec.LoadFromString(js); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	result, err := exec.Execute(ImportContext{FileContent: xml, FileName: "nested.xml"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.Assets) != 2 {
		t.Fatalf("expected 2 assets, got %d", len(result.Assets))
	}
	if result.Assets[0].Name != "a" || result.Assets[1].Name != "b" {
		t.Errorf("names: got %q, %q; want a, b", result.Assets[0].Name, result.Assets[1].Name)
	}
}

// TestExecute_OpenVASSampleRegression is the end-to-end regression guard:
// the actual user-reported routine against the actual user-reported file.
// Skips quietly when the artefacts are not on disk so CI on other machines
// is unaffected.
func TestExecute_OpenVASSampleRegression(t *testing.T) {
	js, err := os.ReadFile("/home/tachyon/sample-routine.js")
	if err != nil {
		t.Skipf("sample routine not available: %v", err)
	}
	xml, err := os.ReadFile("/home/tachyon/SourceExamples/openvas-scan_25.0.3-example-vulnerability-report.xml")
	if err != nil {
		t.Skipf("sample xml not available: %v", err)
	}
	exec := New()
	if err := exec.LoadFromString(string(js)); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	result, err := exec.Execute(ImportContext{
		FileContent: string(xml),
		FileName:    "openvas.xml",
		SourceType:  "OpenVAS Report",
		SourceRef:   "test",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.Assets) == 0 {
		t.Error("expected assets > 0 (OpenVAS file has 22 host elements)")
	}
	total := len(result.Vulnerabilities) + len(result.Detections)
	if total == 0 {
		t.Error("expected vulnerabilities+detections > 0 (OpenVAS file has 46 result elements)")
	}
	for i, v := range result.Vulnerabilities {
		// Hosts should be bare IPs, not the IP+hostname concatenation that
		// InnerText would have produced.
		if len(v.Host) > 0 && (v.Host[0] < '0' || v.Host[0] > '9') {
			t.Errorf("vulnerability[%d] host %q does not start with a digit (mixed-content leak?)", i, v.Host)
			break
		}
	}
}

func TestExecute_EmptyContent(t *testing.T) {
	// Validation path: empty input must not crash and must leave ctx.xml null.
	const js = `
var importFunction = function(ctx) {
  return {
    assets: [{ name: ctx.xml === null ? "nullxml" : "has", type: "Host", lastSeen: "" }],
    vulnerabilities: [],
    detections: []
  };
};`
	exec := New()
	if err := exec.LoadFromString(js); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	result, err := exec.Execute(ImportContext{FileContent: "", FileName: "empty.xml"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.Assets) != 1 || result.Assets[0].Name != "nullxml" {
		t.Fatalf("expected ctx.xml null on empty content, got %+v", result.Assets)
	}
}
