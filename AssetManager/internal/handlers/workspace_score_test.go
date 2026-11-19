// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins AM-DATA-41 and AM-DATA-42 for a shared workspace: on an appliance the
// Exposure and Coverage scores are the workspace's — computed over the
// records of all its users and pushed to each user's organisation, once per
// organisation — while in SaaS they stay the owner's.

package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"tachyonik/assetmanager/internal/config"
	"tachyonik/assetmanager/internal/systemmanager"
)

type pushedScore struct {
	UserID   int64   `json:"userId"`
	SubScore string  `json:"subScore"`
	Value    float64 `json:"value"`
}

// scoreSystemManager knows users 7, 9 and 11: 7 and 9 in organisation 1, 11
// in organisation 2, each declaring 4 hosts. It records every sub-score
// pushed.
func scoreSystemManager(t *testing.T) (*systemmanager.Client, func() []pushedScore) {
	t.Helper()
	var mu sync.Mutex
	var pushed []pushedScore
	orgOf := map[string]int{"7": 1, "9": 1, "11": 2}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/internal/group-members":
			fmt.Fprint(w, `{"userIds":[7,9,11]}`)
		case r.URL.Path == "/api/internal/organisation":
			fmt.Fprintf(w, `{"id":%d,"name":"Org","assetsHosts":4}`, orgOf[r.URL.Query().Get("userId")])
		case r.URL.Path == "/api/internal/organisation/sub-score":
			var p pushedScore
			_ = json.NewDecoder(r.Body).Decode(&p)
			mu.Lock()
			pushed = append(pushed, p)
			mu.Unlock()
			fmt.Fprint(w, `{}`)
		case strings.HasPrefix(r.URL.Path, "/api/internal/users/"):
			fmt.Fprint(w, `{"id":7,"active":true}`)
		default:
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(srv.Close)
	return systemmanager.NewClient(srv.URL, ""), func() []pushedScore {
		mu.Lock()
		defer mu.Unlock()
		return append([]pushedScore(nil), pushed...)
	}
}

func scoresByUser(p []pushedScore) map[string]float64 {
	out := map[string]float64{}
	for _, s := range p {
		out[fmt.Sprintf("%d %s", s.UserID, s.SubScore)] = s.Value
	}
	return out
}

func workspaceScoreHandler(t *testing.T, mode string) (*Handler, func() []pushedScore) {
	t.Helper()
	h := scopedHandler(t) // user 7's 10.0.0.7, without findings: score 0
	h.config = &config.Config{Instance: config.InstanceConfig{Mode: mode}}
	sm, pushed := scoreSystemManager(t)
	h.systemManagerClient = sm
	// User 9: two hosts with findings of severity 0, score 100 each.
	for _, host := range []string{"10.0.0.8", "10.0.0.9"} {
		if _, err := h.db.CreateAsset(host, "Host", 9, 1, "scan.xml", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := h.db.CreateDetection("ssh", host, "22", 1, "scan.xml", 9, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.db.RefreshAllAssets(); err != nil {
		t.Fatal(err)
	}
	return h, pushed
}

func TestOnAnApplianceTheScoresAreTheWorkspaces(t *testing.T) {
	h, pushed := workspaceScoreHandler(t, config.ModeAppliance)
	h.recalculateOrganisationScore(9)

	got := scoresByUser(pushed())
	// Three hosts in the workspace (0, 100, 100): exposure 66.67; the one
	// unmanaged host of the four declared counts against coverage: 50.
	// Organisation 1 once (through 7), organisation 2 through 11.
	if len(pushed()) != 4 {
		t.Fatalf("pushed %v, want exposure and coverage for organisations 1 and 2", pushed())
	}
	for _, key := range []string{"7 exposure", "11 exposure"} {
		if v := got[key]; v < 66.6 || v > 66.7 {
			t.Errorf("%s = %.2f, want the workspace's 66.67", key, v)
		}
	}
	for _, key := range []string{"7 coverage", "11 coverage"} {
		if v := got[key]; v != 50 {
			t.Errorf("%s = %.2f, want the workspace's 50", key, v)
		}
	}
}

func TestInSaaSTheScoresAreTheOwners(t *testing.T) {
	h, pushed := workspaceScoreHandler(t, config.ModeSaaS)
	h.recalculateOrganisationScore(9)
	got := scoresByUser(pushed())
	if len(pushed()) != 2 || got["9 exposure"] != 100 {
		t.Errorf("pushed %v, want only user 9's own exposure of 100", pushed())
	}
}
