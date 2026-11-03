// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The migration list: the baseline schema, and each numbered change since.
//
// The shape worth knowing is asset_source: one asset, unique per user by name
// and type, may be observed by many sources. That is what lets a re-import
// delete only the observations its own new run no longer produces, rather than
// the asset itself.

package database

import (
	"fmt"
)

// assetSchema is the assets table as the baseline (v1) creates it. Columns
// added by later migrations (last_scanned, v2; the scan coverage columns, v3)
// are not part of it.
const assetSchema = `
CREATE TABLE IF NOT EXISTS assets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL DEFAULT 0,
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    severity INTEGER NOT NULL DEFAULT 0 CHECK(severity >= 0 AND severity <= 100),
    score INTEGER NOT NULL DEFAULT 0,
    last_seen DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    modified_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (user_id, name, type)
);

CREATE INDEX IF NOT EXISTS idx_assets_user_id ON assets(user_id);
CREATE INDEX IF NOT EXISTS idx_assets_created_at ON assets(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_assets_modified_at ON assets(modified_at DESC);
CREATE INDEX IF NOT EXISTS idx_assets_severity ON assets(severity DESC);
`

const vulnerabilitiesSchema = `
CREATE TABLE IF NOT EXISTS vulnerabilities (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL DEFAULT 0,
    name TEXT NOT NULL,
    host TEXT NOT NULL,
    port TEXT NOT NULL,
    severity INTEGER NOT NULL CHECK(severity >= 0 AND severity <= 100),
    source TEXT NOT NULL DEFAULT '',
    last_seen DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    modified_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_vulnerabilities_user_id ON vulnerabilities(user_id);
CREATE INDEX IF NOT EXISTS idx_vulnerabilities_host ON vulnerabilities(host);
CREATE INDEX IF NOT EXISTS idx_vulnerabilities_severity ON vulnerabilities(severity DESC);
CREATE INDEX IF NOT EXISTS idx_vulnerabilities_source ON vulnerabilities(source);
CREATE INDEX IF NOT EXISTS idx_vulnerabilities_created_at ON vulnerabilities(created_at DESC);
`

const detectionsSchema = `
CREATE TABLE IF NOT EXISTS detections (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL DEFAULT 0,
    name TEXT NOT NULL,
    host TEXT NOT NULL,
    port TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT '',
    last_seen DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    modified_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_detections_user_id ON detections(user_id);
CREATE INDEX IF NOT EXISTS idx_detections_host ON detections(host);
CREATE INDEX IF NOT EXISTS idx_detections_source ON detections(source);
CREATE INDEX IF NOT EXISTS idx_detections_created_at ON detections(created_at DESC);
`

// assetSourceSchema defines the junction table linking assets to their sources.
// One asset (unique by user_id+name+type) can be observed by many sources, each
// with its own last_seen timestamp and a denormalised label for display.
const assetSourceSchema = `
CREATE TABLE IF NOT EXISTS asset_source (
    asset_id INTEGER NOT NULL,
    source_id INTEGER,
    source_label TEXT NOT NULL DEFAULT '',
    last_seen DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (asset_id) REFERENCES assets(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_asset_source_asset_source ON asset_source(asset_id, source_id);
CREATE INDEX IF NOT EXISTS idx_asset_source_source_id ON asset_source(source_id);
`

// Migrate drives the per-service migration runner. See
// migration_runner.go for how versions are tracked.
func (db *DB) Migrate() error {
	return runMigrations(db, allMigrations())
}

// baselineMigrate creates the schema as it stood when the runner was
// introduced — fully idempotent. It refuses a database from before that,
// whose conversion is no longer kept. Future schema changes do NOT extend
// this function; add a new Migration{} entry in allMigrations() with the next
// version number.
func baselineMigrate(db *DB) error {
	// Enable foreign keys (needed for ON DELETE CASCADE on asset_source).
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	// Create base tables (idempotent).
	if _, err := db.Exec(assetSchema); err != nil {
		return fmt.Errorf("failed to run base migrations: %w", err)
	}
	if _, err := db.Exec(vulnerabilitiesSchema); err != nil {
		return fmt.Errorf("failed to create vulnerabilities table: %w", err)
	}
	if _, err := db.Exec(detectionsSchema); err != nil {
		return fmt.Errorf("failed to create detections table: %w", err)
	}
	if _, err := db.Exec(assetSourceSchema); err != nil {
		return fmt.Errorf("failed to create asset_source table: %w", err)
	}

	// A database from before the migration runner still has assets.source,
	// which a migration once moved into asset_source. That conversion has been
	// removed — no such database is left — so refuse it plainly rather than
	// run on a schema the queries do not expect.
	var legacy int
	if err := db.Get(&legacy, "SELECT COUNT(*) FROM pragma_table_info('assets') WHERE name = 'source'"); err != nil {
		return fmt.Errorf("failed to probe assets.source: %w", err)
	}
	if legacy > 0 {
		return fmt.Errorf("this database predates the migration runner (assets still has a source column); upgrading it directly is no longer supported")
	}

	return nil
}

// migrateAddLastScanned adds assets.last_scanned (v2) and fills it for the
// existing host assets from their vulnerabilities and detections. NULL means
// the host has never been scanned. Additive only: a binary from before v2
// simply does not read the column.
func migrateAddLastScanned(db *DB) error {
	var n int
	if err := db.Get(&n, "SELECT COUNT(*) FROM pragma_table_info('assets') WHERE name = 'last_scanned'"); err != nil {
		return fmt.Errorf("check last_scanned column: %w", err)
	}
	if n == 0 {
		if _, err := db.Exec("ALTER TABLE assets ADD COLUMN last_scanned DATETIME"); err != nil {
			return fmt.Errorf("add last_scanned column: %w", err)
		}
	}
	return db.UpdateAllAssetLastScanned()
}

