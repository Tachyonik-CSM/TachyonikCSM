// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the "is set" and "is not set" operators of the asset filter on the
// three columns whose value can be missing — last scanned, coverage verified
// and scan coverage interval: that they split the assets without overlap,
// that a date julianday() cannot read counts as not set, and that "is not
// set" finds the same assets as the older "> 99999" convention the dashboard
// links used.

package database

import (
	"testing"
	"time"
)

func TestPresenceOperators(t *testing.T) {
	db := newTestDB(t)

	// 10.0.6.1 has every value, 10.0.6.2 none, 10.0.6.3 an unreadable date
	// in both date columns and no interval.
	var full int64
	for _, name := range []string{"10.0.6.1", "10.0.6.2", "10.0.6.3"} {
		res, err := db.CreateAsset(name, "Host", 7, 1, "a.xml", nil)
		if err != nil {
			t.Fatal(err)
		}
		if name == "10.0.6.1" {
			full = res.Asset.ID
		}
	}
	verified := time.Now().Add(-48 * time.Hour)
	interval := 7
	if err := db.SetAssetScanCoverage(full, 1, "a.xml", &verified, &interval); err != nil {
		t.Fatal(err)
	}
	// last_scanned follows the findings; setting it directly is enough for
	// what the filter reads.
	if _, err := db.Exec(`UPDATE assets SET last_scanned = ? WHERE id = ?`, time.Now().Add(-24*time.Hour), full); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE assets SET last_scanned = 'garbage', last_scan_coverage_verification = 'garbage' WHERE name = '10.0.6.3'`); err != nil {
		t.Fatal(err)
	}

	count := func(rule AssetFilterRule) int {
		t.Helper()
		n, err := db.CountAssetsByUserWithFilter(7, &AssetFilter{Rules: []AssetFilterRule{rule}, MatchAll: true})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	for _, column := range []string{"lastScannedDays", "coverageVerifiedDays", "scanCoverageIntervalDays"} {
		set := count(AssetFilterRule{Column: column, Operator: "is set"})
		notSet := count(AssetFilterRule{Column: column, Operator: "is not set"})
		if set != 1 || notSet != 2 {
			t.Errorf("%s: is set %d, is not set %d, want 1 and 2", column, set, notSet)
		}
		if legacy := count(AssetFilterRule{Column: column, Operator: ">", Value: "99999"}); legacy != notSet {
			t.Errorf("%s: \"> 99999\" finds %d, \"is not set\" %d", column, legacy, notSet)
		}
	}

	// An operator without a value on a column that does not offer it is
	// ignored, as any rule it cannot read is.
	if n := count(AssetFilterRule{Column: "score", Operator: "is not set"}); n != 3 {
		t.Errorf("score is not set: %d assets, want the rule ignored (3)", n)
	}
}
