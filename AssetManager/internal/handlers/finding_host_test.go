// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins AM-DATA-13: a finding always has the host asset of its owner. Creating
// a finding reports its host, linked to the finding's source, so deleting a
// source and a re-import remove a finding's asset only together with the
// finding. A finding that would need a new host asset is
// held to the host asset limit (AM-DATA-33, AM-DATA-44). After each scenario
// no finding may be left without its asset.

package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"tachyonik/assetmanager/internal/database"
	"tachyonik/lib/listquery"
)

func noFindingWithoutItsAsset(t *testing.T, h *Handler) {
	t.Helper()
	if n, err := h.db.FindingsWithoutHostAsset(); err != nil || n != 0 {
		t.Errorf("%d finding(s) without their host asset (%v)", n, err)
	}
}

func hostAsset(t *testing.T, h *Handler, userID int64, host string) *database.Asset {
	t.Helper()
	assets, err := h.db.ListAssets([]int64{userID}, nil, listquery.Query{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range assets {
		if assets[i].Name == host {
			return &assets[i]
		}
	}
	return nil
}

func call(t *testing.T, handler http.HandlerFunc, method, target, body string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	handler(w, callAs(method, target, body, 0, true))
	return w.Code, w.Body.String()
}

func bySource(label string, id int) string {
	return "?source=" + url.QueryEscape(label+" (ID: "+strconv.Itoa(id)+")")
}

func TestAFindingCreatesItsHostAssetLinkedToItsSource(t *testing.T) {
	h := scopedHandler(t)
	for _, c := range []struct {
		handler http.HandlerFunc
		path    string
		body    string
		host    string
	}{
		{h.CreateVulnerability, "/api/vulnerabilities", `{"name":"CVE-1","host":"10.0.0.20","port":"443","severity":50,"userId":7,"sourceId":4,"sourceLabel":"vulns.xml"}`, "10.0.0.20"},
		{h.CreateDetection, "/api/detections", `{"name":"ssh","host":"10.0.0.21","port":"22","userId":7,"sourceId":4,"sourceLabel":"vulns.xml"}`, "10.0.0.21"},
	} {
		if code, body := call(t, c.handler, "POST", c.path, c.body); code != http.StatusCreated {
			t.Fatalf("%s: %d %s", c.path, code, body)
		}
		a := hostAsset(t, h, 7, c.host)
		if a == nil {
			t.Fatalf("no host asset for %s", c.host)
		}
		if a.Type != database.AssetTypeHost || len(a.Sources) != 1 || a.Sources[0].SourceID == nil || *a.Sources[0].SourceID != 4 {
			t.Errorf("%s: type %s, sources %+v; want Host linked to source 4", c.host, a.Type, a.Sources)
		}
	}

	// A finding on the known host 10.0.0.7 links it to its source, too.
	if code, body := call(t, h.CreateVulnerability, "POST", "/api/vulnerabilities", `{"name":"CVE-2","host":"10.0.0.7","port":"80","severity":20,"userId":7,"sourceId":5,"sourceLabel":"other.xml"}`); code != http.StatusCreated {
		t.Fatalf("%d %s", code, body)
	}
	if a := hostAsset(t, h, 7, "10.0.0.7"); a == nil || len(a.Sources) != 1 || *a.Sources[0].SourceID != 5 {
		t.Errorf("10.0.0.7 not linked to source 5: %+v", a)
	}
	noFindingWithoutItsAsset(t, h)
}

func TestDeletingTheHostsSourceKeepsTheAssetOfAnotherSourcesFindings(t *testing.T) {
	h := scopedHandler(t)
	// Source 1 lists the host; source 2 only reports a vulnerability on it.
	if code, body := call(t, h.CreateAsset, "POST", "/api/assets", `{"name":"10.0.0.30","type":"Host","userId":7,"sourceId":1,"sourceLabel":"hosts.xml"}`); code != http.StatusCreated {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := call(t, h.CreateVulnerability, "POST", "/api/vulnerabilities", `{"name":"CVE-3","host":"10.0.0.30","port":"443","severity":70,"userId":7,"sourceId":2,"sourceLabel":"vulns.xml"}`); code != http.StatusCreated {
		t.Fatalf("%d %s", code, body)
	}

	// Source 1 goes, as ResourceManager deletes it: findings, then assets.
	for _, d := range []http.HandlerFunc{h.DeleteVulnerabilitiesBySource, h.DeleteDetectionsBySource, h.DeleteAssetsBySource} {
		if code, body := call(t, d, "DELETE", "/"+bySource("hosts.xml", 1), ""); code != http.StatusOK {
			t.Fatalf("delete source 1: %d %s", code, body)
		}
	}
	if hostAsset(t, h, 7, "10.0.0.30") == nil {
		t.Fatal("the asset went with source 1 although source 2 still reports a vulnerability on it")
	}
	noFindingWithoutItsAsset(t, h)

	// Source 2 goes: both are gone.
	for _, d := range []http.HandlerFunc{h.DeleteVulnerabilitiesBySource, h.DeleteDetectionsBySource, h.DeleteAssetsBySource} {
		if code, body := call(t, d, "DELETE", "/"+bySource("vulns.xml", 2), ""); code != http.StatusOK {
			t.Fatalf("delete source 2: %d %s", code, body)
		}
	}
	if hostAsset(t, h, 7, "10.0.0.30") != nil {
		t.Error("the asset outlived its last source")
	}
	noFindingWithoutItsAsset(t, h)
}

func TestAReimportWithoutTheHostButWithItsFindingKeepsTheAsset(t *testing.T) {
	h := scopedHandler(t)
	first := `{"name":"10.0.0.40","type":"Host","userId":7,"sourceId":3,"sourceLabel":"scan.xml"}`
	vuln := `{"name":"CVE-4","host":"10.0.0.40","port":"443","severity":60,"userId":7,"sourceId":3,"sourceLabel":"scan.xml"}`
	if code, body := call(t, h.CreateAsset, "POST", "/api/assets", first); code != http.StatusCreated {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := call(t, h.CreateVulnerability, "POST", "/api/vulnerabilities", vuln); code != http.StatusCreated {
		t.Fatalf("%d %s", code, body)
	}

	// The re-import: the new file reports the vulnerability, not the host.
	started := time.Now()
	time.Sleep(1100 * time.Millisecond) // modified_at is stored to the second
	if code, body := call(t, h.CreateVulnerability, "POST", "/api/vulnerabilities", vuln); code != http.StatusCreated {
		t.Fatalf("%d %s", code, body)
	}
	stale := bySource("scan.xml", 3) + "&modifiedBefore=" + url.QueryEscape(started.Add(500*time.Millisecond).Format(time.RFC3339Nano))
	for _, d := range []http.HandlerFunc{h.DeleteStaleVulnerabilitiesBySource, h.DeleteStaleDetectionsBySource, h.DeleteStaleAssetsBySource} {
		if code, body := call(t, d, "DELETE", "/"+stale, ""); code != http.StatusOK {
			t.Fatalf("stale sweep: %d %s", code, body)
		}
	}
	if hostAsset(t, h, 7, "10.0.0.40") == nil {
		t.Error("the stale sweep removed the host asset of a finding the re-import reported")
	}
	noFindingWithoutItsAsset(t, h)
}

func TestAFindingOnANewHostIsHeldToTheHostAssetLimit(t *testing.T) {
	h := limitedHandler(t, false) // user 7 holds 10.0.0.7, the one host allowed
	if code, body := call(t, h.CreateVulnerability, "POST", "/api/vulnerabilities", `{"name":"CVE-6","host":"10.0.0.50","port":"443","severity":50,"userId":7,"sourceId":3,"sourceLabel":"scan.xml"}`); code != http.StatusForbidden || !strings.Contains(body, "Host asset limit reached") {
		t.Errorf("a vulnerability on a new host at the limit: %d %s, want 403", code, body)
	}
	if code, body := call(t, h.CreateDetection, "POST", "/api/detections", `{"name":"ssh","host":"10.0.0.51","port":"22","userId":7,"sourceId":3,"sourceLabel":"scan.xml"}`); code != http.StatusForbidden {
		t.Errorf("a detection on a new host at the limit: %d %s, want 403", code, body)
	}
	if code, body := call(t, h.CreateVulnerability, "POST", "/api/vulnerabilities", `{"name":"CVE-7","host":"10.0.0.7","port":"443","severity":50,"userId":7,"sourceId":3,"sourceLabel":"scan.xml"}`); code != http.StatusCreated {
		t.Errorf("a vulnerability on the existing host at the limit: %d %s, want 201", code, body)
	}
	noFindingWithoutItsAsset(t, h)
}

func TestWithoutSystemManagerAFindingOnANewHostWaits(t *testing.T) {
	h := limitedHandler(t, true)
	if code, body := call(t, h.CreateVulnerability, "POST", "/api/vulnerabilities", `{"name":"CVE-8","host":"10.0.0.60","port":"443","severity":50,"userId":7,"sourceId":3,"sourceLabel":"scan.xml"}`); code != http.StatusServiceUnavailable {
		t.Errorf("%d %s, want 503", code, body)
	}
	noFindingWithoutItsAsset(t, h)
}
