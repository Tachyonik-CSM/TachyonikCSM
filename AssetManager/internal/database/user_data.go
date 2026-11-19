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

// DeleteAllUserData deletes all assets, vulnerabilities, and detections for a user
func (db *DB) DeleteAllUserData(userID int64) (int64, error) {
	var total int64

	result, err := db.Exec("DELETE FROM assets WHERE user_id = ?", userID)
	if err == nil {
		n, _ := result.RowsAffected()
		total += n
	}

	result, err = db.Exec("DELETE FROM vulnerabilities WHERE user_id = ?", userID)
	if err == nil {
		n, _ := result.RowsAffected()
		total += n
	}

	result, err = db.Exec("DELETE FROM detections WHERE user_id = ?", userID)
	if err == nil {
		n, _ := result.RowsAffected()
		total += n
	}

	return total, nil
}

// TransferAllUserData reassigns all assets, vulnerabilities, and detections from one user to another
func (db *DB) TransferAllUserData(fromUserID, toUserID int64) (int64, error) {
	var total int64
	now := time.Now()

	result, err := db.Exec(
		"UPDATE assets SET user_id = ?, modified_at = ? WHERE user_id = ?",
		toUserID, now, fromUserID,
	)
	if err == nil {
		n, _ := result.RowsAffected()
		total += n
	}

	result, err = db.Exec(
		"UPDATE vulnerabilities SET user_id = ?, modified_at = ? WHERE user_id = ?",
		toUserID, now, fromUserID,
	)
	if err == nil {
		n, _ := result.RowsAffected()
		total += n
	}

	result, err = db.Exec(
		"UPDATE detections SET user_id = ?, modified_at = ? WHERE user_id = ?",
		toUserID, now, fromUserID,
	)
	if err == nil {
		n, _ := result.RowsAffected()
		total += n
	}

	return total, nil
}

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
