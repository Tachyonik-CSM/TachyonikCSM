// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that a scan coverage table goes to AssetManager in one request, naming
// the user and the source, and that its answer is read.

package assetmanager

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestApplyScanCoverageSendsTheTable(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/assets/scan-coverage" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"covered":[4,5],"created":[5],"hostnames":1,"unmatched":0,"invalid":0,"refused":0}`))
	}))
	defer srv.Close()

	out, err := NewClient(srv.URL, "k").ApplyScanCoverage(40, "cov.txt", 52, []ScanCoverageEntry{
		{Target: "10.0.0.5", IntervalDays: 7, VerifiedAt: "2026-10-21T07:10:09Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Covered) != 2 || len(out.Created) != 1 || out.Hostnames != 1 {
		t.Errorf("outcome = %+v", out)
	}
	if got["userId"] != float64(52) || got["sourceId"] != float64(40) || got["sourceLabel"] != "cov.txt" {
		t.Errorf("payload = %v", got)
	}
	entries, _ := got["entries"].([]interface{})
	if len(entries) != 1 || entries[0].(map[string]interface{})["scanCoverageIntervalDays"] != float64(7) {
		t.Errorf("entries = %v", got["entries"])
	}
}
