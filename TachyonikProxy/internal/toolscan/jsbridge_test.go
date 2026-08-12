// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for the JS globals routines rely on: the netscan view over the last
// local-network sweep, its behaviour when no sweep is available, and the
// httpGet helper the detection prompt has always documented.

package toolscan

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tachyonik/tachyonikproxy/internal/netscan"
)

// stubProvider serves a fixed snapshot.
type stubProvider struct{ snap netscan.Snapshot }

func (s stubProvider) Snapshot() netscan.Snapshot { return s.snap }

func readySnapshot(hosts ...netscan.Host) netscan.Snapshot {
	return netscan.Snapshot{
		Network:  "192.168.178.0/24",
		Ready:    true,
		LastScan: time.Now(),
		Hosts:    hosts,
	}
}

func openvasHost(ip string) netscan.Host {
	return netscan.Host{
		IP: ip, Port: 443, URL: "https://" + ip + ":443/", Status: 200,
		Body:        "<html><head><title>OPENVAS SCAN</title></head></html>",
		Headers:     map[string]string{"server": "gsad"},
		TLSTrusted:  false,
		CertSubject: "CN=openvas",
	}
}

// The rule from the feature request, in its simplified form: no address
// arithmetic, no per-host curl, just a match over the completed sweep.
func TestNetScan_SimplifiedOpenVASRule(t *testing.T) {
	s := NewWithNetScan(stubProvider{readySnapshot(
		openvasHost("192.168.178.42"),
		netscan.Host{IP: "192.168.178.9", Port: 443, Status: 200, Body: "<title>Router</title>"},
		openvasHost("192.168.178.77"),
	)})

	results := s.Scan([]RoutineInput{{
		Name: "openvas",
		Code: `
			var rules = [{
			  name: "OPENVAS SCAN",
			  description: "OpenVAS/Greenbone web interface on the local network",
			  detect: function () {
			    if (!netscan.info().ready) return null;
			    var hits = netscan.find("<title>OPENVAS SCAN</title>");
			    if (hits.length === 0) return null;
			    var out = [];
			    for (var i = 0; i < hits.length; i++) {
			      out.push({
			        name: "OPENVAS SCAN",
			        host: hits[i].ip,
			        version: "unknown",
			        description: "Detected via HTTPS at " + hits[i].url
			      });
			    }
			    return out;
			  }
			}];`,
	}})

	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 (one per matching host): %+v", len(results), results)
	}
	got := map[string]bool{}
	for _, r := range results {
		if !r.Detected {
			t.Errorf("result %+v not marked detected", r)
		}
		got[r.Host] = true
	}
	if !got["192.168.178.42"] || !got["192.168.178.77"] {
		t.Fatalf("hosts = %v, want both OpenVAS addresses", got)
	}
	if got["192.168.178.9"] {
		t.Error("the non-matching host was reported as a detection")
	}
}

// Before the first sweep completes, a routine must be able to tell "nothing
// found" apart from "nothing looked yet" — otherwise a proxy that just started
// reports every network tool absent.
func TestNetScan_NotReadyIsDistinguishable(t *testing.T) {
	s := NewWithNetScan(stubProvider{netscan.Snapshot{Network: "192.168.178.0/24"}})

	results := s.Scan([]RoutineInput{{
		Name: "readiness",
		Code: `
			var rules = [{
			  name: "readiness",
			  detect: function () {
			    if (!netscan.info().ready) return null;
			    return { name: "readiness", version: "1" };
			  }
			}];`,
	}})

	if len(results) != 1 || results[0].Detected {
		t.Fatalf("want a single not-detected result while the sweep is pending, got %+v", results)
	}
	if results[0].Error != "" {
		t.Fatalf("routine errored instead of reporting not-detected: %s", results[0].Error)
	}
}

// netscan disabled: the provider is absent entirely. The API must still exist,
// or every routine referencing it throws.
func TestNetScan_AbsentProviderStillUsable(t *testing.T) {
	s := New() // no provider at all

	results := s.Scan([]RoutineInput{{
		Name: "absent",
		Code: `
			var rules = [{
			  name: "absent",
			  detect: function () {
			    var i = netscan.info();
			    if (i.ready) return { name: "absent" };
			    if (netscan.hosts().length !== 0) return { name: "wrong" };
			    if (netscan.find("x").length !== 0) return { name: "wrong" };
			    if (netscan.get("1.2.3.4") !== null) return { name: "wrong" };
			    return null;
			  }
			}];`,
	}})

	if len(results) != 1 || results[0].Detected || results[0].Error != "" {
		t.Fatalf("want one clean not-detected result, got %+v", results)
	}
}

