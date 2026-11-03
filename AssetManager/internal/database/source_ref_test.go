// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins ParseSourceRef, the reader of "filename (ID: N)" — the source
// reference findings store and the by-source endpoints are called with — and
// that a database from before the migration runner is refused rather than
// run on a schema the queries do not expect.

package database

import (
	"strings"
	"testing"
)

func TestParseSourceRef(t *testing.T) {
	for _, c := range []struct {
		raw   string
		id    int64
		label string
	}{
		{"scan.xml (ID: 108)", 108, "scan.xml"},
		{"  scan.xml (ID:7)  ", 7, "scan.xml"},
		{"name (with) parens.xml (ID: 3)", 3, "name (with) parens.xml"},
		{"scan.xml", 0, "scan.xml"},
		{"scan.xml (ID: x)", 0, "scan.xml (ID: x)"},
		{"(ID: 5) scan.xml", 0, "(ID: 5) scan.xml"},
		{"", 0, ""},
	} {
		id, label := ParseSourceRef(c.raw)
		if id != c.id || label != c.label {
			t.Errorf("ParseSourceRef(%q) = %d, %q; want %d, %q", c.raw, id, label, c.id, c.label)
		}
	}
}

func TestPreRunnerDatabaseIsRefused(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// The assets table as it was before the runner: one row per source,
	// the source in a column of its own.
	if _, err := db.Exec(`CREATE TABLE assets (
		id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL DEFAULT 0,
		name TEXT NOT NULL, type TEXT NOT NULL, source TEXT,
		severity INTEGER NOT NULL DEFAULT 0, score INTEGER NOT NULL DEFAULT 0,
		last_seen DATETIME, created_at DATETIME, modified_at DATETIME)`); err != nil {
		t.Fatal(err)
	}
	err = db.Migrate()
	if err == nil || !strings.Contains(err.Error(), "predates the migration runner") {
		t.Fatalf("Migrate on a pre-runner database: %v; want the refusal", err)
	}
}
