// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins AM-DATA-48: a host counts as fully covered only when it is current in
// all three host views at once — seen at most 30 days ago, scanned at most 2
// days ago, scanned every 1 or 2 days — at the same boundaries as the views'
// own counts, and only Host assets count.

package database

import (
	"testing"
	"time"
)

func TestFullyCoveredHostsAreCurrentInAllThreeViews(t *testing.T) {
	db := newTestDB(t)
	day := 24 * time.Hour
	for _, c := range []struct {
		name             string
		seenAgo, scanAgo time.Duration
		interval         int // 0: none
		fullyCovered     bool
	}{
		{"10.0.7.1", 1 * day, 1 * day, 1, true},
		{"10.0.7.2", 30*day - time.Hour, 2*day - time.Hour, 2, true}, // the boundaries
		{"10.0.7.3", 31 * day, 1 * day, 1, false},                    // seen too long ago
		{"10.0.7.4", 1 * day, 3 * day, 1, false},                     // scanned too long ago
		{"10.0.7.5", 1 * day, 1 * day, 3, false},                     // scanned too rarely
		{"10.0.7.6", 1 * day, 1 * day, 0, false},                     // not monitored
	} {
		res, err := db.CreateAsset(c.name, "Host", 7, 1, "a.xml", nil)
		if err != nil {
			t.Fatal(err)
		}
		if c.interval > 0 {
			iv := c.interval
			if err := db.SetAssetScanCoverage(res.Asset.ID, 1, "a.xml", nil, &iv); err != nil {
				t.Fatal(err)
			}
		}
		now := time.Now()
		if _, err := db.Exec(`UPDATE assets SET last_seen = ?, last_scanned = ? WHERE id = ?`,
			now.Add(-c.seenAgo), now.Add(-c.scanAgo), res.Asset.ID); err != nil {
			t.Fatal(err)
		}
	}
	// Not a host: never counted.
	if _, err := insertLegacyAsset(db, "www.example.com", "Domain", 7, nil); err != nil {
		t.Fatal(err)
	}

	stats, err := db.GetAssetStatsByUser(7)
	if err != nil {
		t.Fatal(err)
	}
	if stats.HostsFullyCovered != 2 {
		t.Errorf("fully covered %d, want 2 (the two current in all three)", stats.HostsFullyCovered)
	}
	// Never more than any view's own current count.
	for _, n := range []int{stats.HostsSeen30d, stats.HostsScanned2d, stats.HostsMonitored2d} {
		if stats.HostsFullyCovered > n {
			t.Errorf("fully covered %d exceeds a view's current count %d", stats.HostsFullyCovered, n)
		}
	}
}