// A nil *netscan.Scanner behind the interface is the disabled path. Stored
// naively it makes the interface non-nil, so the nil check passes and Snapshot
// is called on a nil receiver — which used to panic and take the proxy down on
// its first scan.
func TestNetScan_TypedNilProviderDoesNotPanic(t *testing.T) {
	var typedNil *netscan.Scanner
	s := NewWithNetScan(typedNil)

	results := s.Scan([]RoutineInput{{
		Name: "typednil",
		Code: `var rules = [{ name: "t", detect: function(){ return netscan.info().ready ? {name:"t"} : null; } }];`,
	}})

	if len(results) != 1 || results[0].Detected {
		t.Fatalf("want one not-detected result, got %+v", results)
	}
}

func TestNetScan_FindMatchingAndGet(t *testing.T) {
	s := NewWithNetScan(stubProvider{readySnapshot(
		openvasHost("192.168.178.42"),
		netscan.Host{IP: "192.168.178.9", Port: 8443, Status: 200, Body: "nmap service"},
	)})

	results := s.Scan([]RoutineInput{{
		Name: "lookups",
		Code: `
			var rules = [{
			  name: "lookups",
			  detect: function () {
			    if (netscan.findMatching("OPENVAS\\s+SCAN").length !== 1) return null;
			    if (netscan.findMatching("([").length !== 0) return null;   // invalid regex: no hits, no throw
			    var h = netscan.get("192.168.178.9", 8443);
			    if (h === null || h.port !== 8443) return null;
			    if (netscan.get("10.0.0.1") !== null) return null;
			    var o = netscan.get("192.168.178.42");
			    if (o.headers["server"] !== "gsad" || o.tlsTrusted !== false) return null;
			    return { name: "lookups", version: "ok" };
			  }
			}];`,
	}})

	if len(results) != 1 || !results[0].Detected {
		t.Fatalf("lookup assertions failed: %+v", results)
	}
}

// httpGet has been in the detection prompt all along while the runtime did not
// provide it — and since routines may not use try/catch, calling it threw and
// failed the whole file.
func TestHTTPGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Probe") != "yes" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		fmt.Fprint(w, "hello from tool")
	}))
	defer srv.Close()

	s := New()
	results := s.Scan([]RoutineInput{{
		Name: "httpget",
		Code: `
			var rules = [{
			  name: "httpget",
			  detect: function () {
			    var bad = httpGet("` + srv.URL + `");
			    if (bad.status !== 403) return null;
			    var r = httpGet("` + srv.URL + `", { headers: { "X-Probe": "yes" }, timeout: 2000 });
			    if (r.status !== 200 || r.body.indexOf("hello from tool") < 0) return null;
			    var fail = httpGet("http://127.0.0.1:1/");
			    if (fail.status !== 0 || fail.error === "") return null;
			    return { name: "httpget", version: "ok" };
			  }
			}];`,
	}})

	if len(results) != 1 || !results[0].Detected {
		t.Fatalf("httpGet assertions failed: %+v", results)
	}
}

// The scanner fills in the proxy's own address only when a routine omits host;
// network detections must keep the address they discovered.
func TestNetScan_DiscoveredHostIsPreserved(t *testing.T) {
	s := NewWithNetScan(stubProvider{readySnapshot(openvasHost("192.168.178.42"))})

	results := s.Scan([]RoutineInput{{
		Name: "hostkeep",
		Code: `
			var rules = [{
			  name: "hostkeep",
			  detect: function () {
			    var h = netscan.find("OPENVAS");
			    return [{ name: "hostkeep", host: h[0].ip }];
			  }
			}];`,
	}})

	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Host != "192.168.178.42" {
		t.Fatalf("host = %q, want the discovered address", results[0].Host)
	}
	if strings.TrimSpace(results[0].Error) != "" {
		t.Fatalf("unexpected error: %s", results[0].Error)
	}
}
