// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The migration runner: numbered, idempotent schema changes, each recorded once
// applied.
//
// It refuses to start against a database whose schema is newer than the binary
// understands, so a rolled-back deployment fails loudly instead of writing
// through a schema it cannot interpret.

package database

import (
	"database/sql"
	"fmt"
	"time"
)

// Migration is one numbered, idempotent schema change. The runner records
// applied versions in the schema_migrations table; on subsequent startup
// it only runs migrations whose version is higher than the highest
// recorded version. New migrations are appended to the list returned by
// allMigrations(); their version numbers are immutable once released.
type Migration struct {
	Version     int
	Description string
	Apply       func(db *DB) error
}

// MaxSupportedSchemaVersion is the highest schema version this binary
// understands. Bumped together with every new entry in allMigrations().
// On startup the runner refuses to proceed if the database already
// records a higher version — that would mean we're a binary downgrade
// past a destructive migration boundary, which the additive-only
// release discipline says we should never reach without first restoring
// a pre-deploy snapshot.
const MaxSupportedSchemaVersion = 6

func ensureSchemaMigrationsTable(db *DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
	`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	return nil
}

func appliedVersion(db *DB) (int, error) {
	var v sql.NullInt64
	if err := db.Get(&v, "SELECT MAX(version) FROM schema_migrations"); err != nil {
		return 0, fmt.Errorf("read schema_migrations: %w", err)
	}
	if !v.Valid {
		return 0, nil
	}
	return int(v.Int64), nil
}

func recordApplied(db *DB, version int) error {
	_, err := db.Exec(
		`INSERT OR IGNORE INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		version, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("record schema_migrations version %d: %w", version, err)
	}
	return nil
}

func runMigrations(db *DB, migrations []Migration) error {
	if err := ensureSchemaMigrationsTable(db); err != nil {
		return err
	}
	current, err := appliedVersion(db)
	if err != nil {
		return err
	}
	if current > MaxSupportedSchemaVersion {
		return fmt.Errorf(
			"database schema version %d is newer than this binary supports (max %d) — "+
				"refusing to start. Restore a snapshot taken before the offending release, "+
				"or upgrade the binary.",
			current, MaxSupportedSchemaVersion,
		)
	}
	for _, m := range migrations {
		if m.Version <= current {
			continue
		}
		if err := m.Apply(db); err != nil {
			return fmt.Errorf("migration v%d (%s): %w", m.Version, m.Description, err)
		}
		if err := recordApplied(db, m.Version); err != nil {
			return err
		}
	}
	return nil
}

// allMigrations: append-only registry. v1 is the legacy baseline
// (everything the service did before the runner existed).
func allMigrations() []Migration {
	return []Migration{
		{
			Version:     1,
			Description: "baseline schema (pre-runner CREATE/ALTER history)",
			Apply:       func(db *DB) error { return baselineMigrate(db) },
		},
		{
			Version:     2,
			Description: "assets.last_scanned: newest finding timestamp of a host",
			Apply:       func(db *DB) error { return migrateAddLastScanned(db) },
		},
		{
			Version:     3,
			Description: "scan coverage verification and interval, per source and per asset",
			Apply:       func(db *DB) error { return migrateAddScanCoverage(db) },
		},
		{
			Version:     4,
			Description: "asset_source.counts_as_seen: whether a link counts toward the asset's last seen",
			Apply:       func(db *DB) error { return migrateAddCountsAsSeen(db) },
		},
		{
			Version:     5,
			Description: "asset_source.modified_at: when an import last touched a link",
			Apply:       func(db *DB) error { return migrateAddLinkModifiedAt(db) },
		},
		{
			Version:     6,
			Description: "vulnerabilities and detections: source_id and source_label, filled from the source string",
			Apply:       func(db *DB) error { return migrateAddFindingSourceColumns(db) },
		},
	}
}
