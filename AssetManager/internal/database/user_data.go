// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// A user's data as a whole: deleting it when the account goes, moving it to
// another user (an anonymous session that registers), and listing which users
// still hold any, for SystemManager's orphan sweep.

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
