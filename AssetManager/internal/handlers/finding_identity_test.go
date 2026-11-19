// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins AM-DATA-47 through the API: the same finding sent twice is one record
// (201, then 200 with the same id), and reporting a known finding again is
// never refused for a limit, nor while SystemManager cannot be asked —
// only a new one is (AM-DATA-35, AM-DATA-44).

package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"tachyonik/assetmanager/internal/systemmanager"
)

const fritz = `{"name":"AVM FRITZ!Box Information Disclosure","host":"192.168.178.1","port":"80/tcp","severity":78,"userId":7,"sourceId":109,"sourceLabel":"scan.xml"}`

func idOf(t *testing.T, body string) int64 {
	t.Helper()
	var v struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return v.ID
}

func TestTheSameFindingTwiceIsOneRecord(t *testing.T) {
	h := scopedHandler(t)
	code1, body1 := call(t, h.CreateVulnerability, "POST", "/api/vulnerabilities", fritz)
	code2, body2 := call(t, h.CreateVulnerability, "POST", "/api/vulnerabilities", fritz)
	if code1 != http.StatusCreated || code2 != http.StatusOK || idOf(t, body1) != idOf(t, body2) {
		t.Errorf("first %d %s, second %d %s; want 201, then 200 with the same id", code1, body1, code2, body2)
	}
	if n, _ := h.db.CountVulnerabilitiesByUserWithFilter(7, nil); n != 1 {
		t.Errorf("%d vulnerabilities, want 1", n)
	}
	det := `{"name":"ssh","host":"192.168.178.1","port":"22/tcp","userId":7,"sourceId":109,"sourceLabel":"scan.xml"}`
	c1, _ := call(t, h.CreateDetection, "POST", "/api/detections", det)
	c2, _ := call(t, h.CreateDetection, "POST", "/api/detections", det)
	if c1 != http.StatusCreated || c2 != http.StatusOK {
		t.Errorf("detection twice: %d, %d; want 201, 200", c1, c2)
	}
}

// limitOneHandler is scopedHandler with a SystemManager that allows user 7
// one vulnerability, or answers nothing when down.
func limitOneHandler(t *testing.T, down *bool) *Handler {
	t.Helper()
	h := scopedHandler(t)
	sm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if *down {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":7,"active":true,"maxVulnerabilities":1,"maxHostAssets":100}`)
	}))
	t.Cleanup(sm.Close)
	h.systemManagerClient = systemmanager.NewClient(sm.URL, "")
	return h
}

func TestAKnownFindingIsNeverRefusedForALimit(t *testing.T) {
	down := false
	h := limitOneHandler(t, &down)
	if code, body := call(t, h.CreateVulnerability, "POST", "/api/vulnerabilities", fritz); code != http.StatusCreated {
		t.Fatalf("first: %d %s", code, body)
	}
	if code, body := call(t, h.CreateVulnerability, "POST", "/api/vulnerabilities", fritz); code != http.StatusOK {
		t.Errorf("the known one at the limit: %d %s, want 200", code, body)
	}
	other := `{"name":"Another","host":"192.168.178.1","port":"80/tcp","severity":50,"userId":7,"sourceId":109,"sourceLabel":"scan.xml"}`
	if code, body := call(t, h.CreateVulnerability, "POST", "/api/vulnerabilities", other); code != http.StatusForbidden {
		t.Errorf("a new one at the limit: %d %s, want 403", code, body)
	}

	down = true
	if code, body := call(t, h.CreateVulnerability, "POST", "/api/vulnerabilities", fritz); code != http.StatusOK {
		t.Errorf("the known one while SystemManager is down: %d %s, want 200", code, body)
	}
	if code, body := call(t, h.CreateVulnerability, "POST", "/api/vulnerabilities", other); code != http.StatusServiceUnavailable {
		t.Errorf("a new one while SystemManager is down: %d %s, want 503", code, body)
	}
}
