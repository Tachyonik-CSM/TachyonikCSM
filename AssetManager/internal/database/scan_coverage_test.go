// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the scan coverage of an asset: that it holds exactly what imports set
// (a source's newer report replaces its older one, a report leaving a value
// out keeps it), that several sources combine to the newest verification and
// the shortest interval, that a removed source takes its coverage with it,
// that findings never touch it, and how the two filters treat an asset
// without coverage — verified very long ago, at a very long interval.

package database

import (
	"sort"
	"strings"
	"testing"
	"time"
)

func coverage(t *testing.T, db *DB, name string) (*time.Time, *int) {
	t.Helper()
	var row struct {
		Verified *time.Time `db:"last_scan_coverage_verification"`
		Interval *int       `db:"scan_coverage_interval_days"`
	}
	if err := db.Get(&row, "SELECT last_scan_coverage_verification, scan_coverage_interval_days FROM assets WHERE user_id = 7 AND name = ?", name); err != nil {
		t.Fatal(err)
	}
	return row.Verified, row.Interval
}

func TestScanCoverage(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	ago := func(d int) *time.Time { v := now.AddDate(0, 0, -d); return &v }
	ival := func(d int) *int { return &d }

	// Source 1 reports: verified 5 days ago, weekly.
	res, err := db.CreateAsset("10.0.0.1", "Host", 7, 1, "a.xml", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := res.Asset.ID
	if v, i := coverage(t, db, "10.0.0.1"); v != nil || i != nil {
		t.Fatalf("before any report: %v / %v, want NULL / NULL", v, i)
	}
	if err := db.SetAssetScanCoverage(id, 1, "a.xml", ago(5), ival(7)); err != nil {
		t.Fatal(err)
	}
	if v, i := coverage(t, db, "10.0.0.1"); v == nil || !v.Equal(*ago(5)) || i == nil || *i != 7 {
		t.Fatalf("one source: %v / %v, want %v / 7", v, i, ago(5))
	}

	// Its next report changes the interval only: the verification stays, and
	// the interval may grow — the newer report replaces the older one.
	if err := db.SetAssetScanCoverage(id, 1, "a.xml", nil, ival(30)); err != nil {
		t.Fatal(err)
	}
	if v, i := coverage(t, db, "10.0.0.1"); v == nil || !v.Equal(*ago(5)) || i == nil || *i != 30 {
		t.Fatalf("interval re-reported: %v / %v, want %v / 30", v, i, ago(5))
	}

	// Source 2: verified more recently, scanned less often. Combined: its
	// verification, source 1's interval.
	if _, err := db.CreateAsset("10.0.0.1", "Host", 7, 2, "b.xml", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAssetScanCoverage(id, 2, "b.xml", ago(1), ival(90)); err != nil {
		t.Fatal(err)
	}
	if v, i := coverage(t, db, "10.0.0.1"); v == nil || !v.Equal(*ago(1)) || i == nil || *i != 30 {
		t.Fatalf("two sources: %v / %v, want %v / 30", v, i, ago(1))
	}

	// Findings leave it alone.
	if _, err := db.CreateVulnerability("CVE-1", "10.0.0.1", "443", 50, "", 7, ago(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RefreshAssetsForHosts([]HostRef{{Host: "10.0.0.1", UserID: 7}}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateAllAssetLastScanned(); err != nil {
		t.Fatal(err)
	}
	if v, i := coverage(t, db, "10.0.0.1"); v == nil || !v.Equal(*ago(1)) || i == nil || *i != 30 {
		t.Fatalf("after findings: %v / %v, want %v / 30", v, i, ago(1))
	}

	// Source 1 goes: only source 2's report is left.
	if _, _, err := db.DeleteAssetsBySource(1, 7); err != nil {
		t.Fatal(err)
	}
	if v, i := coverage(t, db, "10.0.0.1"); v == nil || !v.Equal(*ago(1)) || i == nil || *i != 90 {
		t.Fatalf("after source 1 removed: %v / %v, want %v / 90", v, i, ago(1))
	}

	// A second host without any coverage, for the filters.
	if _, err := db.CreateAsset("10.0.0.2", "Host", 7, 2, "b.xml", nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		column, op, value string
		want              []string
	}{
		{"coverageVerifiedDays", "<=", "2", []string{"10.0.0.1"}},
		{"coverageVerifiedDays", ">", "2", []string{"10.0.0.2"}},
		{"scanCoverageIntervalDays", "<=", "90", []string{"10.0.0.1"}},
		// No interval counts as a very long one, as never scanned does for
		// lastScannedDays: "> 99999" is the unmonitored hosts.
		{"scanCoverageIntervalDays", ">", "7", []string{"10.0.0.1", "10.0.0.2"}},
		{"scanCoverageIntervalDays", "<=", "99999", []string{"10.0.0.1"}},
		{"scanCoverageIntervalDays", ">", "99999", []string{"10.0.0.2"}},
		{"scanCoverageIntervalDays", "<", "7", nil},
	} {
		got, err := db.GetAssetsByUsersWithFilter([]int64{7}, &AssetFilter{MatchAll: true, Rules: []AssetFilterRule{
			{Column: tc.column, Operator: tc.op, Value: tc.value},
		}})
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, a := range got {
			names = append(names, a.Name)
		}
		sort.Strings(names)
		if strings.Join(names, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s %s %s: got %v, want %v", tc.column, tc.op, tc.value, names, tc.want)
		}
	}

	// The last source goes with its asset's coverage: cleared, not kept.
	if _, err := db.ClearAssetSourceBySource(2, 7); err != nil {
		t.Fatal(err)
	}
	if v, i := coverage(t, db, "10.0.0.1"); v != nil || i != nil {
		t.Fatalf("after all sources cleared: %v / %v, want NULL / NULL", v, i)
	}
}
