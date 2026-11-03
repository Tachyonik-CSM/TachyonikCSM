// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins that vulnerabilities and detections — the two kinds of finding, kept in
// tables of the same shape — behave alike in everything they share: listing
// and counting with and without a filter, the hosts of a set of IDs, deleting
// by source (one user's or everyone's), deleting what a re-import left stale,
// deleting by ID (only records no source owns, only the caller's), and
// clearing a source while keeping the records.

package database

import (
	"sort"
	"testing"
	"time"
)

// findingOps is one kind of finding seen through the public API.
type findingOps struct {
	name        string
	create      func(db *DB, name, host string, sourceID int64, label string, userID int64) int64
	list        func(db *DB, userIDs []int64, f *Filter) (names []string, err error)
	count       func(db *DB, userID int64, f *Filter) (int, error)
	hosts       func(db *DB, ids []int64, userID int64) ([]string, error)
	bySource    func(db *DB, sourceID int64, userID int64) ([]int64, error)
	stale       func(db *DB, sourceID int64, userID int64, before time.Time) ([]int64, error)
	byIDs       func(db *DB, ids []int64, userID int64) ([]int64, []int64, error)
	clearSource func(db *DB, sourceID int64, userID int64) ([]int64, error)
}

var findingKinds = []findingOps{
	{
		name: "vulnerabilities",
		create: func(db *DB, name, host string, sourceID int64, label string, userID int64) int64 {
			v, err := db.CreateVulnerability(name, host, "80", 50, sourceID, label, userID, nil)
			if err != nil {
				panic(err)
			}
			return v.ID
		},
		list: func(db *DB, userIDs []int64, f *Filter) ([]string, error) {
			rows, err := db.GetVulnerabilitiesByUsersWithFilter(userIDs, f)
			names := make([]string, len(rows))
			for i, r := range rows {
				names[i] = r.Name
			}
			return names, err
		},
		count:       (*DB).CountVulnerabilitiesByUserWithFilter,
		hosts:       (*DB).GetVulnerabilityHostsByIDs,
		bySource:    (*DB).DeleteVulnerabilitiesBySource,
		stale:       (*DB).DeleteStaleVulnerabilitiesBySource,
		byIDs:       (*DB).DeleteVulnerabilitiesByIDs,
		clearSource: (*DB).ClearVulnerabilitySourceBySource,
	},
	{
		name: "detections",
		create: func(db *DB, name, host string, sourceID int64, label string, userID int64) int64 {
			d, err := db.CreateDetection(name, host, "80", sourceID, label, userID, nil)
			if err != nil {
				panic(err)
			}
			return d.ID
		},
		list: func(db *DB, userIDs []int64, f *Filter) ([]string, error) {
			rows, err := db.GetDetectionsByUsersWithFilter(userIDs, f)
			names := make([]string, len(rows))
			for i, r := range rows {
				names[i] = r.Name
			}
			return names, err
		},
		count:       (*DB).CountDetectionsByUserWithFilter,
		hosts:       (*DB).GetDetectionHostsByIDs,
		bySource:    (*DB).DeleteDetectionsBySource,
		stale:       (*DB).DeleteStaleDetectionsBySource,
		byIDs:       (*DB).DeleteDetectionsByIDs,
		clearSource: (*DB).ClearDetectionSourceBySource,
	},
}

