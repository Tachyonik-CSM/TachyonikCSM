// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins MaxSupportedSchemaVersion to the newest migration. Adding a migration
// without raising it leaves a binary that applies the migration once and then
// refuses to start on the database it migrated itself.

package database

import "testing"

func TestTheMaxSupportedVersionIsTheNewestMigration(t *testing.T) {
	migrations := allMigrations()
	newest := migrations[len(migrations)-1].Version
	if MaxSupportedSchemaVersion != newest {
		t.Errorf("MaxSupportedSchemaVersion is %d, the newest migration is v%d", MaxSupportedSchemaVersion, newest)
	}
	for i, m := range migrations {
		if m.Version != i+1 {
			t.Errorf("migration %d has version %d; versions must run 1, 2, 3, …", i, m.Version)
		}
	}
}

// A database migrated by this binary is one it starts on again.
func TestABinaryStartsOnTheDatabaseItMigrated(t *testing.T) {
	db := newTestDB(t)
	if err := db.Migrate(); err != nil {
		t.Errorf("second start: %v", err)
	}
}
