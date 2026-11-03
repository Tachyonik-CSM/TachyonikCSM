// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins what a re-import drops: the asset links it did not touch, and only
// those. A link used to count as stale when its last seen — the date the file
// gives — was before the re-import started, so re-importing any file dated in
// the past deleted every asset it listed while its findings stayed.

package database

import (
	"testing"
	"time"
)

func countAssets(t *testing.T, db *DB, name string) int {
	t.Helper()
	var n int
	if err := db.Get(&n, "SELECT COUNT(*) FROM assets WHERE name = ?", name); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAReimportOfTheSameFileKeepsItsAssets(t *testing.T) {
	db := newTestDB(t)
	april := time.Date(2026, 4, 17, 16, 0, 0, 0, time.UTC)
	importFile := func(hosts ...string) {
		for _, h := range hosts {
			if _, err := db.CreateAsset(h, "Host", 7, 5, "report.xml", &april); err != nil {
				t.Fatal(err)
			}
		}
	}
	importFile("10.0.0.1", "10.0.0.2")

	start := time.Now()
	time.Sleep(5 * time.Millisecond)
	importFile("10.0.0.1") // the newer importer no longer finds 10.0.0.2
	deleted, _, err := db.DeleteStaleAssetsBySource(5, 0, start)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || countAssets(t, db, "10.0.0.1") != 1 || countAssets(t, db, "10.0.0.2") != 0 {
		t.Errorf("deleted %v; want only 10.0.0.2 gone and 10.0.0.1 kept", deleted)
	}
	if ls := lastSeenOf(t, db, "10.0.0.1"); !ls.Equal(april) {
		t.Errorf("last seen %v, want the file's %v", ls, april)
	}
}

// A link written before migration v5 has modified_at filled from created_at;
// one an older binary wrote has none, and counts by created_at.
func TestALinkWithoutModifiedAtCountsByCreatedAt(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.CreateAsset("10.0.0.1", "Host", 7, 5, "report.xml", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE asset_source SET modified_at = NULL, created_at = ?", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	deleted, _, err := db.DeleteStaleAssetsBySource(5, 0, time.Now())
	if err != nil || len(deleted) != 0 {
		t.Errorf("deleted %v (%v); a link created after the cut-off is not stale", deleted, err)
	}
}

// When the source that covers a host also lists it, the link becomes a
// sighting dated by the listing, not by when the coverage was applied.
func TestListingAHostMakesItsCoverageLinkASighting(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.ApplyScanCoverage(7, 5, "both.txt", []CoverageEntry{{Target: "10.0.0.1", IntervalDays: 7}}, nil); err != nil {
		t.Fatal(err)
	}
	listed := time.Now().UTC().Truncate(time.Second).AddDate(0, 0, -3)
	if _, err := db.CreateAsset("10.0.0.1", "Host", 7, 5, "both.txt", &listed); err != nil {
		t.Fatal(err)
	}
	var link struct {
		Counts   int       `db:"counts_as_seen"`
		LastSeen time.Time `db:"last_seen"`
	}
	if err := db.Get(&link, "SELECT counts_as_seen, last_seen FROM asset_source WHERE source_id = 5"); err != nil {
		t.Fatal(err)
	}
	if link.Counts != 1 || !link.LastSeen.Equal(listed) {
		t.Errorf("link = %+v, want a sighting dated %v", link, listed)
	}
	if ls := lastSeenOf(t, db, "10.0.0.1"); !ls.Equal(listed) {
		t.Errorf("asset last seen %v, want %v", ls, listed)
	}
}
