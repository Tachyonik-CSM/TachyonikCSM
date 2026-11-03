// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the fixed set of asset types: a case variant is stored in canonical
// spelling, any other type is refused on create and on retype, and assets of
// another type stored before the set was fixed are kept and counted for the
// startup report.

package database

import (
	"errors"
	"testing"
	"time"
)

// insertLegacyAsset writes an asset of any type directly, as one stored
// before the types were a fixed set. lastSeen nil means now.
func insertLegacyAsset(db *DB, name, assetType string, userID int64, lastSeen *time.Time) (int64, error) {
	seen := time.Now()
	if lastSeen != nil {
		seen = *lastSeen
	}
	res, err := db.Exec(`INSERT INTO assets (user_id, name, type, last_seen) VALUES (?, ?, ?, ?)`, userID, name, assetType, seen)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func TestAssetTypesAreAFixedSet(t *testing.T) {
	db := newTestDB(t)

	res, err := db.CreateAsset("10.0.0.1", " host ", 7, 0, "", nil)
	if err != nil || res.Asset.Type != AssetTypeHost {
		t.Fatalf("CreateAsset with \" host \": %+v, %v; want it stored as Host", res, err)
	}
	if _, err := db.CreateAsset("www.example.com", "Domain", 7, 0, "", nil); !errors.Is(err, ErrUnknownAssetType) {
		t.Errorf("CreateAsset with Domain: %v, want ErrUnknownAssetType", err)
	}
	if _, err := db.UpdateAssetTypeForUsers(res.Asset.ID, []int64{7}, "Domain"); !errors.Is(err, ErrUnknownAssetType) {
		t.Errorf("retype to Domain: %v, want ErrUnknownAssetType", err)
	}

	if _, err := insertLegacyAsset(db, "www.example.com", "Domain", 7, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := insertLegacyAsset(db, "printer", "Device", 9, nil); err != nil {
		t.Fatal(err)
	}
	unknown, err := db.AssetsOfUnknownType()
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown) != 2 || unknown["Domain"] != 1 || unknown["Device"] != 1 {
		t.Errorf("assets of unknown type: %v, want Domain 1 and Device 1", unknown)
	}
}
