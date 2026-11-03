// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins DistinctUserIDs, which SystemManager's orphan sweep calls: that the
// query runs at all — a stray "\n" in its raw string once made every call a
// SQLite syntax error, so this service's orphans were never swept — and that
// each owner is reported once.

package database

import (
	"sort"
	"testing"
	"time"
)

func TestDistinctUserIDs(t *testing.T) {
	db := newTestDB(t)

	ids, err := db.DistinctUserIDs()
	if err != nil {
		t.Fatalf("DistinctUserIDs on an empty database: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("empty database: got %v, want none", ids)
	}

	now := time.Now()
	for _, a := range []struct {
		name   string
		userID int64
	}{{"10.0.0.1", 7}, {"10.0.0.2", 7}, {"10.0.0.3", 9}} {
		if _, err := db.CreateAsset(a.name, "Host", a.userID, 0, "", &now); err != nil {
			t.Fatal(err)
		}
	}

	ids, err = db.DistinctUserIDs()
	if err != nil {
		t.Fatalf("DistinctUserIDs: %v", err)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) != 2 || ids[0] != 7 || ids[1] != 9 {
		t.Fatalf("got %v, want [7 9]", ids)
	}
}
