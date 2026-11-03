// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the host-asset age buckets of the asset statistics: where the 30- and
// 180-day boundaries fall, that only Host assets count, that last_seen written
// in another layout is still read as a time, and that the per-user and the
// user-set variants agree — and that the last-seen filter the dashboard
// applies finds the same hosts as each bucket.

package database

import (
	"testing"
	"time"
)

func TestHostAgeBuckets(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	days := func(d float64) *time.Time {
		v := now.Add(-time.Duration(d * 24 * float64(time.Hour)))
		return &v
	}
	for name, seen := range map[string]*time.Time{
		"10.0.0.1": days(0),
		"10.0.0.2": days(29.9),
		"10.0.0.3": days(30.1),
		"10.0.0.4": days(179.9),
		"10.0.0.5": days(180.1),
		"10.0.0.6": days(400),
	} {
		if _, err := db.CreateAsset(name, "Host", 7, 0, "", seen); err != nil {
			t.Fatal(err)
		}
	}
	// Not a host: counted in the total, not in the buckets.
	if _, err := insertLegacyAsset(db, "www.example.com", "Domain", 7, days(1)); err != nil {
		t.Fatal(err)
	}
	// Another layout of last_seen ("T", no offset), 10 days ago.
	if _, err := db.Exec(`INSERT INTO assets (user_id, name, type, last_seen) VALUES (7, '10.0.0.7', 'Host', ?)`,
		now.UTC().AddDate(0, 0, -10).Format("2006-01-02T15:04:05")); err != nil {
		t.Fatal(err)
	}
	// Someone else's host.
	if _, err := db.CreateAsset("10.0.0.8", "Host", 8, 0, "", days(1)); err != nil {
		t.Fatal(err)
	}

	byUser, err := db.GetAssetStatsByUser(7)
	if err != nil {
		t.Fatal(err)
	}
	if byUser.HostsSeen30d != 3 || byUser.HostsSeen180d != 2 || byUser.HostsSeenOlder != 2 {
		t.Errorf("buckets %d/%d/%d, want 3/2/2", byUser.HostsSeen30d, byUser.HostsSeen180d, byUser.HostsSeenOlder)
	}
	if byUser.Total != 8 {
		t.Errorf("total %d, want 8", byUser.Total)
	}

	bySet, err := db.GetAssetStatsByUsers([]int64{7})
	if err != nil {
		t.Fatal(err)
	}
	if *bySet != *byUser {
		t.Errorf("user-set stats %+v differ from per-user %+v", *bySet, *byUser)
	}

	both, err := db.GetAssetStatsByUsers([]int64{7, 8})
	if err != nil {
		t.Fatal(err)
	}
	if both.HostsSeen30d != 4 {
		t.Errorf("two users: %d recent hosts, want 4", both.HostsSeen30d)
	}
}

// The filter the dashboard's "Show" applies selects exactly the hosts each
// age bucket counts.
func TestLastSeenFilterMatchesBuckets(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i, d := range []float64{1, 29.9, 30.1, 120, 179.9, 180.1, 365} {
		seen := now.Add(-time.Duration(d * 24 * float64(time.Hour)))
		if _, err := db.CreateAsset("10.0.1."+string(rune('1'+i)), "Host", 7, 0, "", &seen); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := insertLegacyAsset(db, "www.example.com", "Domain", 7, &now); err != nil {
		t.Fatal(err)
	}
	stats, err := db.GetAssetStatsByUser(7)
	if err != nil {
		t.Fatal(err)
	}

	host := AssetFilterRule{Column: "type", Operator: "=", Value: "host"}
	for _, c := range []struct {
		name  string
		rules []AssetFilterRule
		want  int
	}{
		{"recent", []AssetFilterRule{host, {Column: "lastSeenDays", Operator: "<=", Value: "30"}}, stats.HostsSeen30d},
		{"middle", []AssetFilterRule{host, {Column: "lastSeenDays", Operator: ">", Value: "30"}, {Column: "lastSeenDays", Operator: "<=", Value: "180"}}, stats.HostsSeen180d},
		{"older", []AssetFilterRule{host, {Column: "lastSeenDays", Operator: ">", Value: "180"}}, stats.HostsSeenOlder},
	} {
		got, err := db.CountAssetsByUserWithFilter(7, &AssetFilter{Rules: c.rules, MatchAll: true})
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want || got == 0 {
			t.Errorf("%s: filter finds %d, bucket counts %d", c.name, got, c.want)
		}
	}
}
