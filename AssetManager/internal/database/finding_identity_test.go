// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins AM-DATA-47: a finding is its owner, source, host, port and name.
// Reporting it again keeps the record — the new severity and label, last
// seen only forward — and migration v8 merges the copies stored before.

package database

import (
	"testing"
	"time"
)

func TestAFindingReportedAgainIsTheSameRecord(t *testing.T) {
	db := newTestDB(t)
	sev := func(v int) *int { return &v }
	older := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	r := FindingReport{Name: "CVE-1", Host: "10.0.0.1", Port: "80/tcp", Severity: sev(78), SourceID: 109, SourceLabel: "scan.xml", UserID: 52, LastSeen: &newer}

	first, err := db.ReportFinding(VulnerabilityTable, r)
	if err != nil || !first.New || !first.Changed {
		t.Fatalf("first report: %+v %v", first, err)
	}
	again, err := db.ReportFinding(VulnerabilityTable, r)
	if err != nil || again.New || again.Changed || again.ID != first.ID {
		t.Errorf("the same report again: %+v %v, want the record %d unchanged", again, err, first.ID)
	}

	// An older file with another score: the score follows, the date does not
	// go back.
	r2 := r
	r2.Severity, r2.LastSeen = sev(60), &older
	out, _ := db.ReportFinding(VulnerabilityTable, r2)
	v, _ := db.GetVulnerabilityByID(first.ID)
	if out.ID != first.ID || !out.Changed || v.Severity != 60 || !v.LastSeen.Equal(newer) {
		t.Errorf("reported with another severity: %+v, record %+v", out, v)
	}

	// Another port, another source: other findings.
	for _, other := range []FindingReport{
		{Name: "CVE-1", Host: "10.0.0.1", Port: "443/tcp", Severity: sev(78), SourceID: 109, UserID: 52},
		{Name: "CVE-1", Host: "10.0.0.1", Port: "80/tcp", Severity: sev(78), SourceID: 110, UserID: 52},
	} {
		if o, _ := db.ReportFinding(VulnerabilityTable, other); !o.New {
			t.Errorf("%+v was taken for the same finding", other)
		}
	}

	// Detections alike.
	d := FindingReport{Name: "ssh", Host: "10.0.0.1", Port: "22/tcp", SourceID: 109, UserID: 52}
	a, _ := db.ReportFinding(DetectionTable, d)
	b, _ := db.ReportFinding(DetectionTable, d)
	if !a.New || b.New || a.ID != b.ID {
		t.Errorf("detection reported twice: %+v %+v", a, b)
	}
	if exists, _ := db.FindingExists(DetectionTable, d); !exists {
		t.Error("FindingExists does not find the detection")
	}
}

func TestTheMigrationMergesTheCopies(t *testing.T) {
	db := newTestDB(t)
	// As before v8: no index, the same finding stored several times.
	for _, q := range []string{
		"DROP INDEX idx_vulnerabilities_identity",
		"DROP INDEX idx_detections_identity",
		"INSERT INTO vulnerabilities (id, name, host, port, severity, source_id, source_label, user_id, created_at, modified_at, last_seen) VALUES (719, 'CVE-1', '10.0.0.1', '80/tcp', 70, 109, 'scan.xml', 52, '2026-10-20 15:14:32', '2026-10-20 15:14:32', '2026-10-19 11:04:33+00:00')",
		"INSERT INTO vulnerabilities (id, name, host, port, severity, source_id, source_label, user_id, created_at, modified_at, last_seen) VALUES (720, 'CVE-1', '10.0.0.1', '80/tcp', 78, 109, 'scan.xml', 52, '2026-10-20 15:14:32', '2026-10-21 08:00:00', '2026-10-20T09:00:00Z')",
		"INSERT INTO vulnerabilities (id, name, host, port, severity, source_id, source_label, user_id, created_at, modified_at, last_seen) VALUES (721, 'CVE-1', '10.0.0.1', '443/tcp', 78, 109, 'scan.xml', 52, '2026-10-20 15:14:32', '2026-10-20 15:14:32', '2026-10-19 11:04:33+00:00')",
		"INSERT INTO detections (name, host, port, source_id, source_label, user_id, created_at, modified_at, last_seen) VALUES ('ssh', '10.0.0.1', '22/tcp', 109, 'scan.xml', 52, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)",
		"INSERT INTO detections (name, host, port, source_id, source_label, user_id, created_at, modified_at, last_seen) VALUES ('ssh', '10.0.0.1', '22/tcp', 109, 'scan.xml', 52, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)",
		"INSERT INTO detections (name, host, port, source_id, source_label, user_id, created_at, modified_at, last_seen) VALUES ('ssh', '10.0.0.1', '22/tcp', 109, 'scan.xml', 52, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := migrateFindingIdentity(db); err != nil {
		t.Fatal(err)
	}

	var ids []int64
	if err := db.Select(&ids, "SELECT id FROM vulnerabilities ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 719 || ids[1] != 721 {
		t.Fatalf("vulnerabilities left: %v, want 719 (80/tcp) and 721 (443/tcp)", ids)
	}
	v, _ := db.GetVulnerabilityByID(719)
	if v.Severity != 78 || v.LastSeen.UTC().Format(time.RFC3339) != "2026-10-20T09:00:00Z" {
		t.Errorf("the kept record: severity %d, last seen %v; want the highest and the newest", v.Severity, v.LastSeen)
	}
	var dets int
	db.Get(&dets, "SELECT COUNT(*) FROM detections")
	if dets != 1 {
		t.Errorf("%d detections left, want 1", dets)
	}

	// The index holds the rule from now on; a second run changes nothing.
	if _, err := db.Exec("INSERT INTO detections (name, host, port, source_id, source_label, user_id, created_at, modified_at, last_seen) VALUES ('ssh', '10.0.0.1', '22/tcp', 109, 'scan.xml', 52, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)"); err == nil {
		t.Error("a copy could still be stored")
	}
	if err := migrateFindingIdentity(db); err != nil {
		t.Fatal(err)
	}
}
