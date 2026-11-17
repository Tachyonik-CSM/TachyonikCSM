// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for sorting and cutting the asset, vulnerability and detection lists
// (AM-API-16): each sort field in both directions and its default, a limit
// that cuts the answer while total still counts every match (with a filter
// too), every bad value refused with 400 naming what is allowed, and a list
// asked without the parameters answered exactly as before.

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

// orderedHandler holds user 7's assets alpha (severity 3), Bravo (9) and
// charlie (5), created in that order, and as many vulnerabilities and
// detections named the same way.
func orderedHandler(t *testing.T) *Handler {
	t.Helper()
	h := scopedHandler(t) // brings asset 10.0.0.7, severity 0
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i, c := range []struct {
		name string
		sev  int
	}{{"alpha", 3}, {"Bravo", 9}, {"charlie", 5}} {
		seen := base.Add(time.Duration(i) * time.Hour)
		res, err := h.db.CreateAsset(c.name, "Host", 7, 0, "f.xml", &seen)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.db.Exec("UPDATE assets SET severity = ?, created_at = ? WHERE id = ?", c.sev, seen, res.Asset.ID); err != nil {
			t.Fatal(err)
		}
		v, err := h.db.CreateVulnerability(c.name, c.name, "443", c.sev*10, 0, "f.xml", 7, &seen)
		if err != nil {
			t.Fatal(err)
		}
		d, err := h.db.CreateDetection(c.name, c.name, "80", 0, "f.xml", 7, &seen)
		if err != nil {
			t.Fatal(err)
		}
		for table, id := range map[string]int64{"vulnerabilities": v.ID, "detections": d.ID} {
			if _, err := h.db.Exec("UPDATE "+table+" SET created_at = ? WHERE id = ?", seen, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	return h
}

type namedList struct {
	names []string
	total int
}

func list(t *testing.T, handler http.HandlerFunc, path, field string) (int, namedList, string) {
	t.Helper()
	w := httptest.NewRecorder()
	handler(w, callAs("GET", path, "", 7, false))
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: %s", path, w.Body)
	}
	if w.Code != http.StatusOK {
		var e string
		json.Unmarshal(body["error"], &e)
		return w.Code, namedList{}, e
	}
	var rows []struct {
		Name string `json:"name"`
	}
	json.Unmarshal(body[field], &rows)
	var out namedList
	for _, r := range rows {
		out.names = append(out.names, r.Name)
	}
	json.Unmarshal(body["total"], &out.total)
	return w.Code, out, ""
}

// AM-API-16
func TestTheListsSortAndCut(t *testing.T) {
	h := orderedHandler(t)
	nameFilter := "&filter=" + url.QueryEscape(`{"rules":[{"column":"name","operator":"contains","value":"a"}],"matchAll":true}`)
	for _, c := range []struct {
		handler     http.HandlerFunc
		path, field string
		want        []string
		total       int
	}{
		{h.ListAssets, "/api/assets?sort=severity", "assets", []string{"Bravo", "charlie", "alpha", "10.0.0.7"}, 4},
		{h.ListAssets, "/api/assets?sort=severity&order=asc", "assets", []string{"10.0.0.7", "alpha", "charlie", "Bravo"}, 4},
		{h.ListAssets, "/api/assets?sort=name", "assets", []string{"10.0.0.7", "alpha", "Bravo", "charlie"}, 4},
		{h.ListAssets, "/api/assets?sort=name&order=desc&limit=2", "assets", []string{"charlie", "Bravo"}, 4},
		{h.ListAssets, "/api/assets?sort=lastSeen&limit=1", "assets", []string{"10.0.0.7"}, 4},
		{h.ListAssets, "/api/assets?limit=500", "assets", []string{"10.0.0.7", "charlie", "Bravo", "alpha"}, 4},
		{h.ListAssets, "/api/assets?sort=severity&limit=1" + nameFilter, "assets", []string{"Bravo"}, 3},
		{h.ListVulnerabilities, "/api/vulnerabilities?sort=severity&limit=2", "vulnerabilities", []string{"Bravo", "charlie"}, 3},
		{h.ListVulnerabilities, "/api/vulnerabilities?sort=host&order=desc", "vulnerabilities", []string{"charlie", "Bravo", "alpha"}, 3},
		{h.ListVulnerabilities, "/api/vulnerabilities?sort=createdAt&order=asc&limit=1", "vulnerabilities", []string{"alpha"}, 3},
		{h.ListDetections, "/api/detections?sort=name&limit=2", "detections", []string{"alpha", "Bravo"}, 3},
		{h.ListDetections, "/api/detections?limit=1", "detections", []string{"charlie"}, 3},
		{h.ListDetections, "/api/detections?limit=5", "detections", []string{"charlie", "Bravo", "alpha"}, 3},
	} {
		code, got, e := list(t, c.handler, c.path, c.field)
		if code != http.StatusOK {
			t.Errorf("%s: %d %s", c.path, code, e)
			continue
		}
		if !reflect.DeepEqual(got.names, c.want) || got.total != c.total {
			t.Errorf("%s: %v total %d, want %v total %d", c.path, got.names, got.total, c.want, c.total)
		}
	}
}

// AM-API-16, AM-API-04: without the parameters each list is what it was.
func TestTheListsWithoutSortOrLimitAreUnchanged(t *testing.T) {
	h := orderedHandler(t)
	assets, _ := h.db.GetAssetsByUsersWithFilter([]int64{7}, nil)
	vulns, _ := h.db.GetVulnerabilitiesByUsersWithFilter([]int64{7}, nil)
	dets, _ := h.db.GetDetectionsByUsersWithFilter([]int64{7}, nil)
	for _, c := range []struct {
		handler     http.HandlerFunc
		path, field string
		want        interface{}
	}{
		{h.ListAssets, "/api/assets", "assets", assets},
		{h.ListVulnerabilities, "/api/vulnerabilities", "vulnerabilities", vulns},
		{h.ListDetections, "/api/detections", "detections", dets},
	} {
		w := httptest.NewRecorder()
		c.handler(w, callAs("GET", c.path, "", 7, false))
		var body map[string]json.RawMessage
		json.Unmarshal(w.Body.Bytes(), &body)
		want, _ := json.Marshal(c.want)
		if w.Code != http.StatusOK || string(body[c.field]) != string(want) {
			t.Errorf("%s: %d %s, want %s", c.path, w.Code, body[c.field], want)
		}
		var total int
		json.Unmarshal(body["total"], &total)
		if total != reflect.ValueOf(c.want).Len() {
			t.Errorf("%s: total %d", c.path, total)
		}
	}
	// The newest first, as before: by creation time.
	if _, got, _ := list(t, h.ListDetections, "/api/detections", "detections"); !reflect.DeepEqual(got.names, []string{"charlie", "Bravo", "alpha"}) {
		t.Errorf("detections without sort: %v", got.names)
	}
}

// AM-API-16, AM-API-02
func TestTheListsRefuseABadSortOrderOrLimit(t *testing.T) {
	h := orderedHandler(t)
	for _, c := range []struct {
		handler    http.HandlerFunc
		path, want string
	}{
		{h.ListAssets, "/api/assets?sort=risk", `sort must be one of severity, score, name, type, createdAt, modifiedAt, lastSeen, lastScanned, not "risk"`},
		{h.ListAssets, "/api/assets?sort=severity%20DESC%3B--", "sort must be one of"},
		{h.ListAssets, "/api/assets?order=desc", "order needs sort"},
		{h.ListAssets, "/api/assets?sort=name&order=down", "order must be asc or desc"},
		{h.ListAssets, "/api/assets?limit=0", "limit must be a whole number from 1 to 500"},
		{h.ListVulnerabilities, "/api/vulnerabilities?sort=score", "sort must be one of severity, name, host, createdAt, modifiedAt, lastSeen"},
		{h.ListVulnerabilities, "/api/vulnerabilities?limit=501", "limit must be a whole number from 1 to 500"},
		{h.ListDetections, "/api/detections?sort=severity", "sort must be one of name, host, createdAt, modifiedAt, lastSeen"},
		{h.ListDetections, "/api/detections?limit=many", "limit must be"},
	} {
		code, _, e := list(t, c.handler, c.path, "")
		if code != http.StatusBadRequest || !strings.Contains(e, c.want) {
			t.Errorf("%s: %d %q, want 400 with %q", c.path, code, e, c.want)
		}
	}
}
