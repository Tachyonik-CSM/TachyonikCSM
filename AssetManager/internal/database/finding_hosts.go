// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Migration v7: every finding gets the host asset of its owner (AM-DATA-13).
// Before, a finding could sit on a host with no asset — its host asset
// refused by the limit, a routine that emitted findings only, or the asset
// removed with another source. The missing assets are created as creating
// the finding now would: of type Host, named by the host, linked to each
// source the findings came from and dated by the newest finding of that
// source. No limit is applied: the findings exist already.
// docs/plans/findings-need-their-host-asset.md.

package database

import (
	"database/sql"
	"fmt"
	"time"

	"tachyonik/lib/logger"
)

// missingFindingHost is one owner, host and source whose findings have no
// host asset.
type missingFindingHost struct {
	UserID      int64          `db:"user_id"`
	Host        string         `db:"host"`
	SourceID    int64          `db:"source_id"`
	SourceLabel string         `db:"source_label"`
	LastSeen    sql.NullString `db:"last_seen"`
}

// FindingsWithoutHostAsset counts the findings whose owner has no asset named
// by their host. AM-DATA-13 holds when it is 0.
func (db *DB) FindingsWithoutHostAsset() (int, error) {
	var n int
	err := db.Get(&n, `SELECT COUNT(*) FROM (
			SELECT user_id, host FROM vulnerabilities UNION ALL SELECT user_id, host FROM detections
		) f WHERE NOT EXISTS (SELECT 1 FROM assets a WHERE a.user_id = f.user_id AND a.name = f.host)`)
	if err != nil {
		return 0, fmt.Errorf("failed to count findings without a host asset: %w", err)
	}
	return n, nil
}

func migrateFindingHostAssets(db *DB) error {
	var missing []missingFindingHost
	// last_seen is stored in more than one layout; julianday() reads them
	// all, and datetime() writes the newest back in one (UTC).
	err := db.Select(&missing, `
		SELECT user_id, host, COALESCE(source_id, 0) AS source_id, COALESCE(source_label, '') AS source_label,
		       datetime(MAX(julianday(last_seen))) AS last_seen
		FROM (
			SELECT user_id, host, source_id, source_label, last_seen FROM vulnerabilities
			UNION ALL
			SELECT user_id, host, source_id, source_label, last_seen FROM detections
		) f
		WHERE host <> '' AND user_id > 0
		  AND NOT EXISTS (SELECT 1 FROM assets a WHERE a.user_id = f.user_id AND a.name = f.host)
		GROUP BY user_id, host, COALESCE(source_id, 0), COALESCE(source_label, '')
		ORDER BY user_id, host`)
	if err != nil {
		return fmt.Errorf("find findings without a host asset: %w", err)
	}
	created, owners := 0, map[int64]bool{}
	for _, m := range missing {
		var lastSeen *time.Time
		if m.LastSeen.Valid {
			if t, perr := time.Parse("2006-01-02 15:04:05", m.LastSeen.String); perr == nil {
				lastSeen = &t
			}
		}
		res, err := db.CreateAsset(m.Host, AssetTypeHost, m.UserID, m.SourceID, m.SourceLabel, lastSeen)
		if err != nil {
			return fmt.Errorf("create the host asset %q of user %d: %w", m.Host, m.UserID, err)
		}
		if res.AssetWasNew {
			created++
			owners[m.UserID] = true
		}
	}
	if created > 0 {
		logger.Infof("Migration v7: created %d host asset(s) for %d owner(s) whose findings had none (AM-DATA-13)", created, len(owners))
	}
	return nil
}
