// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins how a scan coverage table is applied: an address covers its host asset
// and creates it when missing, a network or range covers the known hosts in
// it, a hostname is skipped, the shortest interval wins, and — the point of
// counts_as_seen — none of it moves an asset's last seen. Also that a
// re-import drops what the table no longer lists.

package database

import (
	"testing"
	"time"
)

func lastSeenOf(t *testing.T, db *DB, name string) time.Time {
	t.Helper()
	var ls time.Time
	if err := db.Get(&ls, "SELECT last_seen FROM assets WHERE user_id = 7 AND name = ?", name); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return ls
}

func TestApplyingACoverageTable(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	tenDaysAgo := now.AddDate(0, 0, -10)
	for _, name := range []string{"10.0.0.1", "10.0.0.5", "192.168.9.9"} {
		if _, err := db.CreateAsset(name, "Host", 7, 1, "hosts.xml", &tenDaysAgo); err != nil {
			t.Fatal(err)
		}
	}
	verified := now.Add(-time.Hour)
	entries := []CoverageEntry{
		{Target: "10.0.0.0/24", IntervalDays: 14, VerifiedAt: &verified},
		{Target: "10.0.0.1", IntervalDays: 7, VerifiedAt: &verified},
		{Target: "10.0.0.9", IntervalDays: 3, VerifiedAt: &verified},
		{Target: "10.1.0.0-10.1.0.9", IntervalDays: 5, VerifiedAt: &verified},
		{Target: "scanner.lab", IntervalDays: 1, VerifiedAt: &verified},
		{Target: "10.0.0.7", IntervalDays: 0, VerifiedAt: &verified},
		{Target: "not|a target", IntervalDays: 2, VerifiedAt: &verified},
	}
	out, err := db.ApplyScanCoverage(7, 2, "coverage.txt", entries, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Covered) != 3 || len(out.Created) != 1 || out.Hostnames != 1 || out.Unmatched != 1 || out.Invalid != 2 {
		t.Errorf("outcome = %+v", out)
	}

	for name, want := range map[string]int{"10.0.0.1": 7, "10.0.0.5": 14, "10.0.0.9": 3} {
		v, i := coverage(t, db, name)
		if i == nil || *i != want || v == nil || !v.Equal(verified) {
			t.Errorf("%s: %v / %v, want %v / %d", name, v, i, verified, want)
		}
	}
	if _, i := coverage(t, db, "192.168.9.9"); i != nil {
		t.Errorf("a host outside every target got interval %d", *i)
	}
	// Being in a schedule is not being seen.
	for _, name := range []string{"10.0.0.1", "10.0.0.5"} {
		if ls := lastSeenOf(t, db, name); !ls.Equal(tenDaysAgo) {
			t.Errorf("%s: last seen moved to %v, want %v", name, ls, tenDaysAgo)
		}
	}
	// The created one is a Host with the table's date as its last seen.
	var created struct {
		Type     string    `db:"type"`
		LastSeen time.Time `db:"last_seen"`
	}
	if err := db.Get(&created, "SELECT type, last_seen FROM assets WHERE user_id = 7 AND name = '10.0.0.9'"); err != nil {
		t.Fatal(err)
	}
	if created.Type != "Host" || !created.LastSeen.Equal(verified) {
		t.Errorf("created asset = %+v", created)
	}
	// Recomputing does not pull it forward to the coverage link's date.
	if _, err := db.RefreshAllAssets(); err != nil {
		t.Fatal(err)
	}
	if ls := lastSeenOf(t, db, "10.0.0.9"); !ls.Equal(verified) {
		t.Errorf("after a refresh the created asset's last seen is %v, want %v", ls, verified)
	}
}

func TestALimitStopsCreatingButNotCovering(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.CreateAsset("10.0.0.1", "Host", 7, 1, "hosts.xml", nil); err != nil {
		t.Fatal(err)
	}
	out, err := db.ApplyScanCoverage(7, 2, "coverage.txt", []CoverageEntry{
		{Target: "10.0.0.1", IntervalDays: 7}, {Target: "10.0.0.2", IntervalDays: 7},
	}, func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Covered) != 1 || len(out.Created) != 0 || out.Refused != 1 {
		t.Errorf("outcome = %+v", out)
	}
}

// A source that lists a host and states its coverage keeps its link a
// sighting; the coverage only adds to it.
func TestTheSameSourcesSightingStaysASighting(t *testing.T) {
	db := newTestDB(t)
	twoDaysAgo := time.Now().UTC().Truncate(time.Second).AddDate(0, 0, -2)
	if _, err := db.CreateAsset("10.0.0.1", "Host", 7, 3, "both.txt", &twoDaysAgo); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ApplyScanCoverage(7, 3, "both.txt", []CoverageEntry{{Target: "10.0.0.1", IntervalDays: 2}}, nil); err != nil {
		t.Fatal(err)
	}
	var link struct {
		Counts   int       `db:"counts_as_seen"`
		LastSeen time.Time `db:"last_seen"`
	}
	if err := db.Get(&link, "SELECT counts_as_seen, last_seen FROM asset_source WHERE source_id = 3"); err != nil {
		t.Fatal(err)
	}
	if link.Counts != 1 || !link.LastSeen.Equal(twoDaysAgo) {
		t.Errorf("link = %+v, want a sighting dated %v", link, twoDaysAgo)
	}
	if _, i := coverage(t, db, "10.0.0.1"); i == nil || *i != 2 {
		t.Errorf("interval = %v, want 2", i)
	}
}

// A re-import compares each link's date with its own start; a coverage link
// is dated when it was applied, so what the table still lists survives and
// what it no longer lists goes — with the asset, if the table made it.
func TestAReimportDropsWhatTheTableNoLongerLists(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.CreateAsset("10.0.0.1", "Host", 7, 1, "hosts.xml", nil); err != nil {
		t.Fatal(err)
	}
	table := []CoverageEntry{{Target: "10.0.0.1", IntervalDays: 7}, {Target: "10.0.0.9", IntervalDays: 3}}
	if _, err := db.ApplyScanCoverage(7, 2, "coverage.txt", table, nil); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	time.Sleep(5 * time.Millisecond)
	if _, err := db.ApplyScanCoverage(7, 2, "coverage.txt", table[:1], nil); err != nil {
		t.Fatal(err)
	}
	deleted, _, err := db.DeleteStaleAssetsBySource(2, 0, start)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 {
		t.Errorf("deleted %v, want the asset only the old table named", deleted)
	}
	if _, i := coverage(t, db, "10.0.0.1"); i == nil || *i != 7 {
		t.Errorf("the host the table still lists lost its coverage: %v", i)
	}
	var n int
	if err := db.Get(&n, "SELECT COUNT(*) FROM assets WHERE name = '10.0.0.9'"); err != nil || n != 0 {
		t.Errorf("10.0.0.9 is still there (%d, %v)", n, err)
	}
}
