// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins AM-DATA-37 and AM-DATA-38 (formerly deviation D3): moving a user's
// records merges those the receiver already has — a shared host, a shared
// source link, a shared finding — instead of moving the findings without
// their hosts, and both moving and deleting are all or nothing.

package database

import (
	"strings"
	"testing"
	"time"
)

func count(t *testing.T, db *DB, q string, args ...interface{}) int {
	t.Helper()
	var n int
	if err := db.Get(&n, q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestAMoveMergesWhatTheReceiverHasAlready(t *testing.T) {
	db := newTestDB(t)
	older := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	// The member, 7: two hosts and a vulnerability on the second. The admin,
	// 9: the first host already, from another source and the same one.
	mustCreate := func(_ interface{}, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	mustCreate(db.CreateAsset("10.0.0.1", "Host", 7, 1, "a.xml", &newer))
	mustCreate(db.CreateAsset("10.0.0.2", "Host", 7, 1, "a.xml", nil))
	mustCreate(db.CreateAsset("10.0.0.1", "Host", 9, 2, "b.xml", nil))
	mustCreate(db.CreateAsset("10.0.0.1", "Host", 9, 1, "a.xml", &older))
	mustCreate(db.CreateVulnerability("CVE-2", "10.0.0.2", "443", 50, 1, "a.xml", 7, nil))
	// The same vulnerability at both: the admin's first, the member's later
	// with another score.
	mustCreate(db.CreateVulnerability("CVE-1", "10.0.0.1", "443", 40, 1, "a.xml", 9, nil))
	time.Sleep(1100 * time.Millisecond)
	mustCreate(db.CreateVulnerability("CVE-1", "10.0.0.1", "443", 80, 1, "a.xml", 7, nil))

	res, err := db.TransferAllUserData(7, 9)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RecomputeAfterTransfer(res); err != nil {
		t.Fatal(err)
	}

	if n := count(t, db, "SELECT COUNT(*) FROM assets WHERE user_id = 7") + count(t, db, "SELECT COUNT(*) FROM vulnerabilities WHERE user_id = 7"); n != 0 {
		t.Errorf("%d records left with the member", n)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM assets WHERE user_id = 9"); n != 2 {
		t.Errorf("the admin has %d assets, want 10.0.0.1 and 10.0.0.2", n)
	}
	// 10.0.0.1: one link per source, source 1's with the newer sighting.
	var links []struct {
		SourceID int64  `db:"source_id"`
		LastSeen string `db:"last_seen"`
	}
	if err := db.Select(&links, "SELECT s.source_id, s.last_seen FROM asset_source s JOIN assets a ON a.id = s.asset_id WHERE a.user_id = 9 AND a.name = '10.0.0.1' ORDER BY s.source_id"); err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 || links[0].SourceID != 1 || links[1].SourceID != 2 || !strings.HasPrefix(links[0].LastSeen, "2026-04-01") {
		t.Errorf("links of 10.0.0.1: %+v, want sources 1 (seen 2026-04-01) and 2", links)
	}
	// CVE-1 once, with the member's later score; CVE-2 moved.
	var sev []int
	if err := db.Select(&sev, "SELECT severity FROM vulnerabilities WHERE user_id = 9 AND name = 'CVE-1'"); err != nil {
		t.Fatal(err)
	}
	if len(sev) != 1 || sev[0] != 80 {
		t.Errorf("CVE-1 at the admin: %v, want one with 80", sev)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM vulnerabilities WHERE user_id = 9 AND name = 'CVE-2'"); n != 1 {
		t.Errorf("CVE-2 at the admin: %d, want 1", n)
	}
	if n, _ := db.FindingsWithoutHostAsset(); n != 0 {
		t.Errorf("%d findings without their host asset", n)
	}
	if res.Merged != 2 || res.Moved != 2 {
		t.Errorf("moved %d, merged %d; want 2 moved (10.0.0.2, CVE-2) and 2 merged (10.0.0.1, CVE-1)", res.Moved, res.Merged)
	}
}

func TestAFailedMoveMovesNothing(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.CreateAsset("10.0.0.1", "Host", 7, 1, "a.xml", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateDetection("ssh", "10.0.0.1", "22", 1, "a.xml", 7, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TRIGGER fail_move BEFORE UPDATE OF user_id ON detections BEGIN SELECT RAISE(ABORT, 'forced'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.TransferAllUserData(7, 9); err == nil {
		t.Fatal("a move that failed midway reported success")
	}
	if n := count(t, db, "SELECT COUNT(*) FROM assets WHERE user_id = 7"); n != 1 {
		t.Errorf("the asset moved although the detections did not (%d left)", n)
	}
}

func TestAFailedDeletionDeletesNothing(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.CreateAsset("10.0.0.1", "Host", 7, 1, "a.xml", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateDetection("ssh", "10.0.0.1", "22", 1, "a.xml", 7, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TRIGGER fail_delete BEFORE DELETE ON assets BEGIN SELECT RAISE(ABORT, 'forced'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DeleteAllUserData(7); err == nil {
		t.Fatal("a deletion that failed midway reported success")
	}
	if n := count(t, db, "SELECT COUNT(*) FROM detections WHERE user_id = 7"); n != 1 {
		t.Errorf("the detection went although the asset could not (%d left)", n)
	}
	if _, err := db.Exec("DROP TRIGGER fail_delete"); err != nil {
		t.Fatal(err)
	}
	if n, err := db.DeleteAllUserData(7); err != nil || n != 2 {
		t.Errorf("deleting again: %d, %v; want 2 records", n, err)
	}
}
