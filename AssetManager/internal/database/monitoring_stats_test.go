// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the asset-monitoring buckets of the statistics: host assets by their
// scan coverage interval, where the 2- and 10-day boundaries fall, that a host
// without an interval is unmonitored, that only Host assets count — and that
// the interval filter the dashboard's "Show" applies finds the same hosts as
// each bucket.

package database

import "testing"

func TestMonitoringBucketsMatchFilter(t *testing.T) {
	db := newTestDB(t)
	for i, interval := range []int{1, 2, 3, 10, 11, 90} {
		name := "10.0.4." + string(rune('1'+i))
		res, err := db.CreateAsset(name, "Host", 7, 1, "a.xml", nil)
		if err != nil {
			t.Fatal(err)
		}
		iv := interval
		if err := db.SetAssetScanCoverage(res.Asset.ID, 1, "a.xml", nil, &iv); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"10.0.5.1", "10.0.5.2"} {
		if _, err := db.CreateAsset(name, "Host", 7, 1, "a.xml", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.CreateAsset("www.example.com", "Domain", 7, 1, "a.xml", nil); err != nil {
		t.Fatal(err)
	}

	stats, err := db.GetAssetStatsByUser(7)
	if err != nil {
		t.Fatal(err)
	}
	if stats.HostsMonitored2d != 2 || stats.HostsMonitored10d != 2 || stats.HostsMonitoredOlder != 2 || stats.HostsUnmonitored != 2 {
		t.Fatalf("monitoring buckets %d/%d/%d/%d, want 2/2/2/2",
			stats.HostsMonitored2d, stats.HostsMonitored10d, stats.HostsMonitoredOlder, stats.HostsUnmonitored)
	}
	bySet, err := db.GetAssetStatsByUsers([]int64{7})
	if err != nil {
		t.Fatal(err)
	}
	if *bySet != *stats {
		t.Errorf("user-set stats %+v differ from per-user %+v", *bySet, *stats)
	}

	host := AssetFilterRule{Column: "type", Operator: "=", Value: "Host"}
	interval := func(op, v string) AssetFilterRule {
		return AssetFilterRule{Column: "scanCoverageIntervalDays", Operator: op, Value: v}
	}
	for _, c := range []struct {
		name  string
		rules []AssetFilterRule
		want  int
	}{
		{"recent", []AssetFilterRule{host, interval("<=", "2")}, stats.HostsMonitored2d},
		{"middle", []AssetFilterRule{host, interval(">", "2"), interval("<=", "10")}, stats.HostsMonitored10d},
		{"older", []AssetFilterRule{host, interval(">", "10"), interval("<=", "99999")}, stats.HostsMonitoredOlder},
		{"unmonitored", []AssetFilterRule{host, interval(">", "99999")}, stats.HostsUnmonitored},
	} {
		got, err := db.CountAssetsByUserWithFilter(7, &AssetFilter{Rules: c.rules, MatchAll: true})
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s: filter finds %d, bucket counts %d", c.name, got, c.want)
		}
	}
}
