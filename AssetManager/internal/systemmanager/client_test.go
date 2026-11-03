// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins what callers rely on from the SystemManager client: an unknown user or
// organisation is nil without an error (the limit checks then skip), a failed
// group lookup still yields the user themself, a stats entry is accepted on
// SystemManager's 201 and sent at most once per key and second, the service
// key travels with every request, and a redirect is never followed with it.

package systemmanager

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestClient(t *testing.T) {
	var stats, redirected int32
	var lastSubScore map[string]interface{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/internal/users/1", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Service-Key") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"id":1,"maxHostAssets":5}`))
	})
	mux.HandleFunc("/api/internal/users/2", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	mux.HandleFunc("/api/internal/organisation", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	mux.HandleFunc("/api/internal/group-members", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	mux.HandleFunc("/api/internal/stats", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&stats, 1)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/api/internal/organisation/sub-score", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&lastSubScore)
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/api/internal/users/ids", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	mux.HandleFunc("/elsewhere", func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&redirected, 1) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := NewClient(srv.URL, "k")

	if u, err := c.GetUser(1); err != nil || u == nil || u.MaxHostAssets != 5 {
		t.Errorf("GetUser(1) = %+v, %v", u, err)
	}
	if u, err := c.GetUser(2); err != nil || u != nil {
		t.Errorf("unknown user: %+v, %v; want nil, nil", u, err)
	}
	if o, err := c.GetOrganisationByUserID(2); err != nil || o != nil {
		t.Errorf("unknown organisation: %+v, %v; want nil, nil", o, err)
	}
	if ids, err := c.GetGroupMemberIDs(7); err == nil || len(ids) != 1 || ids[0] != 7 {
		t.Errorf("failed group lookup: %v, %v; want [7] and an error", ids, err)
	}
	for i := 0; i < 3; i++ {
		if err := c.CreateStatsEntry(7, "total_assets", 1); err != nil {
			t.Errorf("stats entry: %v", err)
		}
	}
	if n := atomic.LoadInt32(&stats); n != 1 {
		t.Errorf("stats sent %d times within the throttle window, want 1", n)
	}
	if err := c.UpdateOrganisationSubScore(7, "coverage", 42); err != nil || lastSubScore["subScore"] != "coverage" || lastSubScore["value"] != 42.0 {
		t.Errorf("sub-score: %v, sent %v", err, lastSubScore)
	}
	if _, err := c.ListAllUserIDs(); err == nil {
		t.Error("a redirect was taken as success")
	}
	if atomic.LoadInt32(&redirected) != 0 {
		t.Error("the redirect was followed")
	}
}
