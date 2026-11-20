// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for paging the asset, vulnerability and detection lists (AM-API-16,
// docs/plans/assetmanager-list-pagination.md): pages that neither overlap nor
// skip for every sort field in both directions and the default order, with
// equal values among the records; the total on every page; offset refused
// without a limit or when it is no whole number; the port and source order;
// and "contains" matching % and _ as written.

package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"tachyonik/assetmanager/internal/database"
	"tachyonik/lib/listquery"
)

// pagedHandler holds, for user 7, seven assets, vulnerabilities and detections
// with repeated values: two created at the same moment, equal severities,
// ports and labels.
func pagedHandler(t *testing.T) *Handler {
	t.Helper()
	h := scopedHandler(t) // brings asset 10.0.0.7
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i, c := range []struct {
		host, port, label string
		sev               int
	}{
		{"h1", "80/tcp", "b.xml", 50}, {"h2", "22/tcp", "a.xml", 50}, {"h3", "443/tcp", "B.xml", 90},
		{"h4", "general/tcp", "a.xml", 10}, {"h5", "80/tcp", "c.xml", 50}, {"h6", "general/icmp", "a.xml", 0},
	} {
		created := base.Add(time.Duration(i/2) * time.Hour) // pairs share a moment
		res, err := h.db.CreateAsset(c.host, "Host", 7, int64(i+1), c.label, &created)
		if err != nil {
			t.Fatal(err)
		}
		v, err := h.db.CreateVulnerability("check", c.host, c.port, c.sev, int64(i+1), c.label, 7, &created)
		if err != nil {
			t.Fatal(err)
		}
		d, err := h.db.CreateDetection("service", c.host, c.port, int64(i+1), c.label, 7, &created)
		if err != nil {
			t.Fatal(err)
		}
		for table, id := range map[string]int64{"assets": res.Asset.ID, "vulnerabilities": v.ID, "detections": d.ID} {
			if _, err := h.db.Exec("UPDATE "+table+" SET created_at = ? WHERE id = ?", created, id); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := h.db.Exec("UPDATE assets SET severity = ? WHERE id = ?", c.sev, res.Asset.ID); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

type page struct {
	ids   []int64
	ports []string
	total int
}

func getPage(t *testing.T, handler http.HandlerFunc, path, field string) (int, page) {
	t.Helper()
	w := httptest.NewRecorder()
	handler(w, callAs("GET", path, "", 7, false))
	if w.Code != http.StatusOK {
		return w.Code, page{}
	}
	var body map[string]json.RawMessage
	json.Unmarshal(w.Body.Bytes(), &body)
	var rows []struct {
		ID   int64  `json:"id"`
		Port string `json:"port"`
	}
	json.Unmarshal(body[field], &rows)
	var p page
	for _, r := range rows {
		p.ids = append(p.ids, r.ID)
		p.ports = append(p.ports, r.Port)
	}
	json.Unmarshal(body["total"], &p.total)
	return w.Code, p
}

func TestTheListsPageWithoutOverlapOrGap(t *testing.T) {
	h := pagedHandler(t)
	for _, l := range []struct {
		path, field string
		handler     http.HandlerFunc
		fields      []string
	}{
		{"/api/assets", "assets", h.ListAssets, listquery.Names(database.AssetSortFields)},
		{"/api/vulnerabilities", "vulnerabilities", h.ListVulnerabilities, listquery.Names(database.VulnerabilitySortFields)},
		{"/api/detections", "detections", h.ListDetections, listquery.Names(database.DetectionSortFields)},
	} {
		queries := []string{""} // the default order
		for _, f := range l.fields {
			queries = append(queries, "sort="+f+"&order=asc&", "sort="+f+"&order=desc&")
		}
		for _, q := range queries {
			_, whole := getPage(t, l.handler, l.path+"?"+q+"limit=500", l.field)
			var paged []int64
			for offset := 0; offset < whole.total+2; offset += 2 {
				code, p := getPage(t, l.handler, fmt.Sprintf("%s?%slimit=2&offset=%d", l.path, q, offset), l.field)
				if code != http.StatusOK {
					t.Fatalf("%s?%s offset %d: %d", l.path, q, offset, code)
				}
				if p.total != whole.total {
					t.Errorf("%s?%s offset %d: total %d, want %d", l.path, q, offset, p.total, whole.total)
				}
				paged = append(paged, p.ids...)
			}
			if !reflect.DeepEqual(paged, whole.ids) {
				t.Errorf("%s?%s: pages %v, the whole list %v", l.path, q, paged, whole.ids)
			}
		}
	}
}

func TestOffsetNeedsALimitAndAWholeNumber(t *testing.T) {
	h := pagedHandler(t)
	for _, path := range []string{"/api/assets?offset=2", "/api/vulnerabilities?limit=2&offset=-1", "/api/detections?limit=2&offset=two"} {
		handler := h.ListAssets
		switch {
		case path[5] == 'v':
			handler = h.ListVulnerabilities
		case path[5] == 'd':
			handler = h.ListDetections
		}
		if code, _ := getPage(t, handler, path, ""); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", path, code)
		}
	}
	if code, p := getPage(t, h.ListAssets, "/api/assets?limit=2&offset=0", "assets"); code != http.StatusOK || len(p.ids) != 2 {
		t.Errorf("offset 0: %d %v", code, p.ids)
	}
}

func TestPortsSortByTheirNumberThenAsText(t *testing.T) {
	h := pagedHandler(t)
	_, p := getPage(t, h.ListDetections, "/api/detections?sort=port", "detections")
	want := []string{"22/tcp", "80/tcp", "80/tcp", "443/tcp", "general/icmp", "general/tcp"}
	if !reflect.DeepEqual(p.ports, want) {
		t.Errorf("by port: %v, want %v", p.ports, want)
	}
}

// "contains" matches what it is given: % and _ are no wildcards.
func TestContainsMatchesWildcardsLiterally(t *testing.T) {
	h := scopedHandler(t)
	for _, name := range []string{"a_b", "axb", "50%", "500"} {
		if _, err := h.db.CreateAsset(name, "Host", 7, 1, "f.xml", nil); err != nil {
			t.Fatal(err)
		}
	}
	for value, want := range map[string]int{"a_b": 1, "50%": 1, "A_B": 1} {
		f := url.QueryEscape(`{"rules":[{"column":"name","operator":"contains","value":` + fmt.Sprintf("%q", value) + `}],"matchAll":true}`)
		if _, p := getPage(t, h.ListAssets, "/api/assets?filter="+f, "assets"); p.total != want {
			t.Errorf("name contains %q: %d assets, want %d", value, p.total, want)
		}
	}
}
