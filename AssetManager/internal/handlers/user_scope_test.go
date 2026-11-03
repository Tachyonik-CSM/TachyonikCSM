// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests whose assets a caller may name. A service names any user — that is
// how ActionGenerator reads everyone's assets — but a logged-in user cannot:
// ?userId= used to be taken from anyone, so a user could list another user's
// assets and statistics, and a service-key header that the middleware never
// checked (no key configured) let a user choose an asset's owner.

package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"tachyonik/assetmanager/internal/config"
	"tachyonik/assetmanager/internal/database"
	"tachyonik/assetmanager/internal/systemmanager"
	ws "tachyonik/assetmanager/internal/websocket"
)

func scopedHandler(t *testing.T) *Handler {
	t.Helper()
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.CreateAsset("10.0.0.7", "Host", 7, 0, "", nil); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	hub := ws.NewHub()
	go hub.Run()
	return &Handler{db: db, hub: hub, config: &config.Config{}, systemManagerClient: stubSystemManager(t)}
}

// stubSystemManager stands in for SystemManager: every user exists, is
// active and has no limits, and is the only member of their workspace.
// Statistics and audit events are taken and dropped.
func stubSystemManager(t *testing.T) *systemmanager.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/internal/users/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/internal/users/")
		w.Write([]byte(`{"id":` + id + `,"active":true}`))
	})
	mux.HandleFunc("/api/internal/group-members", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"userIds":[` + r.URL.Query().Get("userId") + `]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusCreated) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return systemmanager.NewClient(srv.URL, "")
}

func callAs(method, target, body string, userID int64, service bool) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	ctx := context.WithValue(r.Context(), userIDKey, userID)
	if service {
		ctx = context.WithValue(ctx, internalSvcKey, true)
	}
	return r.WithContext(ctx)
}

func TestAUserCannotListAnotherUsersAssets(t *testing.T) {
	h := scopedHandler(t)
	for _, path := range []string{"/api/assets?userId=7", "/api/assets/stats?userId=7"} {
		handler := h.ListAssets
		if strings.Contains(path, "stats") {
			handler = h.GetAssetStats
		}
		w := httptest.NewRecorder()
		handler(w, callAs("GET", path, "", 52, false))
		if w.Code != http.StatusForbidden {
			t.Errorf("%s as user 52: %d, want 403", path, w.Code)
		}
		w = httptest.NewRecorder()
		handler(w, callAs("GET", path, "", 0, true))
		if w.Code != http.StatusOK {
			t.Errorf("%s as a service: %d, want 200", path, w.Code)
		}
	}
}

