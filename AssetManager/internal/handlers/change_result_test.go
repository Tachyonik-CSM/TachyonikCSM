// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the one answer of deletions and unlinkings (AM-API-13): every one of
// the twelve endpoints answers with deleted, updated and skipped and their
// ids, and the ids say what really happened — an asset a source still reports
// is skipped, not deleted, and one another source still reports is updated.

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

type answer struct {
	Deleted    int     `json:"deleted"`
	DeletedIDs []int64 `json:"deletedIds"`
	Updated    int     `json:"updated"`
	UpdatedIDs []int64 `json:"updatedIds"`
	Skipped    int     `json:"skipped"`
	SkippedIDs []int64 `json:"skippedIds"`
}

func answerOf(t *testing.T, what string, w *httptest.ResponseRecorder) answer {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", what, w.Code, w.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	for _, k := range []string{"deleted", "deletedIds", "updated", "updatedIds", "skipped", "skippedIds"} {
		if v, ok := raw[k]; !ok || string(v) == "null" {
			t.Errorf("%s: %q missing or null in %s", what, k, w.Body)
		}
	}
	var a answer
	json.Unmarshal(w.Body.Bytes(), &a)
	if a.Deleted != len(a.DeletedIDs) || a.Updated != len(a.UpdatedIDs) || a.Skipped != len(a.SkippedIDs) {
		t.Errorf("%s: counts and ids disagree: %s", what, w.Body)
	}
	return a
}

func TestEveryDeletionAnswersAlike(t *testing.T) {
	h := scopedHandler(t)
	source := "?source=" + url.QueryEscape("scan.xml (ID: 1)")
	stale := source + "&modifiedBefore=" + url.QueryEscape(time.Now().Add(time.Hour).Format(time.RFC3339))
	for _, c := range []struct {
		what    string
		handler http.HandlerFunc
		method  string
		path    string
		body    string
		service bool
	}{
		{"assets by id", h.DeleteAssetsByIDs, "DELETE", "/api/assets", `{"ids":[999]}`, false},
		{"assets by source", h.DeleteAssetsBySource, "DELETE", "/api/assets/by-source" + source, "", true},
		{"asset sources unlinked", h.ClearAssetSourcesBySource, "PATCH", "/api/assets/by-source" + source, "", true},
		{"stale assets", h.DeleteStaleAssetsBySource, "DELETE", "/api/assets/stale" + stale, "", true},
		{"vulnerabilities by id", h.DeleteVulnerabilitiesByIDs, "DELETE", "/api/vulnerabilities", `{"ids":[999]}`, false},
		{"vulnerabilities by source", h.DeleteVulnerabilitiesBySource, "DELETE", "/api/vulnerabilities/by-source" + source, "", true},
		{"vulnerability sources unlinked", h.ClearVulnerabilitySourcesBySource, "PATCH", "/api/vulnerabilities/by-source" + source, "", true},
		{"stale vulnerabilities", h.DeleteStaleVulnerabilitiesBySource, "DELETE", "/api/vulnerabilities/stale" + stale, "", true},
		{"detections by id", h.DeleteDetectionsByIDs, "DELETE", "/api/detections", `{"ids":[999]}`, false},
		{"detections by source", h.DeleteDetectionsBySource, "DELETE", "/api/detections/by-source" + source, "", true},
		{"detection sources unlinked", h.ClearDetectionSourcesBySource, "PATCH", "/api/detections/by-source" + source, "", true},
		{"stale detections", h.DeleteStaleDetectionsBySource, "DELETE", "/api/detections/stale" + stale, "", true},
	} {
		w := httptest.NewRecorder()
		user := int64(7)
		if c.service {
			user = 0
		}
		c.handler(w, callAs(c.method, c.path, c.body, user, c.service))
		answerOf(t, c.what, w)
	}
}

// The WebUI removes deletedIds from its list. An asset a source still
// reports must not be among them.
func TestDeletingAssetsByIDNamesOnlyWhatWasDeleted(t *testing.T) {
	h := scopedHandler(t) // user 7's 10.0.0.7, without a source
	var free int64
	if err := h.db.Get(&free, "SELECT id FROM assets WHERE name = '10.0.0.7'"); err != nil {
		t.Fatal(err)
	}
	linked, err := h.db.CreateAsset("10.0.0.8", "Host", 7, 1, "scan.xml", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string][]int64{"ids": {free, linked.Asset.ID}})
	w := httptest.NewRecorder()
	h.DeleteAssetsByIDs(w, callAs("DELETE", "/api/assets", string(body), 7, false))
	a := answerOf(t, "by id", w)
	if len(a.DeletedIDs) != 1 || a.DeletedIDs[0] != free {
		t.Errorf("deletedIds %v, want only %d", a.DeletedIDs, free)
	}
	if len(a.SkippedIDs) != 1 || a.SkippedIDs[0] != linked.Asset.ID {
		t.Errorf("skippedIds %v, want %d, which a source still reports", a.SkippedIDs, linked.Asset.ID)
	}
}

// Deleting a source's assets keeps an asset another source still reports,
// and the answer names it as updated.
func TestDeletingBySourceNamesTheAssetsThatRemain(t *testing.T) {
	h := scopedHandler(t)
	only, err := h.db.CreateAsset("10.0.0.10", "Host", 7, 1, "scan.xml", nil)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := h.db.CreateAsset("10.0.0.11", "Host", 7, 1, "scan.xml", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.CreateAsset("10.0.0.11", "Host", 7, 2, "other.xml", nil); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.DeleteAssetsBySource(w, callAs("DELETE", "/api/assets/by-source?source="+url.QueryEscape("scan.xml (ID: 1)"), "", 0, true))
	a := answerOf(t, "by source", w)
	if len(a.DeletedIDs) != 1 || a.DeletedIDs[0] != only.Asset.ID {
		t.Errorf("deletedIds %v, want %d", a.DeletedIDs, only.Asset.ID)
	}
	if len(a.UpdatedIDs) != 1 || a.UpdatedIDs[0] != shared.Asset.ID {
		t.Errorf("updatedIds %v, want %d, which other.xml still reports", a.UpdatedIDs, shared.Asset.ID)
	}
}