func sorted(ids []int64) []int64 {
	out := append([]int64(nil), ids...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sameIDs(t *testing.T, what string, got, want []int64) {
	t.Helper()
	g, w := sorted(got), sorted(want)
	if len(g) != len(w) {
		t.Errorf("%s: got %v, want %v", what, g, w)
		return
	}
	for i := range g {
		if g[i] != w[i] {
			t.Errorf("%s: got %v, want %v", what, g, w)
			return
		}
	}
}

// remaining lists the names of every finding of user 7 and 9, sorted.
func remaining(t *testing.T, db *DB, k findingOps) []string {
	t.Helper()
	names, err := k.list(db, []int64{7, 9}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	return names
}

func TestFindingsBehaveAlike(t *testing.T) {
	for _, k := range findingKinds {
		t.Run(k.name, func(t *testing.T) {
			t.Run("list, count and hosts", func(t *testing.T) {
				db := newTestDB(t)
				a := k.create(db, "alpha", "10.0.0.1", 1, "s1.xml", 7)
				b := k.create(db, "beta", "10.0.0.2", 1, "s1.xml", 7)
				k.create(db, "gamma", "10.0.0.3", 1, "s1.xml", 9)

				all, err := k.list(db, []int64{7}, nil)
				if err != nil || len(all) != 2 {
					t.Fatalf("list user 7: %v %v", all, err)
				}
				f := &Filter{MatchAll: true, Rules: []FilterRule{{Column: "name", Operator: "contains", Value: "alp"}}}
				got, err := k.list(db, []int64{7, 9}, f)
				if err != nil || len(got) != 1 || got[0] != "alpha" {
					t.Fatalf("filtered list: %v %v", got, err)
				}
				notHost := &Filter{MatchAll: true, Rules: []FilterRule{{Column: "host", Operator: "does not contain", Value: "10.0.0.1"}}}
				if n, err := k.count(db, 7, notHost); err != nil || n != 1 {
					t.Fatalf("count host does not contain: %d %v", n, err)
				}
				if n, err := k.count(db, 7, nil); err != nil || n != 2 {
					t.Fatalf("count all: %d %v", n, err)
				}
				hosts, err := k.hosts(db, []int64{a, b}, 7)
				sort.Strings(hosts)
				if err != nil || len(hosts) != 2 || hosts[0] != "10.0.0.1" || hosts[1] != "10.0.0.2" {
					t.Fatalf("hosts: %v %v", hosts, err)
				}
				if hosts, _ := k.hosts(db, []int64{a}, 9); len(hosts) != 0 {
					t.Fatalf("hosts of another user's ids: %v", hosts)
				}
			})

			t.Run("delete by source", func(t *testing.T) {
				db := newTestDB(t)
				a := k.create(db, "a", "h1", 1, "s1.xml", 7)
				// The same source after its file was renamed: found by its id.
				renamed := k.create(db, "a2", "h1", 1, "renamed.xml", 7)
				k.create(db, "b", "h1", 2, "s2.xml", 7)
				c := k.create(db, "c", "h1", 1, "s1.xml", 9)
				ids, err := k.bySource(db, 1, 7)
				if err != nil {
					t.Fatal(err)
				}
				sameIDs(t, "user 7", ids, []int64{a, renamed})
				ids, err = k.bySource(db, 1, 0)
				if err != nil {
					t.Fatal(err)
				}
				sameIDs(t, "everyone", ids, []int64{c})
				if ids, _ := k.bySource(db, 1, 0); ids == nil || len(ids) != 0 {
					t.Errorf("nothing left: got %#v, want an empty slice", ids)
				}
				if got := remaining(t, db, k); len(got) != 1 || got[0] != "b" {
					t.Errorf("left: %v", got)
				}
			})

			t.Run("delete stale", func(t *testing.T) {
				db := newTestDB(t)
				old := k.create(db, "old", "h1", 1, "s1.xml", 7)
				oldOther := k.create(db, "old9", "h1", 1, "s1.xml", 9)
				if _, err := db.Exec(`UPDATE `+k.name+` SET modified_at = ?`, time.Now().Add(-time.Hour)); err != nil {
					t.Fatal(err)
				}
				k.create(db, "new", "h1", 1, "s1.xml", 7)
				before := time.Now().Add(-time.Minute)
				ids, err := k.stale(db, 1, 7, before)
				if err != nil {
					t.Fatal(err)
				}
				sameIDs(t, "user 7", ids, []int64{old})
				ids, err = k.stale(db, 1, 0, before)
				if err != nil {
					t.Fatal(err)
				}
				sameIDs(t, "everyone", ids, []int64{oldOther})
				if got := remaining(t, db, k); len(got) != 1 || got[0] != "new" {
					t.Errorf("left: %v", got)
				}
			})

			t.Run("delete by IDs", func(t *testing.T) {
				db := newTestDB(t)
				manual := k.create(db, "manual", "h1", 0, "", 7)
				blank := k.create(db, "blank", "h1", 0, "  ", 7)
				sourced := k.create(db, "sourced", "h1", 1, "s1.xml", 7)
				foreign := k.create(db, "foreign", "h1", 0, "", 9)
				// A source known only by its name still owns the record.
				labelOnly := k.create(db, "labelonly", "h1", 0, "s9.xml", 7)
				deleted, skipped, err := k.byIDs(db, []int64{manual, blank, sourced, foreign, labelOnly, 999}, 7)
				if err != nil {
					t.Fatal(err)
				}
				sameIDs(t, "deleted", deleted, []int64{manual, blank})
				sameIDs(t, "skipped", skipped, []int64{sourced, foreign, labelOnly, 999})
				if got := remaining(t, db, k); len(got) != 3 || got[0] != "foreign" || got[1] != "labelonly" || got[2] != "sourced" {
					t.Errorf("left: %v", got)
				}
			})

			t.Run("clear source", func(t *testing.T) {
				db := newTestDB(t)
				a := k.create(db, "a", "h1", 1, "s1.xml", 7)
				b := k.create(db, "b", "h1", 1, "s1.xml", 9)
				k.create(db, "c", "h1", 2, "s2.xml", 7)
				ids, err := k.clearSource(db, 1, 7)
				if err != nil {
					t.Fatal(err)
				}
				sameIDs(t, "user 7", ids, []int64{a})
				ids, err = k.clearSource(db, 1, 0)
				if err != nil {
					t.Fatal(err)
				}
				sameIDs(t, "everyone", ids, []int64{b})
				var cleared int
				if err := db.Get(&cleared, `SELECT COUNT(*) FROM `+k.name+` WHERE source_id IS NULL AND source_label = ''`); err != nil || cleared != 2 {
					t.Errorf("records without a source: %d %v", cleared, err)
				}
				if got := remaining(t, db, k); len(got) != 3 {
					t.Errorf("clearing deleted records: %v", got)
				}
			})
		})
	}
}
