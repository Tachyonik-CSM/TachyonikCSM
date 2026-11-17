// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Checks real answers of AssetManager's handlers against spec/openapi.yaml
// (TachyonikLib apicontract.CheckAnswer): each must validate against its
// documented answer and have no field the document leaves out. Covers the
// operations other modules' clients call (SourceImporter, SystemManager,
// ToolManager); the clients check their side against the same document.

package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"tachyonik/assetmanager/internal/config"
	"tachyonik/assetmanager/internal/systemmanager"
	"tachyonik/lib/apicontract"
)

func TestServiceAnswersAreDocumented(t *testing.T) {
	h := scopedHandler(t)
	h.config = &config.Config{}
	h.config.Auth.InternalServiceKey = "svc-key"
	h.config.Auth.JWTSecret = testSecret
	h.accounts = newAccountChecker(&fakeUsers{users: map[int64]*systemmanager.User{}})
	srv := httptest.NewServer(h.SetupRoutes())
	t.Cleanup(srv.Close)
	doc := document(t)

	call := func(method, path, body string, want ...int) {
		t.Helper()
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, srv.URL+path, r)
		req.Header.Set("X-Internal-Service-Key", "svc-key")
		req.Header.Set("X-User-ID", "1")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		got, _ := io.ReadAll(resp.Body)
		ok := false
		for _, s := range want {
			ok = ok || resp.StatusCode == s
		}
		if !ok {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, got)
		}
		for _, p := range apicontract.CheckAnswer(doc, method, path, got) {
			t.Error(p)
		}
	}

	// What an import writes, with its optional fields: a host with its scan
	// coverage, a finding of each kind, and a coverage table.
	call("POST", "/api/assets", `{"name":"10.0.0.9","type":"Host","sourceId":5,"sourceLabel":"scan.xml","userId":1,"lastSeen":"2026-11-01T00:00:00Z","scanCoverageVerifiedAt":"2026-11-01T00:00:00Z","scanCoverageIntervalDays":7}`, http.StatusCreated)
	call("POST", "/api/assets", `{"name":"10.0.0.9","type":"Host","sourceId":6,"sourceLabel":"other.xml","userId":1}`, http.StatusOK)
	call("POST", "/api/vulnerabilities", `{"name":"Weak cipher","host":"10.0.0.9","port":"443/tcp","severity":40,"sourceId":5,"sourceLabel":"scan.xml","userId":1,"lastSeen":"2026-11-01T00:00:00Z"}`, http.StatusCreated)
	call("POST", "/api/detections", `{"name":"HTTPS Server Detection","host":"10.0.0.9","port":"443/tcp","sourceId":5,"sourceLabel":"scan.xml","userId":1,"lastSeen":"2026-11-01T00:00:00Z"}`, http.StatusCreated)
	call("POST", "/api/assets/scan-coverage", `{"userId":1,"sourceId":5,"sourceLabel":"scan.xml","entries":[{"target":"10.0.0.0/24","scanCoverageIntervalDays":7,"scanCoverageVerifiedAt":"2026-11-01T00:00:00Z"},{"target":"10.0.1.1","scanCoverageIntervalDays":1},{"target":"host.example","scanCoverageIntervalDays":1},{"target":"not a target","scanCoverageIntervalDays":1}]}`, http.StatusOK)

	// What the chat reads.
	call("GET", "/api/assets/stats?userId=1", "", http.StatusOK)
	call("GET", "/api/assets?userId=1", "", http.StatusOK)
	call("GET", "/api/assets?userId=1&filter="+url.QueryEscape(`{"rules":[{"column":"name","operator":"contains","value":"10.0"}],"matchAll":true}`), "", http.StatusOK)
	call("GET", "/api/vulnerabilities", "", http.StatusOK)
	call("GET", "/api/detections?filter="+url.QueryEscape(`{"rules":[{"column":"host","operator":"contains","value":"10.0"}],"matchAll":true}`), "", http.StatusOK)
	// Sorted and cut, as the chat's generated tools ask (AM-API-16).
	call("GET", "/api/assets?userId=1&sort=severity&limit=1", "", http.StatusOK)
	call("GET", "/api/vulnerabilities?sort=lastSeen&order=asc&limit=1", "", http.StatusOK)
	call("GET", "/api/detections?sort=name&limit=1", "", http.StatusOK)

	// A re-import's sweep: everything of source 5 is older than now.
	stale := "?source=" + url.QueryEscape("scan.xml (ID: 5)") + "&modifiedBefore=" + url.QueryEscape(time.Now().Add(time.Hour).Format(time.RFC3339))
	call("DELETE", "/api/vulnerabilities/stale"+stale, "", http.StatusOK)
	call("DELETE", "/api/detections/stale"+stale, "", http.StatusOK)
	call("DELETE", "/api/assets/stale"+stale, "", http.StatusOK)

	// What SystemManager calls.
	call("POST", "/api/internal/organisation/recalculate-score", `{"userId":1}`, http.StatusOK)
	call("GET", "/api/internal/users/ids", "", http.StatusOK)
	call("PATCH", "/api/internal/users/1/data/transfer?newOwner=3", "", http.StatusOK)
	call("DELETE", "/api/internal/users/3/data", "", http.StatusOK)
	call("DELETE", "/api/internal/users/7/data", "", http.StatusOK)
	call("GET", "/api/internal/users/ids", "", http.StatusOK) // no one left: [], not null
}
