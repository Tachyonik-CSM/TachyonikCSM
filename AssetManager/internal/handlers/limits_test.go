// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the limits on creating records when SystemManager is reachable and
// when it is not: a new host asset, a vulnerability and a detection are
// refused with 503 while the limits cannot be read (AM-DATA-44), and
// reporting an existing host asset again is never refused, neither at the
// limit nor during the outage (AM-DATA-33). Both used to be refused, and the
// findings went ahead unchecked.

package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"tachyonik/assetmanager/internal/systemmanager"
)

// limitedHandler is scopedHandler (user 7 has host 10.0.0.7) with a
// SystemManager that gives user 7 one host asset, or that answers every
// question with 500 when down.
func limitedHandler(t *testing.T, down bool) *Handler {
	t.Helper()
	h := scopedHandler(t)
	sm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/internal/users/7" {
			fmt.Fprint(w, `{"id":7,"username":"u","active":true,"maxHostAssets":1}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(sm.Close)
	h.systemManagerClient = systemmanager.NewClient(sm.URL, "")
	return h
}

func create(t *testing.T, handler http.HandlerFunc, path, body string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	handler(w, callAs("POST", path, body, 0, true))
	return w.Code, w.Body.String()
}

const (
	existingHost = `{"name":"10.0.0.7","type":"Host","userId":7,"sourceId":3,"sourceLabel":"scan.xml"}`
	newHost      = `{"name":"10.0.0.8","type":"Host","userId":7,"sourceId":3,"sourceLabel":"scan.xml"}`
)

// AM-DATA-33 (formerly deviation D2): at the limit, only a new host is refused.
func TestAtTheHostLimitAnExistingHostIsStillReported(t *testing.T) {
	h := limitedHandler(t, false)
	if code, body := create(t, h.CreateAsset, "/api/assets", existingHost); code != http.StatusOK {
		t.Errorf("re-reporting the existing host: %d %s, want 200", code, body)
	}
	if code, body := create(t, h.CreateAsset, "/api/assets", newHost); code != http.StatusForbidden {
		t.Errorf("a new host at the limit: %d %s, want 403", code, body)
	}
}

// AM-DATA-44 (formerly deviations D4 and D5).
func TestWithoutSystemManagerNothingNewIsCreated(t *testing.T) {
	h := limitedHandler(t, true)
	if code, body := create(t, h.CreateAsset, "/api/assets", existingHost); code != http.StatusOK {
		t.Errorf("re-reporting the existing host: %d %s, want 200", code, body)
	}
	for _, c := range []struct {
		what    string
		handler http.HandlerFunc
		path    string
		body    string
	}{
		{"a new host", h.CreateAsset, "/api/assets", newHost},
		{"a vulnerability", h.CreateVulnerability, "/api/vulnerabilities",
			`{"name":"ssh weak","host":"10.0.0.7","port":"22","severity":50,"userId":7,"sourceId":3,"sourceLabel":"scan.xml"}`},
		{"a detection", h.CreateDetection, "/api/detections",
			`{"name":"ssh","host":"10.0.0.7","port":"22","userId":7,"sourceId":3,"sourceLabel":"scan.xml"}`},
	} {
		if code, body := create(t, c.handler, c.path, c.body); code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d %s, want 503", c.what, code, body)
		}
	}
	var n int
	if err := h.db.Get(&n, "SELECT (SELECT COUNT(*) FROM assets) + (SELECT COUNT(*) FROM vulnerabilities) + (SELECT COUNT(*) FROM detections)"); err != nil || n != 1 {
		t.Errorf("records stored: %d (%v), want only the existing host", n, err)
	}
}

// AM-DATA-44: a coverage table that would create a host is answered with 503,
// not with an outcome that counts the outage as a reached limit; one that
// creates nothing is applied as usual.
func TestWithoutSystemManagerACoverageTableThatCreatesIsRefused(t *testing.T) {
	h := limitedHandler(t, true)
	w := httptest.NewRecorder()
	h.ApplyScanCoverage(w, callAs("POST", "/api/assets/scan-coverage", coverageBody, 0, true))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("a table naming a new address: %d %s, want 503", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	h.ApplyScanCoverage(w, callAs("POST", "/api/assets/scan-coverage",
		`{"userId":7,"sourceId":40,"sourceLabel":"cov.txt","entries":[{"target":"10.0.0.0/24","scanCoverageIntervalDays":7}]}`, 0, true))
	if w.Code != http.StatusOK {
		t.Errorf("a table covering only existing hosts: %d %s, want 200", w.Code, w.Body)
	}
}
