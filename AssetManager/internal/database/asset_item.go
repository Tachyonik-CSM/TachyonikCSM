// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// One asset by its id: read it, or change its type. Both are scoped to the user
// IDs the caller may see, so an id belonging to anyone else is "not found".
//
// Only the type can be changed. An asset's severity and score are computed from
// its vulnerabilities and detections (asset_refresh.go), and its name
// is what those are joined on (vulnerabilities.host = assets.name), so changing
// any of them would be overwritten, or would silently detach the findings.

package database

import (
	"errors"
	"fmt"
	"time"
)

// ErrAssetNotFound is returned for an id that does not exist or is not visible.
var ErrAssetNotFound = errors.New("asset not found")

// GetAssetForUsers returns asset id if it belongs to one of userIDs.
func (db *DB) GetAssetForUsers(id int64, userIDs []int64) (*Asset, error) {
	if len(userIDs) == 0 {
		return nil, ErrAssetNotFound
	}
	clause, args := buildUserIDsClause(userIDs)
	var assets []Asset
	if err := db.Select(&assets, "SELECT "+assetColumns+" FROM assets WHERE id = ? AND "+clause,
		append([]interface{}{id}, args...)...); err != nil {
		return nil, fmt.Errorf("failed to get asset %d: %w", id, err)
	}
	if len(assets) == 0 {
		return nil, ErrAssetNotFound
	}
	if err := db.populateAssetSources(assets); err != nil {
		return nil, err
	}
	return &assets[0], nil
}

// UpdateAssetTypeForUsers sets the type of asset id if it belongs to one of
// userIDs, and returns the asset as it now is.
func (db *DB) UpdateAssetTypeForUsers(id int64, userIDs []int64, assetType string) (*Asset, error) {
	if len(userIDs) == 0 {
		return nil, ErrAssetNotFound
	}
	clause, args := buildUserIDsClause(userIDs)
	res, err := db.Exec("UPDATE assets SET type = ?, modified_at = ? WHERE id = ? AND "+clause,
		append([]interface{}{assetType, time.Now(), id}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("failed to update asset %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrAssetNotFound
	}
	// Only a Host has a last_scanned, so a type change sets or clears it.
	if err := db.refreshLastScanned("id = ?", id); err != nil {
		return nil, err
	}
	return db.GetAssetForUsers(id, userIDs)
}
