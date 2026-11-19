// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins AM-SEC-13: a service that creates a record must name its owner, in
// the body or with X-User-ID, and the owner must exist (AM-DATA-33,
// AM-DATA-35). Records for user 0, or for a user no one knows, used to be
// created: no one saw them, no limit held them, and the next orphan sweep
// deleted them.

package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tachyonik/assetmanager/internal/systemmanager"
	"tachyonik/lib/listquery"
)

// ownerHandler is scopedHandler with a SystemManager that knows user 7 only.
func ownerHandler(t *testing.T) *Handler {
	t.Helper()
	h := scopedHandler(t)
	sm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/internal/users/7" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":7,"username":"u","active":true,"maxHostAssets":100,"maxVulnerabilities":100,"maxDetections":100}`)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/internal/users/") {
			http.Error(w, `{"error":"User not found"}`, http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(sm.Close)
	h.systemManagerClient = systemmanager.NewClient(sm.URL, "")
	return h
}

type createCase struct {
	what    string
	handler http.HandlerFunc
	path    string
	body    string // with %s for the owner field
}

func createCases(h *Handler) []createCase {
	return []createCase{
		{"asset", h.CreateAsset, "/api/assets", `{"name":"10.0.0.9","type":"Host",%s"sourceId":3,"sourceLabel":"scan.xml"}`},
		{"vulnerability", h.CreateVulnerability, "/api/vulnerabilities", `{"name":"CVE-1","host":"10.0.0.9","port":"443","severity":50,%s"sourceId":3,"sourceLabel":"scan.xml"}`},
		{"detection", h.CreateDetection, "/api/detections", `{"name":"ssh","host":"10.0.0.9","port":"22",%s"sourceId":3,"sourceLabel":"scan.xml"}`},
		{"scan coverage", h.ApplyScanCoverage, "/api/assets/scan-coverage", `{%s"sourceId":3,"sourceLabel":"scan.xml","entries":[{"target":"10.0.0.9","scanCoverageIntervalDays":7}]}`},
	}
}

func TestAServiceMustNameTheOwner(t *testing.T) {
	h := ownerHandler(t)
	for _, c := range createCases(h) {
		for _, owner := range []string{``, `"userId":0,`, `"userId":-3,`} {
			w := httptest.NewRecorder()
			c.handler(w, callAs("POST", c.path, fmt.Sprintf(c.body, owner), 0, true))
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), ownerRequired) {
				t.Errorf("%s with owner %q: %d %s, want 400 %q", c.what, owner, w.Code, w.Body, ownerRequired)
			}
		}
	}
	// Nothing was created for user 0.
	if assets, _ := h.db.ListAssets([]int64{0}, nil, listquery.Query{}); len(assets) != 0 {
		t.Errorf("%d assets for user 0", len(assets))
	}
}

func TestAnOwnerNoOneKnowsIsRefused(t *testing.T) {
	h := ownerHandler(t)
	for _, c := range createCases(h) {
		w := httptest.NewRecorder()
		c.handler(w, callAs("POST", c.path, fmt.Sprintf(c.body, `"userId":99,`), 0, true))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), unknownOwner) {
			t.Errorf("%s for user 99: %d %s, want 400 %q", c.what, w.Code, w.Body, unknownOwner)
		}
	}
	if assets, _ := h.db.ListAssets([]int64{99}, nil, listquery.Query{}); len(assets) != 0 {
		t.Errorf("%d assets for user 99", len(assets))
	}
}

func TestTheOwnerMayBeNamedInTheBodyOrTheHeader(t *testing.T) {
	h := ownerHandler(t)
	for _, c := range createCases(h) {
		w := httptest.NewRecorder()
		c.handler(w, callAs("POST", c.path, fmt.Sprintf(c.body, `"userId":7,`), 0, true))
		if w.Code >= 300 {
			t.Errorf("%s for user 7 in the body: %d %s", c.what, w.Code, w.Body)
		}
		// X-User-ID: the middleware puts the user it names in the context.
		r := callAs("POST", c.path, fmt.Sprintf(c.body, ``), 0, true)
		r = r.WithContext(context.WithValue(r.Context(), userIDKey, int64(7)))
		w = httptest.NewRecorder()
		c.handler(w, r)
		if w.Code >= 300 {
			t.Errorf("%s for user 7 by X-User-ID: %d %s", c.what, w.Code, w.Body)
		}
	}
}

// AM-PUR-20, AM-SEC-12: only a service importing a source creates records.
func TestAUserCreatesNothing(t *testing.T) {
	h := ownerHandler(t)
	for _, c := range createCases(h) {
		w := httptest.NewRecorder()
		c.handler(w, callAs("POST", c.path, fmt.Sprintf(c.body, ``), 7, false))
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), importOnly) {
			t.Errorf("%s as user 7: %d %s, want 403 %q", c.what, w.Code, w.Body, importOnly)
		}
	}
	if assets, _ := h.db.ListAssets([]int64{7}, nil, listquery.Query{}); len(assets) != 1 {
		t.Errorf("user 7 has %d assets, want only the original one", len(assets))
	}
}
