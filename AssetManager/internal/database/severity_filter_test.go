// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the severity classes of the asset statistics to the filters the
// dashboard's Asset status chart applies on "Show": each filter lists exactly
// the assets its class counts — the band boundaries, and No Threat against
// Unmanaged by whether the host has detections — for one user and across a
// shared workspace, where a host's detections may belong to another member.

package database

import "testing"

// severityClassFilters are the filters the WebUI's AssetsChart sets, with
// severity on the filter's 0-10 scale.
var severityClassFilters = map[string][]AssetFilterRule{
	"critical":  {{Column: "severity", Operator: ">=", Value: "9"}},
	"high":      {{Column: "severity", Operator: ">=", Value: "7"}, {Column: "severity", Operator: "<", Value: "9"}},
	"medium":    {{Column: "severity", Operator: ">=", Value: "4"}, {Column: "severity", Operator: "<", Value: "7"}},
	"low":       {{Column: "severity", Operator: ">", Value: "0"}, {Column: "severity", Operator: "<", Value: "4"}},
	"noThreat":  {{Column: "severity", Operator: "=", Value: "0"}, {Column: "detections", Operator: ">", Value: "0"}},
	"unmanaged": {{Column: "severity", Operator: "=", Value: "0"}, {Column: "detections", Operator: "=", Value: "0"}},
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

	// Severity on the stored 0-100 scale, at and around every boundary.
	for name, severity := range map[string]int{
		"crit-100": 100, "crit-90": 90, "high-89": 89, "high-70": 70, "med-69": 69, "med-40": 40,
		"low-39": 39, "low-1": 1, "zero-detected": 0, "zero-alone": 0, "zero-other-member": 0,
	} {
		if _, err := db.CreateAsset(name, "Host", 7, 0, "", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE assets SET severity = ? WHERE name = ?`, severity, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.CreateDetection("ssh", "zero-detected", "22", "test", 7, nil); err != nil {
		t.Fatal(err)
	}
	// A detection another member of the workspace recorded for this host.
	if _, err := db.CreateDetection("http", "zero-other-member", "80", "test", 8, nil); err != nil {
		t.Fatal(err)
	}

	for _, scope := range [][]int64{{7}, {7, 8}} {
		stats, err := db.GetAssetStatsByUsers(scope)
		if len(scope) == 1 {
			stats, err = db.GetAssetStatsByUser(7)
		}
		if err != nil {
			t.Fatal(err)
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
