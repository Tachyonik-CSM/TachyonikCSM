// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests POST /api/assets/scan-coverage as SourceImporter calls it: a service
// naming the user, a table of targets, and an answer saying what was done —
// with the host asset limit stopping new assets but not the coverage of
// existing ones.

package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"tachyonik/assetmanager/internal/database"
	"tachyonik/assetmanager/internal/systemmanager"
)

func coverageHandler(t *testing.T, maxHostAssets int) *Handler {
	t.Helper()
	h := scopedHandler(t) // has host 10.0.0.7 of user 7
	sm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/internal/users/7" {
			fmt.Fprintf(w, `{"id":7,"username":"u","maxHostAssets":%d}`, maxHostAssets)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(sm.Close)
	h.systemManagerClient = systemmanager.NewClient(sm.URL, "")
	return h
}

func postCoverage(t *testing.T, h *Handler, body string) database.CoverageOutcome {
	t.Helper()
	w := httptest.NewRecorder()
	h.ApplyScanCoverage(w, callAs("POST", "/api/assets/scan-coverage", body, 0, true))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var out database.CoverageOutcome
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

const coverageBody = `{"userId":7,"sourceId":40,"sourceLabel":"openvas-scan-coverage.txt","entries":[
	{"target":"10.0.0.0/24","scanCoverageIntervalDays":7,"scanCoverageVerifiedAt":"2026-10-21T07:10:09Z"},
	{"target":"10.0.1.5","scanCoverageIntervalDays":1,"scanCoverageVerifiedAt":"2026-10-21T07:10:09Z"},
	{"target":"scanner.lab","scanCoverageIntervalDays":1}]}`

func TestACoverageTableIsAppliedForAService(t *testing.T) {
	h := coverageHandler(t, 0)
	out := postCoverage(t, h, coverageBody)
	if len(out.Covered) != 2 || len(out.Created) != 1 || out.Hostnames != 1 {
		t.Fatalf("outcome = %+v", out)
	}
	var interval int
	if err := h.db.Get(&interval, "SELECT scan_coverage_interval_days FROM assets WHERE user_id = 7 AND name = '10.0.0.7'"); err != nil || interval != 7 {
		t.Errorf("10.0.0.7 interval = %d (%v), want 7", interval, err)
	}
}

func TestTheHostAssetLimitStopsOnlyNewAssets(t *testing.T) {
	h := coverageHandler(t, 1) // user 7 already has its one host
	out := postCoverage(t, h, coverageBody)
	if len(out.Covered) != 1 || len(out.Created) != 0 || out.Refused != 1 {
		t.Errorf("outcome = %+v", out)
	}
}

func TestACoverageTableNeedsASource(t *testing.T) {
	h := coverageHandler(t, 0)
	w := httptest.NewRecorder()
	h.ApplyScanCoverage(w, callAs("POST", "/api/assets/scan-coverage", `{"userId":7,"entries":[]}`, 0, true))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", w.Code)
	}
}
