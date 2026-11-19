// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the severity classes of the asset statistics to the filters the
// dashboard's Asset status chart applies on "Show": each filter lists exactly
// the assets its class counts — the band boundaries, and No Threat against
// Unmanaged by the asset's score, 100 with findings and 0 without
// (AM-DATA-18, -19) — for one user and across a shared workspace. A host whose
// only findings are vulnerabilities of severity 0 is No Threat: it used to
// count as Unmanaged, by its detections alone (formerly deviation D1).

package database

import "testing"

// severityClassFilters are the filters the WebUI's AssetsChart sets, with
// severity on the filter's 0-10 scale.
var severityClassFilters = map[string][]AssetFilterRule{
	"critical":  {{Column: "severity", Operator: ">=", Value: "9"}},
	"high":      {{Column: "severity", Operator: ">=", Value: "7"}, {Column: "severity", Operator: "<", Value: "9"}},
	"medium":    {{Column: "severity", Operator: ">=", Value: "4"}, {Column: "severity", Operator: "<", Value: "7"}},
	"low":       {{Column: "severity", Operator: ">", Value: "0"}, {Column: "severity", Operator: "<", Value: "4"}},
	"noThreat":  {{Column: "severity", Operator: "=", Value: "0"}, {Column: "score", Operator: "=", Value: "100"}},
	"unmanaged": {{Column: "severity", Operator: "=", Value: "0"}, {Column: "score", Operator: "=", Value: "0"}},
}

func classCounts(s *AssetStats) map[string]int {
	return map[string]int{
		"critical": s.Critical, "high": s.High, "medium": s.Medium, "low": s.Low,
		"noThreat": s.NoThreat, "unmanaged": s.Unmanaged,
	}
}

func TestEachSeverityClassFilterListsItsClass(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}

	// Real findings at the severity-0 hosts, so their scores are computed.
	for _, h := range []struct {
		name  string
		owner int64
	}{{"zero-detected", 7}, {"zero-vulnerable", 7}, {"zero-alone", 7}, {"zero-other-member", 7}, {"zero-other-member", 8}} {
		if _, err := db.CreateAsset(h.name, "Host", h.owner, 1, "test", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.CreateDetection("ssh", "zero-detected", "22", 1, "test", 7, nil); err != nil {
		t.Fatal(err)
	}
	// Only a vulnerability of severity 0: a finding, so No Threat (D1).
	if _, err := db.CreateVulnerability("Info", "zero-vulnerable", "443", 0, 1, "test", 7, nil); err != nil {
		t.Fatal(err)
	}
	// The same host's detection, recorded by another member of the workspace
	// with their own asset of it (AM-DATA-13): their asset is No Threat, the
	// first member's, about which they have recorded nothing, Unmanaged.
	if _, err := db.CreateDetection("http", "zero-other-member", "80", 1, "test", 8, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RefreshAllAssets(); err != nil {
		t.Fatal(err)
	}
	// The bands: severity on the stored 0-100 scale, at and around every
	// boundary, with the score AM-DATA-18 gives it.
	for name, severity := range map[string]int{
		"crit-100": 100, "crit-90": 90, "high-89": 89, "high-70": 70, "med-69": 69, "med-40": 40,
		"low-39": 39, "low-1": 1,
	} {
		if _, err := db.CreateAsset(name, "Host", 7, 1, "test", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE assets SET severity = ?, score = ? WHERE name = ?`, severity, 100-severity, name); err != nil {
			t.Fatal(err)
		}
	}

	for _, scope := range [][]int64{{7}, {7, 8}} {
		stats, err := db.GetAssetStatsByUsers(scope)
		if len(scope) == 1 {
			stats, err = db.GetAssetStatsByUser(7)
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(scope) == 1 && (stats.NoThreat != 2 || stats.Unmanaged != 2) {
			t.Errorf("user 7: no threat %d, unmanaged %d; want 2 (detected, vulnerable) and 2 (alone, other member's)", stats.NoThreat, stats.Unmanaged)
		}
		if len(scope) == 2 && (stats.NoThreat != 3 || stats.Unmanaged != 2) {
			t.Errorf("users 7 and 8: no threat %d, unmanaged %d; want 3 and 2", stats.NoThreat, stats.Unmanaged)
		}
		for class, want := range classCounts(stats) {
			got, err := db.CountAssetsByUsersWithFilter(scope, &AssetFilter{MatchAll: true, Rules: severityClassFilters[class]})
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("users %v, %s: the filter lists %d, the statistics count %d", scope, class, got, want)
			}
		}
	}
}