// A user's own list without ?userId= still works and shows only their assets.
func TestAUsersOwnListIsUnchanged(t *testing.T) {
	h := scopedHandler(t)
	w := httptest.NewRecorder()
	h.ListAssets(w, callAs("GET", "/api/assets", "", 52, false))
	var resp struct {
		Assets []database.Asset `json:"assets"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if w.Code != http.StatusOK || len(resp.Assets) != 0 {
		t.Errorf("user 52's own list: %d with %d asset(s), want 200 and none of user 7's", w.Code, len(resp.Assets))
	}
}

// A user sending the service-key header — unchecked when no key is configured
// — still creates the asset for themselves, not for the body's userId.
func TestAUserCannotChooseAnAssetsOwner(t *testing.T) {
	h := scopedHandler(t)
	r := callAs("POST", "/api/assets", `{"name":"printer-9","type":"Host","userId":7,"sourceId":1,"sourceLabel":"scan.xml"}`, 52, false)
	r.Header.Set("X-Internal-Service-Key", "anything")
	w := httptest.NewRecorder()
	h.CreateAsset(w, r)
	if mine, _ := h.db.GetAssetsByUsersWithFilter([]int64{52}, nil); len(mine) != 1 {
		t.Errorf("status %d: user 52 has %d asset(s), want the new one (%s)", w.Code, len(mine), w.Body.String())
	}
	if theirs, _ := h.db.GetAssetsByUsersWithFilter([]int64{7}, nil); len(theirs) != 1 {
		t.Errorf("user 7 has %d asset(s), want only their original one", len(theirs))
	}
}

// A user sending the service-key header — unchecked when no key is configured
// — used to make this act for ALL users. It now clears only their own.
func TestAUserCannotClearAnotherUsersAssetSources(t *testing.T) {
	h := scopedHandler(t)
	if _, err := h.db.CreateAsset("printer-7", "Host", 7, 1, "scan.xml", nil); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	r := callAs("PATCH", "/api/assets/by-source?source="+url.QueryEscape("scan.xml (ID: 1)"), "", 52, false)
	r.Header.Set("X-Internal-Service-Key", "anything")
	w := httptest.NewRecorder()
	h.ClearAssetSourcesBySource(w, r)

	theirs, _ := h.db.GetAssetsByUsersWithFilter([]int64{7}, nil)
	for _, a := range theirs {
		if a.Name == "printer-7" && len(a.Sources) == 0 {
			t.Errorf("status %d: user 52 cleared user 7's asset source", w.Code)
		}
	}
}

// The re-import cleanups delete for every user, so only a service may call
// them. A key header the middleware did not accept — an instance with no key
// configured passes it through to the JWT path — must not count as one.
func TestOnlyAServiceDeletesStaleRecords(t *testing.T) {
	h := scopedHandler(t)
	for name, handler := range map[string]http.HandlerFunc{
		"assets":          h.DeleteStaleAssetsBySource,
		"vulnerabilities": h.DeleteStaleVulnerabilitiesBySource,
		"detections":      h.DeleteStaleDetectionsBySource,
	} {
		target := "/api/" + name + "/stale?source=" + url.QueryEscape("scan.xml (ID: 1)") + "&modifiedBefore=2026-01-01T00:00:00Z"
		r := callAs("DELETE", target, "", 52, false)
		r.Header.Set("X-Internal-Service-Key", "anything")
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s as user 52 with an unchecked key header: %d, want 403", name, w.Code)
		}
		w = httptest.NewRecorder()
		handler(w, callAs("DELETE", target, "", 0, true))
		if w.Code != http.StatusOK {
			t.Errorf("%s as a service: %d, want 200 (%s)", name, w.Code, w.Body.String())
		}
	}
}

// ?filter= is read straight into the database filter: a rule narrows the
// list, and a filter that is not JSON is refused rather than ignored.
func TestListFilterParameter(t *testing.T) {
	h := scopedHandler(t)
	list := func(filter string) (int, int) {
		w := httptest.NewRecorder()
		h.ListAssets(w, callAs("GET", "/api/assets?filter="+url.QueryEscape(filter), "", 7, false))
		var resp struct {
			Assets []database.Asset `json:"assets"`
			Total  int              `json:"total"`
		}
		json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, len(resp.Assets)
	}
	if code, n := list(`{"matchAll":true,"rules":[{"column":"name","operator":"contains","value":"10.0.0"}]}`); code != http.StatusOK || n != 1 {
		t.Errorf("matching filter: %d, %d assets; want 200 and 1", code, n)
	}
	if code, n := list(`{"matchAll":true,"rules":[{"column":"name","operator":"contains","value":"nomatch"}]}`); code != http.StatusOK || n != 0 {
		t.Errorf("non-matching filter: %d, %d assets; want 200 and 0", code, n)
	}
	if code, _ := list(`not json`); code != http.StatusBadRequest {
		t.Errorf("invalid filter: %d, want 400", code)
	}
}

// An asset is linked to the source it came from, given as sourceId and
// sourceLabel. The "filename (ID: N)" string once accepted as source is no
// longer read: a create carrying only that is refused like one without any.
func TestCreateAssetNeedsASource(t *testing.T) {
	h := scopedHandler(t)
	for name, c := range map[string]struct {
		body string
		want int
	}{
		"no source":           {`{"name":"10.0.0.9","type":"Host"}`, http.StatusBadRequest},
		"only the old string": {`{"name":"10.0.0.9","type":"Host","source":"scan.xml (ID: 1)"}`, http.StatusBadRequest},
		"source id and label": {`{"name":"10.0.0.9","type":"Host","sourceId":1,"sourceLabel":"scan.xml"}`, http.StatusCreated},
		"an unknown type":     {`{"name":"www.example.com","type":"Domain","sourceId":1,"sourceLabel":"scan.xml"}`, http.StatusBadRequest},
	} {
		w := httptest.NewRecorder()
		h.CreateAsset(w, callAs("POST", "/api/assets", c.body, 7, false))
		if w.Code != c.want {
			t.Errorf("%s: %d, want %d (%s)", name, w.Code, c.want, w.Body.String())
		}
	}
}
