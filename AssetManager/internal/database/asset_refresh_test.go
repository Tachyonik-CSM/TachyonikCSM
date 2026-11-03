// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the recompute of an asset from what still refers to it: that last seen
// is the newest of its sources, vulnerabilities and detections, that severity
// and score follow the vulnerabilities left, and that removing one of two
// sources moves all of them back to what the other source says.

package database

import (
	"testing"
	"time"
)

func assetNamed(t *testing.T, db *DB, name string) Asset {
	t.Helper()
	var a Asset
	if err := db.Get(&a, "SELECT "+assetColumns+" FROM assets WHERE user_id = 7 AND name = ? AND type = 'Host'", name); err != nil {
		t.Fatalf("asset %s: %v", name, err)
	}
	return a
}

func TestAnAssetFollowsTheSourcesItKeeps(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	ago := func(d int) *time.Time { v := now.AddDate(0, 0, -d); return &v }
	const host = "10.0.0.1"

	// Source 1 lists the host 2 days ago and found a severe vulnerability 1 day
	// ago; source 2 lists it 30 days ago and found a milder one 40 days ago.
	if _, err := db.CreateAsset(host, "Host", 7, 1, "s1.xml", ago(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateAsset(host, "Host", 7, 2, "s2.xml", ago(30)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateVulnerability("CVE-1", host, "443", 70, "s1.xml (ID: 1)", 7, ago(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateVulnerability("CVE-2", host, "22", 30, "s2.xml (ID: 2)", 7, ago(40)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RefreshAssetsForHosts([]HostRef{{Host: host, UserID: 7}}); err != nil {
		t.Fatal(err)
	}
	a := assetNamed(t, db, host)
	if a.Severity != 70 || a.Score != 30 || !a.LastSeen.Equal(*ago(1)) {
		t.Fatalf("with both sources: severity %d score %d last seen %v, want 70, 30, %v", a.Severity, a.Score, a.LastSeen, ago(1))
	}

	// Source 1 is deleted, as ResourceManager does it: its vulnerabilities,
	// then its link, both on behalf of every user (userID 0).
	refs, err := db.VulnerabilityHostRefsBySource("s1.xml (ID: 1)", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DeleteVulnerabilitiesBySource("s1.xml (ID: 1)", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RefreshAssetsForHosts(refs); err != nil {
		t.Fatal(err)
	}
	deleted, refreshed, err := db.DeleteAssetsBySource(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 0 {
		t.Fatalf("deleted %v: the host still has source 2", deleted)
	}
	a = assetNamed(t, db, host)
	// Source 2's listing (30 days) is newer than its vulnerability (40 days).
	if a.Severity != 30 || a.Score != 70 || !a.LastSeen.Equal(*ago(30)) {
		t.Errorf("after deleting source 1: severity %d score %d last seen %v, want 30, 70, %v", a.Severity, a.Score, a.LastSeen, ago(30))
	}
	if a.LastScanned == nil || !a.LastScanned.Equal(*ago(40)) {
		t.Errorf("last scanned %v, want %v", a.LastScanned, ago(40))
	}
	if len(refreshed) != 1 {
		t.Errorf("refreshed %v, want the host: losing source 1's listing moved its last seen", refreshed)
	}

	// Source 2's findings go too (a re-import that no longer has them): the
	// host is unmanaged, and its last seen falls back to the listing.
	if _, err := db.DeleteVulnerabilitiesBySource("s2.xml (ID: 2)", 7); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RefreshAssetsForHosts([]HostRef{{Host: host, UserID: 7}}); err != nil {
		t.Fatal(err)
	}
	a = assetNamed(t, db, host)
	if a.Severity != 0 || a.Score != 0 || !a.LastSeen.Equal(*ago(30)) || a.LastScanned != nil {
		t.Errorf("without findings: severity %d score %d last seen %v last scanned %v, want 0, 0, %v, none",
			a.Severity, a.Score, a.LastSeen, a.LastScanned, ago(30))
	}
}

// A listing older than what the asset already shows does not lower it, and a
// newer finding raises it: last seen is the newest evidence, whichever kind.
func TestLastSeenIsTheNewestEvidence(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	ago := func(d int) *time.Time { v := now.AddDate(0, 0, -d); return &v }

	if _, err := db.CreateAsset("10.0.0.2", "Host", 7, 1, "s1.xml", ago(5)); err != nil {
		t.Fatal(err)
	}
	res, err := db.CreateAsset("10.0.0.2", "Host", 7, 2, "s2.xml", ago(50))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Asset.LastSeen.Equal(*ago(5)) {
		t.Errorf("after an older listing: %v, want %v", res.Asset.LastSeen, ago(5))
	}
	if _, err := db.CreateDetection("ssh", "10.0.0.2", "22", "s3.xml (ID: 3)", 7, ago(1)); err != nil {
		t.Fatal(err)
	}
	changed, err := db.RefreshAssetsForHosts([]HostRef{{Host: "10.0.0.2", UserID: 7}})
	if err != nil {
		t.Fatal(err)
	}
	if a := assetNamed(t, db, "10.0.0.2"); len(changed) != 1 || !a.LastSeen.Equal(*ago(1)) || a.Score != 100 {
		t.Errorf("after a newer detection: changed %v, last seen %v score %d, want the asset, %v, 100", changed, a.LastSeen, a.Score, ago(1))
	}
	// Nothing new: nothing reported as changed.
	if changed, _ := db.RefreshAssetsForHosts([]HostRef{{Host: "10.0.0.2", UserID: 7}}); len(changed) != 0 {
		t.Errorf("a refresh without changes reported %v", changed)
	}
}

// What an older version left behind is put right by the startup refresh.
func TestRefreshAllAssetsRepairsStaleValues(t *testing.T) {
	db := newTestDB(t)
	old := time.Now().UTC().AddDate(0, 0, -9).Truncate(time.Second)
	if _, err := db.CreateAsset("10.0.0.3", "Host", 7, 1, "s1.xml", &old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE assets SET last_seen = ?, severity = 90, score = 10 WHERE name = '10.0.0.3'", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RefreshAllAssets(); err != nil {
		t.Fatal(err)
	}
	if a := assetNamed(t, db, "10.0.0.3"); !a.LastSeen.Equal(old) || a.Severity != 0 || a.Score != 0 {
		t.Errorf("after the refresh: last seen %v severity %d score %d, want %v, 0, 0", a.LastSeen, a.Severity, a.Score, old)
	}
}
