// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that deleting a source's findings on behalf of a service — as
// ResourceManager does when a source is deleted — recomputes the hosts they
// belonged to. It ran "for user 0" and matched no asset, so a host kept the
// severity, score and last seen of findings that no longer existed.

package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDeletingASourcesFindingsAsAServiceRecomputesTheirHosts(t *testing.T) {
	h := scopedHandler(t) // has host 10.0.0.7 of user 7, no source
	now := time.Now().UTC().Truncate(time.Second)
	day := func(d int) *time.Time { v := now.AddDate(0, 0, -d); return &v }
	if _, err := h.db.CreateAsset("10.0.0.7", "Host", 7, 2, "s2.xml", day(20)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.CreateVulnerability("CVE-1", "10.0.0.7", "443", 80, "s1.xml (ID: 1)", 7, day(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.CreateDetection("ssh", "10.0.0.7", "22", "s1.xml (ID: 1)", 7, day(1)); err != nil {
		t.Fatal(err)
	}
	h.refreshHosts(nil) // nothing to do, must not fail
	if _, err := h.db.RefreshAllAssets(); err != nil {
		t.Fatal(err)
	}

	for _, del := range []func(http.ResponseWriter, *http.Request){h.DeleteVulnerabilitiesBySource, h.DeleteDetectionsBySource} {
		w := httptest.NewRecorder()
		del(w, callAs("DELETE", "/api/x/by-source?source=s1.xml+%28ID%3A+1%29", "", 0, true))
		if w.Code != http.StatusOK {
			t.Fatalf("delete: %d %s", w.Code, w.Body.String())
		}
	}

	var got struct {
		Severity, Score int
		LastSeen        time.Time `db:"last_seen"`
	}
	if err := h.db.Get(&got, "SELECT severity, score, last_seen FROM assets WHERE name = '10.0.0.7' AND user_id = 7"); err != nil {
		t.Fatal(err)
	}
	if got.Severity != 0 || got.Score != 0 || !got.LastSeen.Equal(*day(20)) {
		t.Errorf("severity %d score %d last seen %v, want 0, 0 and the remaining listing's %v", got.Severity, got.Score, got.LastSeen, day(20))
	}
}
