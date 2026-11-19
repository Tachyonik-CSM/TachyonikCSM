// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins AM-DATA-45: the usage Admin → Users shows is the same count the limit
// checks use — host assets only of type Host, every vulnerability and
// detection — per user and for the instance.

package database

import (
	"fmt"
	"testing"
)

func TestUsageCountsWhatTheLimitsCount(t *testing.T) {
	db := newTestDB(t)
	for i := 0; i < 3; i++ {
		if _, err := db.CreateAsset(fmt.Sprintf("10.0.0.%d", i), "Host", 7, 1, "scan.xml", nil); err != nil {
			t.Fatal(err)
		}
	}
	// A row of another type, as older versions stored them: not a host.
	if _, err := db.Exec("INSERT INTO assets (name, type, user_id, created_at, modified_at) VALUES ('example.com', 'Domain', 7, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateAsset("10.0.1.1", "Host", 9, 1, "scan.xml", nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.CreateVulnerability(fmt.Sprintf("CVE-%d", i), "10.0.0.1", "443", 50, 1, "scan.xml", 7, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.CreateDetection("ssh", "10.0.2.1", "22", 1, "scan.xml", 11, nil); err != nil {
		t.Fatal(err)
	}

	users, total, err := db.UsageByUser()
	if err != nil {
		t.Fatal(err)
	}
	want := []Usage{
		{UserID: 7, HostAssets: 3, Vulnerabilities: 2},
		{UserID: 9, HostAssets: 1},
		{UserID: 11, Detections: 1},
	}
	if fmt.Sprint(users) != fmt.Sprint(want) {
		t.Errorf("per user %v, want %v", users, want)
	}
	if total != (Usage{HostAssets: 4, Vulnerabilities: 2, Detections: 1}) {
		t.Errorf("instance %v", total)
	}

	// The same counts the limit checks read.
	for _, u := range users {
		hosts, _ := db.CountHostAssetsByUser(u.UserID)
		vulns, _ := db.CountVulnerabilitiesByUserWithFilter(u.UserID, nil)
		dets, _ := db.CountDetectionsByUserWithFilter(u.UserID, nil)
		if hosts != u.HostAssets || vulns != u.Vulnerabilities || dets != u.Detections {
			t.Errorf("user %d: usage %v, the limit checks count %d/%d/%d", u.UserID, u, hosts, vulns, dets)
		}
	}
	if all, _ := db.CountHostAssetsAllUsers(); all != total.HostAssets {
		t.Errorf("instance host assets %d, the appliance check counts %d", total.HostAssets, all)
	}
}

func TestUsageOfAnEmptyInstance(t *testing.T) {
	users, total, err := newTestDB(t).UsageByUser()
	if err != nil || users == nil || len(users) != 0 || total != (Usage{}) {
		t.Errorf("usage %v, total %v, err %v", users, total, err)
	}
}
