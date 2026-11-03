// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins migration v6: findings stored with only the "filename (ID: N)" string
// get its id and filename as source_id and source_label; a string without an
// id becomes a label alone; one entered by hand stays without a source; and
// running it again changes nothing.

package database

import "testing"

func TestFindingSourceColumnsAreFilled(t *testing.T) {
	db := newTestDB(t)
	for _, table := range []string{"vulnerabilities", "detections"} {
		for _, src := range []string{"scan.xml (ID: 108)", "report.csv", ""} {
			cols, vals := "name, host, port, source, user_id", "'n', 'h', '1', ?, 7"
			if table == "vulnerabilities" {
				cols, vals = cols+", severity", vals+", 50"
			}
			if _, err := db.Exec(`INSERT INTO `+table+` (`+cols+`) VALUES (`+vals+`)`, src); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := 0; i < 2; i++ {
		if err := migrateAddFindingSourceColumns(db); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	for _, table := range []string{"vulnerabilities", "detections"} {
		var rows []struct {
			Source      string `db:"source"`
			SourceID    *int64 `db:"source_id"`
			SourceLabel string `db:"source_label"`
		}
		if err := db.Select(&rows, `SELECT source, source_id, source_label FROM `+table+` ORDER BY id`); err != nil {
			t.Fatal(err)
		}
		want := []struct {
			id    int64
			label string
		}{{108, "scan.xml"}, {0, "report.csv"}, {0, ""}}
		for i, r := range rows {
			var id int64
			if r.SourceID != nil {
				id = *r.SourceID
			}
			if id != want[i].id || r.SourceLabel != want[i].label {
				t.Errorf("%s %q: id %d label %q, want %d %q", table, r.Source, id, r.SourceLabel, want[i].id, want[i].label)
			}
		}
	}
}
