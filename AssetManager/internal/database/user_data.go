// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// A user's data as a whole: deleting it when the account goes, moving it to
// another user (an anonymous session that registers), listing which users
// still hold any, for SystemManager's orphan sweep, and counting what each
// holds against their limits, for Admin → Users (AM-DATA-45).

package database

import (
	"fmt"
	"time"
)

// DeleteAllUserData deletes all assets (with their links), vulnerabilities and
// detections of a user, all or nothing (AM-DATA-37): any failure undoes it
// and is returned, so SystemManager can ask again. It returns how many
// records went.
func (db *DB) DeleteAllUserData(userID int64) (int64, error) {
	tx, err := db.Beginx()
	if err != nil {
		return 0, fmt.Errorf("failed to start deleting the records of user %d: %w", userID, err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	var total int64
	for _, table := range []string{"detections", "vulnerabilities", "assets"} {
		res, err := tx.Exec("DELETE FROM "+table+" WHERE user_id = ?", userID)
		if err != nil {
			return 0, fmt.Errorf("failed to delete the %s of user %d: %w", table, userID, err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to delete the records of user %d: %w", userID, err)
	}
	return total, nil
}

// TransferResult is what TransferAllUserData did.
type TransferResult struct {
	// Moved counts the records that changed owner, Merged those folded into a
	// record the receiver already had.
	Moved, Merged int64
	// Assets are the receiver's assets that gained a link, to recompute.
	Assets []int64
}

// TransferAllUserData moves all records of one user to another, completely or
// not at all (AM-DATA-38). A record the receiver already has is merged into
// the receiver's instead of blocking the move:
//   - an asset of the same name and type gives the receiver's its links; two
//     links of the same source become one — the newer sighting, the later
//     touch, the newer report's label and scan coverage (AM-DATA-09);
//   - a finding of the same identity (AM-DATA-47) is folded into the
//     receiver's, which takes the newer report's label and severity and the
//     newest last seen.
//
// Moved each kind in one statement, a single shared host used to stop all
// assets from moving while the findings moved anyway, and the answer was
// success (formerly deviation D3). Derived values are recomputed by the
// caller (RecomputeAfterTransfer).
func (db *DB) TransferAllUserData(fromUserID, toUserID int64) (TransferResult, error) {
	var out TransferResult
	if fromUserID == toUserID {
		return out, nil
	}
	tx, err := db.Beginx()
	if err != nil {
		return out, fmt.Errorf("failed to start moving the records of user %d: %w", fromUserID, err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	now := time.Now()
	fail := func(what string, err error) (TransferResult, error) {
		return TransferResult{}, fmt.Errorf("failed to move the records of user %d to user %d: %s: %w", fromUserID, toUserID, what, err)
	}

	// Assets the receiver has already: merge their links, then drop them.
	var pairs []struct {
		From int64 `db:"from_id"`
		To   int64 `db:"to_id"`
	}
	if err := tx.Select(&pairs, `SELECT a.id AS from_id, b.id AS to_id FROM assets a
		JOIN assets b ON b.user_id = ? AND b.name = a.name AND b.type = a.type
		WHERE a.user_id = ?`, toUserID, fromUserID); err != nil {
		return fail("find the shared assets", err)
	}
	for _, p := range pairs {
		// A link of a source the receiver's asset has too: one link.
		if _, err := tx.Exec(`UPDATE asset_source AS b SET
			last_seen = CASE
				WHEN a.counts_as_seen = 1 AND (b.counts_as_seen = 0 OR julianday(a.last_seen) > julianday(b.last_seen)) THEN a.last_seen
				WHEN b.counts_as_seen = 1 THEN b.last_seen
				WHEN julianday(a.last_seen) > julianday(b.last_seen) THEN a.last_seen
				ELSE b.last_seen END,
			counts_as_seen = MAX(a.counts_as_seen, b.counts_as_seen),
			source_label = CASE WHEN `+linkNewer+` THEN a.source_label ELSE b.source_label END,
			scan_coverage_verified_at = CASE WHEN `+linkNewer+` AND a.scan_coverage_verified_at IS NOT NULL THEN a.scan_coverage_verified_at ELSE COALESCE(b.scan_coverage_verified_at, a.scan_coverage_verified_at) END,
			scan_coverage_interval_days = CASE WHEN `+linkNewer+` AND a.scan_coverage_interval_days IS NOT NULL THEN a.scan_coverage_interval_days ELSE COALESCE(b.scan_coverage_interval_days, a.scan_coverage_interval_days) END,
			modified_at = CASE WHEN `+linkNewer+` THEN a.modified_at ELSE b.modified_at END
			FROM asset_source AS a
			WHERE a.asset_id = ? AND b.asset_id = ? AND a.source_id IS b.source_id`, p.From, p.To); err != nil {
			return fail("merge the links of a shared asset", err)
		}
		// The other links move over.
		if _, err := tx.Exec(`UPDATE asset_source SET asset_id = ? WHERE asset_id = ?
			AND NOT EXISTS (SELECT 1 FROM asset_source b WHERE b.asset_id = ? AND b.source_id IS asset_source.source_id)`, p.To, p.From, p.To); err != nil {
			return fail("move the links of a shared asset", err)
		}
		// What is left of the moved asset (merged links) goes with it.
		if _, err := tx.Exec("DELETE FROM assets WHERE id = ?", p.From); err != nil {
			return fail("drop a shared asset", err)
		}
		out.Merged++
		out.Assets = append(out.Assets, p.To)
	}
	res, err := tx.Exec("UPDATE assets SET user_id = ?, modified_at = ? WHERE user_id = ?", toUserID, now, fromUserID)
	if err != nil {
		return fail("move the assets", err)
	}
	n, _ := res.RowsAffected()
	out.Moved += n

	// Findings the receiver has already: fold them into the receiver's.
	for _, t := range []struct{ table, severity string }{
		{"vulnerabilities", "severity = CASE WHEN " + findingNewer + " THEN f.severity ELSE r.severity END,"},
		{"detections", ""},
	} {
		if _, err := tx.Exec(`UPDATE `+t.table+` AS r SET `+t.severity+`
			source_label = CASE WHEN `+findingNewer+` THEN f.source_label ELSE r.source_label END,
			last_seen = CASE WHEN julianday(f.last_seen) > julianday(r.last_seen) THEN f.last_seen ELSE r.last_seen END,
			modified_at = CASE WHEN `+findingNewer+` THEN f.modified_at ELSE r.modified_at END
			FROM `+t.table+` AS f
			WHERE r.user_id = ? AND f.user_id = ? AND `+sameFinding, toUserID, fromUserID); err != nil {
			return fail("merge the shared "+t.table, err)
		}
		res, err := tx.Exec(`DELETE FROM `+t.table+` AS f WHERE f.user_id = ? AND EXISTS (
			SELECT 1 FROM `+t.table+` r WHERE r.user_id = ? AND `+sameFinding+`)`, fromUserID, toUserID)
		if err != nil {
			return fail("drop the shared "+t.table, err)
		}
		m, _ := res.RowsAffected()
		out.Merged += m
		res, err = tx.Exec("UPDATE "+t.table+" SET user_id = ?, modified_at = ? WHERE user_id = ?", toUserID, now, fromUserID)
		if err != nil {
			return fail("move the "+t.table, err)
		}
		n, _ := res.RowsAffected()
		out.Moved += n
	}

	if err := tx.Commit(); err != nil {
		return fail("commit", err)
	}
	return out, nil
}

// RecomputeAfterTransfer recomputes the receiver's assets that gained links
// in a transfer: their scan coverage and derived values.
func (db *DB) RecomputeAfterTransfer(r TransferResult) error {
	if len(r.Assets) == 0 {
		return nil
	}
	if err := db.combineScanCoverage(r.Assets); err != nil {
		return err
	}
	_, err := db.RefreshAssetsByIDs(r.Assets)
	return err
}

// linkNewer: in a merge of link a into link b, a was touched later.
const linkNewer = "julianday(COALESCE(a.modified_at, a.created_at)) > julianday(COALESCE(b.modified_at, b.created_at))"

// findingNewer: in a merge of finding f into the receiver's r, f was reported
// later.
const findingNewer = "julianday(f.modified_at) > julianday(r.modified_at)"

// sameFinding: f and r have the same identity apart from the owner (AM-DATA-47).
const sameFinding = "r.source_id = f.source_id AND r.host = f.host AND r.port = f.port AND r.name = f.name"

// DistinctUserIDs returns every user id that owns a row in this service. Used
// by SystemManager's orphan sweep; the UNION keeps it to one query and one
// pass, and it is small — the count is bounded by the number of users.
func (db *DB) DistinctUserIDs() ([]int64, error) {
	var ids []int64
	query := `
		SELECT DISTINCT user_id FROM assets
		UNION
		SELECT DISTINCT user_id FROM vulnerabilities
		UNION
		SELECT DISTINCT user_id FROM detections
	`
	if err := db.Select(&ids, query); err != nil {
		return nil, fmt.Errorf("failed to list distinct user ids: %w", err)
	}
	return ids, nil
}

// Usage is what one user holds, or the instance holds in all, as the limits
// count it (AM-DATA-45): host assets, vulnerabilities and detections.
type Usage struct {
	UserID          int64 `db:"user_id" json:"userId"`
	HostAssets      int   `db:"host_assets" json:"hostAssets"`
	Vulnerabilities int   `db:"vulnerabilities" json:"vulnerabilities"`
	Detections      int   `db:"detections" json:"detections"`
}

// UsageByUser counts every user's host assets, vulnerabilities and detections,
// and the instance's, with the same conditions as the limit checks: assets of
// type Host (CountHostAssetsByUser, CountHostAssetsAllUsers), and every
// vulnerability and detection (countFindings without a filter). A user who
// holds none of the three is not listed. The instance's Usage has UserID 0.
func (db *DB) UsageByUser() ([]Usage, Usage, error) {
	usage := []Usage{}
	err := db.Select(&usage, `
		SELECT user_id, SUM(host_assets) AS host_assets, SUM(vulnerabilities) AS vulnerabilities, SUM(detections) AS detections
		FROM (
			SELECT user_id, COUNT(*) AS host_assets, 0 AS vulnerabilities, 0 AS detections FROM assets WHERE type = 'Host' GROUP BY user_id
			UNION ALL
			SELECT user_id, 0, COUNT(*), 0 FROM vulnerabilities GROUP BY user_id
			UNION ALL
			SELECT user_id, 0, 0, COUNT(*) FROM detections GROUP BY user_id
		)
		GROUP BY user_id
		ORDER BY user_id`)
	if err != nil {
		return nil, Usage{}, fmt.Errorf("failed to count the usage per user: %w", err)
	}
	var total Usage
	for _, u := range usage {
		total.HostAssets += u.HostAssets
		total.Vulnerabilities += u.Vulnerabilities
		total.Detections += u.Detections
	}
	return usage, total, nil
}
