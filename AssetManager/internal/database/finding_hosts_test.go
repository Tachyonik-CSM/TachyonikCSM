// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins migration v7: findings stored before AM-DATA-13 required their host
// asset get one, linked to each of their sources and dated by the newest
// finding of each; a host that had its asset gets nothing new, and a second
// run creates nothing.

package database

import (
	"testing"
	"time"

	"tachyonik/lib/listquery"
)

func TestTheMigrationGivesEveryFindingItsHostAsset(t *testing.T) {
	db := newTestDB(t)
	older := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 4, 2, 11, 30, 0, 0, time.UTC)
	// Written straight to the tables, as before the rule: no host assets.
	if _, err := db.CreateVulnerability("CVE-1", "10.0.0.1", "443", 50, 1, "scan.xml", 7, &older); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateVulnerability("CVE-2", "10.0.0.1", "80", 20, 1, "scan.xml", 7, &newer); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateDetection("ssh", "10.0.0.1", "22", 2, "other.xml", 7, &older); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateDetection("ssh", "10.0.0.2", "22", 0, "", 9, nil); err != nil { // by hand
		t.Fatal(err)
	}
	// A host with its asset already.
	if _, err := db.CreateAsset("10.0.0.3", "Host", 7, 1, "scan.xml", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateVulnerability("CVE-3", "10.0.0.3", "443", 50, 1, "scan.xml", 7, nil); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.FindingsWithoutHostAsset(); n != 4 {
		t.Fatalf("before: %d findings without their asset, want 4", n)
	}

	if err := migrateFindingHostAssets(db); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.FindingsWithoutHostAsset(); n != 0 {
		t.Errorf("after: %d findings without their asset", n)
	}

	assets, err := db.ListAssets([]int64{7}, nil, listquery.Query{})
	if err != nil {
		t.Fatal(err)
	}
	var one *Asset
	for i := range assets {
		if assets[i].Name == "10.0.0.1" {
			one = &assets[i]
		}
	}
	if one == nil || one.Type != AssetTypeHost || len(one.Sources) != 2 {
		t.Fatalf("10.0.0.1: %+v, want a Host linked to sources 1 and 2", one)
	}
	for _, s := range one.Sources {
		want := older
		if s.SourceID != nil && *s.SourceID == 1 {
			want = newer // the newest finding of source 1
		}
		if !s.LastSeen.Equal(want) {
			t.Errorf("link to source %v: last seen %v, want %v", s.SourceID, s.LastSeen, want)
		}
	}
	if len(assets) != 2 {
		t.Errorf("user 7 has %d assets, want 10.0.0.1 and 10.0.0.3", len(assets))
	}
	hand, _ := db.ListAssets([]int64{9}, nil, listquery.Query{})
	if len(hand) != 1 || len(hand[0].Sources) != 0 {
		t.Errorf("user 9's hand-made host: %+v, want one asset without a link", hand)
	}

	// A second run changes nothing.
	if err := migrateFindingHostAssets(db); err != nil {
		t.Fatal(err)
	}
	again, _ := db.ListAssets([]int64{7, 9}, nil, listquery.Query{})
	if len(again) != 3 {
		t.Errorf("a second run left %d assets, want 3", len(again))
	}
}
