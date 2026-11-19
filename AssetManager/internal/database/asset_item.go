// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// One asset by its id, scoped to the user IDs the caller may see, so an id
// belonging to anyone else is "not found". Nothing of an asset is written
// here: its name and type are set when an import creates it, and its severity
// and score are computed from its findings (asset_refresh.go; AM-DATA-06).

package database

import (
	"errors"
	"fmt"
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
