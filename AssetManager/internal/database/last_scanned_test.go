// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins assets.last_scanned: that it is the newest last_seen of the host's
// vulnerabilities and detections whatever layout those are stored in, that
// it falls again (or to NULL) when findings go away, that only Host assets
// get one, that a host created after its findings picks it up, that the v2
// migration fills existing rows, and how the lastScannedDays filter treats a
// host never scanned.

package database

import (
	"testing"
	"time"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return db
}

func lastScanned(t *testing.T, db *DB, name, assetType string) *time.Time {
	t.Helper()
	var v *time.Time
	if err := db.Get(&v, "SELECT last_scanned FROM assets WHERE user_id = 7 AND name = ? AND type = ?", name, assetType); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestLastScanned(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	ago := func(d int) *time.Time { v := now.AddDate(0, 0, -d); return &v }

	if _, err := db.CreateAsset("10.0.0.1", "Host", 7, 0, "", ago(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := insertLegacyAsset(db, "10.0.0.1", "Domain", 7, ago(0)); err != nil {
		t.Fatal(err)
	}
	if v := lastScanned(t, db, "10.0.0.1", "Host"); v != nil {
		t.Fatalf("host without findings: last_scanned = %v, want NULL", v)
	}

	vuln, err := db.CreateVulnerability("CVE-1", "10.0.0.1", "443", 50, "", 7, ago(20))
	if err != nil {
		t.Fatal(err)
	}
	// A detection written in another layout ("T", no offset), 5 days ago.
	if _, err := db.Exec(`INSERT INTO detections (user_id, name, host, port, last_seen) VALUES (7, 'ssh', '10.0.0.1', '22', ?)`,
		now.AddDate(0, 0, -5).Format("2006-01-02T15:04:05")); err != nil {
		t.Fatal(err)
	}
	// Someone else's finding on the same name.
	if _, err := db.CreateVulnerability("CVE-2", "10.0.0.1", "443", 50, "", 8, ago(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RefreshAssetsForHosts([]HostRef{{Host: "10.0.0.1", UserID: 7}}); err != nil {
		t.Fatal(err)
	}
	if v := lastScanned(t, db, "10.0.0.1", "Host"); v == nil || !v.Equal(*ago(5)) {
		t.Fatalf("last_scanned = %v, want %v (the detection)", v, ago(5))
	}
	if v := lastScanned(t, db, "10.0.0.1", "Domain"); v != nil {
		t.Fatalf("domain: last_scanned = %v, want NULL", v)
	}

	// The newer finding goes: back to the vulnerability; then none at all.
	if _, err := db.Exec("DELETE FROM detections WHERE host = '10.0.0.1'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RefreshAssetsForHosts([]HostRef{{Host: "10.0.0.1", UserID: 7}}); err != nil {
		t.Fatal(err)
	}
	if v := lastScanned(t, db, "10.0.0.1", "Host"); v == nil || !v.Equal(*ago(20)) {
		t.Fatalf("after deleting the detection: last_scanned = %v, want %v", v, ago(20))
	}
	if _, _, err := db.DeleteVulnerabilitiesByIDs([]int64{vuln.ID}, 7); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateAllAssetLastScanned(); err != nil {
		t.Fatal(err)
	}
	if v := lastScanned(t, db, "10.0.0.1", "Host"); v != nil {
		t.Fatalf("after deleting all findings: last_scanned = %v, want NULL", v)
	}

	// Findings first, host afterwards.
	if _, err := db.CreateVulnerability("CVE-3", "10.0.0.2", "80", 50, "", 7, ago(40)); err != nil {
		t.Fatal(err)
	}
	res, err := db.CreateAsset("10.0.0.2", "Host", 7, 0, "", ago(0))
	if err != nil {
		t.Fatal(err)
	}
	if v := res.Asset.LastScanned; v == nil || !v.Equal(*ago(40)) {
		t.Fatalf("new host: LastScanned = %v, want %v", v, ago(40))
	}

	// lastScannedDays: 10.0.0.2 was scanned 40 days ago, 10.0.0.1 never.
	for _, tc := range []struct {
		op, value string
		want      []string
	}{
		{"<=", "30", nil},
		{"<=", "45", []string{"10.0.0.2"}},
		{">", "45", []string{"10.0.0.1"}},
	} {
		got, err := db.GetAssetsByUsersWithFilter([]int64{7}, &AssetFilter{MatchAll: true, Rules: []AssetFilterRule{
			{Column: "type", Operator: "=", Value: "Host"},
			{Column: "lastScannedDays", Operator: tc.op, Value: tc.value},
		}})
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, a := range got {
			names = append(names, a.Name)
		}
		if len(names) != len(tc.want) || (len(names) > 0 && names[0] != tc.want[0]) {
			t.Errorf("lastScannedDays %s %s: got %v, want %v", tc.op, tc.value, names, tc.want)
		}
	}
}

func TestMigrateAddLastScannedFillsExistingHosts(t *testing.T) {
	db := newTestDB(t)
	seen := time.Now().UTC().AddDate(0, 0, -3).Truncate(time.Second)
	if _, err := db.CreateAsset("10.0.0.9", "Host", 7, 0, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateDetection("ssh", "10.0.0.9", "22", "", 7, &seen); err != nil {
		t.Fatal(err)
	}
	// As a database from before v2: no column, no recorded v2.
	if _, err := db.Exec("ALTER TABLE assets DROP COLUMN last_scanned"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM schema_migrations WHERE version >= 2"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	if v := lastScanned(t, db, "10.0.0.9", "Host"); v == nil || !v.Equal(seen) {
		t.Fatalf("after migration: last_scanned = %v, want %v", v, seen)
	}
}

// The scanning-coverage buckets of the statistics add up to all hosts, and
// the filter the dashboard's "Show" applies selects exactly the hosts each
// one counts — never scanned included.
func TestScanBucketsMatchFilter(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	for i, d := range []float64{0.5, 1.9, 2.1, 9.9, 10.1, 40} {
		name := "10.0.2." + string(rune('1'+i))
		seen := now.Add(-time.Duration(d * 24 * float64(time.Hour)))
		if _, err := db.CreateVulnerability("CVE-"+name, name, "443", 50, "", 7, &seen); err != nil {
			t.Fatal(err)
		}
		if _, err := db.CreateAsset(name, "Host", 7, 0, "", &now); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"10.0.3.1", "10.0.3.2"} {
		if _, err := db.CreateAsset(name, "Host", 7, 0, "", &now); err != nil {
			t.Fatal(err)
		}
	}
	// A last_scanned that cannot be read counts as never scanned.
	if _, err := db.Exec("UPDATE assets SET last_scanned = 'garbage' WHERE name = '10.0.3.2'"); err != nil {
		t.Fatal(err)
	}
	if _, err := insertLegacyAsset(db, "www.example.com", "Domain", 7, &now); err != nil {
		t.Fatal(err)
	}
	stats, err := db.GetAssetStatsByUser(7)
	if err != nil {
		t.Fatal(err)
	}
	if stats.HostsScanned2d != 2 || stats.HostsScanned10d != 2 || stats.HostsScannedOlder != 2 || stats.HostsNeverScanned != 2 {
		t.Fatalf("scan buckets %d/%d/%d/%d, want 2/2/2/2",
			stats.HostsScanned2d, stats.HostsScanned10d, stats.HostsScannedOlder, stats.HostsNeverScanned)
	}
	bySet, err := db.GetAssetStatsByUsers([]int64{7})
	if err != nil {
		t.Fatal(err)
	}
	if *bySet != *stats {
		t.Errorf("user-set stats %+v differ from per-user %+v", *bySet, *stats)
	}

	host := AssetFilterRule{Column: "type", Operator: "=", Value: "Host"}
	days := func(op, v string) AssetFilterRule {
		return AssetFilterRule{Column: "lastScannedDays", Operator: op, Value: v}
	}
	for _, c := range []struct {
		name  string
		rules []AssetFilterRule
		want  int
	}{
		{"recent", []AssetFilterRule{host, days("<=", "2")}, stats.HostsScanned2d},
		{"middle", []AssetFilterRule{host, days(">", "2"), days("<=", "10")}, stats.HostsScanned10d},
		{"older", []AssetFilterRule{host, days(">", "10"), days("<=", "99999")}, stats.HostsScannedOlder},
		{"never", []AssetFilterRule{host, days(">", "99999")}, stats.HostsNeverScanned},
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