// migrateAddScanCoverage adds the scan coverage columns (v3): per source on
// asset_source, as the source last reported them, and per asset on assets,
// combined over its sources. All start NULL — the values are only ever set
// by an import, so there is nothing to fill. Additive only.
func migrateAddScanCoverage(db *DB) error {
	for _, c := range []struct{ table, column, decl string }{
		{"asset_source", "scan_coverage_verified_at", "DATETIME"},
		{"asset_source", "scan_coverage_interval_days", "INTEGER"},
		{"assets", "last_scan_coverage_verification", "DATETIME"},
		{"assets", "scan_coverage_interval_days", "INTEGER"},
	} {
		var n int
		if err := db.Get(&n, "SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", c.table, c.column); err != nil {
			return fmt.Errorf("check %s.%s column: %w", c.table, c.column, err)
		}
		if n > 0 {
			continue
		}
		if _, err := db.Exec("ALTER TABLE " + c.table + " ADD COLUMN " + c.column + " " + c.decl); err != nil {
			return fmt.Errorf("add %s.%s column: %w", c.table, c.column, err)
		}
	}
	return nil
}

// migrateAddCountsAsSeen adds asset_source.counts_as_seen (v4). A link made
// only to record scan coverage — a scanner's schedule says it would scan the
// host, which is not a sighting of it — is 0, and is left out of the asset's
// last seen. Every existing link is a sighting, which the default of 1 keeps.
// Additive only: a binary from before v4 does not read the column and counts
// every link, as it always did.
func migrateAddCountsAsSeen(db *DB) error {
	var n int
	if err := db.Get(&n, "SELECT COUNT(*) FROM pragma_table_info('asset_source') WHERE name = 'counts_as_seen'"); err != nil {
		return fmt.Errorf("check counts_as_seen column: %w", err)
	}
	if n > 0 {
		return nil
	}
	if _, err := db.Exec("ALTER TABLE asset_source ADD COLUMN counts_as_seen INTEGER NOT NULL DEFAULT 1"); err != nil {
		return fmt.Errorf("add counts_as_seen column: %w", err)
	}
	return nil
}

// migrateAddLinkModifiedAt adds asset_source.modified_at (v5): when an import
// last touched the link. A re-import drops the links it did not touch, and
// used to tell them by last_seen — but that is the date the file says the host
// was seen, which re-importing the same file leaves where it was, so every
// link looked stale and every asset of the source was deleted. Existing links
// get their created_at; a link an older binary writes stays NULL, which the
// stale check reads as created_at. Additive only.
func migrateAddLinkModifiedAt(db *DB) error {
	var n int
	if err := db.Get(&n, "SELECT COUNT(*) FROM pragma_table_info('asset_source') WHERE name = 'modified_at'"); err != nil {
		return fmt.Errorf("check asset_source.modified_at column: %w", err)
	}
	if n == 0 {
		if _, err := db.Exec("ALTER TABLE asset_source ADD COLUMN modified_at DATETIME"); err != nil {
			return fmt.Errorf("add asset_source.modified_at column: %w", err)
		}
	}
	if _, err := db.Exec("UPDATE asset_source SET modified_at = created_at WHERE modified_at IS NULL"); err != nil {
		return fmt.Errorf("fill asset_source.modified_at: %w", err)
	}
	return nil
}

// migrateAddFindingSourceColumns (v6) gives vulnerabilities and detections
// the source_id and source_label an asset's link has, and fills them from the
// "filename (ID: N)" string the source column held: the id where the string
// has one (NULL otherwise), the filename as the label. Additive only: the
// source column stays, no longer read or written, and is dropped in a later
// release, as destructive changes wait one release after a column falls out
// of use.
func migrateAddFindingSourceColumns(db *DB) error {
	for _, table := range []string{"vulnerabilities", "detections"} {
		for _, col := range []struct{ name, decl string }{
			{"source_id", "INTEGER"},
			{"source_label", "TEXT NOT NULL DEFAULT ''"},
		} {
			var n int
			if err := db.Get(&n, "SELECT COUNT(*) FROM pragma_table_info('"+table+"') WHERE name = ?", col.name); err != nil {
				return fmt.Errorf("check %s.%s column: %w", table, col.name, err)
			}
			if n == 0 {
				if _, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN " + col.name + " " + col.decl); err != nil {
					return fmt.Errorf("add %s.%s column: %w", table, col.name, err)
				}
			}
		}
		if _, err := db.Exec("CREATE INDEX IF NOT EXISTS idx_" + table + "_source_id ON " + table + "(source_id)"); err != nil {
			return fmt.Errorf("index %s.source_id: %w", table, err)
		}

		// Filled per distinct source string, in Go, with the parser the API
		// uses — the strings are few (one per imported file), the rows many.
		var refs []string
		if err := db.Select(&refs, "SELECT DISTINCT source FROM "+table+" WHERE source != '' AND source_id IS NULL AND source_label = ''"); err != nil {
			return fmt.Errorf("read %s sources: %w", table, err)
		}
		tx, err := db.Beginx()
		if err != nil {
			return err
		}
		for _, ref := range refs {
			id, label := ParseSourceRef(ref)
			var idArg interface{}
			if id > 0 {
				idArg = id
			}
			if _, err := tx.Exec("UPDATE "+table+" SET source_id = ?, source_label = ? WHERE source = ? AND source_id IS NULL AND source_label = ''", idArg, label, ref); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("fill %s source columns: %w", table, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s source columns: %w", table, err)
		}
	}
	return nil
}
