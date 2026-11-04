// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The asset types: a fixed set, for now Host only. An asset of any other type
// is refused, so nothing is stored that the type-specific behaviour — last
// scanned, the host-asset limit and licence cap, scan coverage, the dashboard's
// counts — does not know how to treat. Adding a type is a deliberate change to
// the specification (spec/02-data-model-and-behaviour.md, AM-DATA-02 and
// AM-DATA-03), which must then say what those mean for it.
//
// A case variant is accepted as the canonical spelling: imports have sent
// "host", and the limits compare against "Host" exactly.

package database

import (
	"errors"
	"fmt"
	"strings"
)

// AssetTypeHost is a machine, named by its address.
const AssetTypeHost = "Host"

// AssetTypes lists every asset type, in canonical spelling.
var AssetTypes = []string{AssetTypeHost}

// ErrUnknownAssetType is returned for a type outside AssetTypes.
var ErrUnknownAssetType = errors.New("unknown asset type")

// CanonicalAssetType returns the canonical spelling of an asset type, matched
// ignoring case and surrounding space, and whether it is one at all.
func CanonicalAssetType(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	for _, t := range AssetTypes {
		if strings.EqualFold(raw, t) {
			return t, true
		}
	}
	return "", false
}

// unknownAssetType is the error for a type outside AssetTypes, naming it and
// the known ones.
func unknownAssetType(raw string) error {
	return fmt.Errorf("%w %q (known: %s)", ErrUnknownAssetType, raw, strings.Join(AssetTypes, ", "))
}

// AssetsOfUnknownType counts the stored assets per type outside AssetTypes —
// rows written before the set was fixed. Nothing changes them; startup
// reports them.
func (db *DB) AssetsOfUnknownType() (map[string]int, error) {
	placeholders := make([]string, len(AssetTypes))
	args := make([]interface{}, len(AssetTypes))
	for i, t := range AssetTypes {
		placeholders[i] = "?"
		args[i] = t
	}
	var rows []struct {
		Type  string `db:"type"`
		Count int    `db:"n"`
	}
	if err := db.Select(&rows, "SELECT type, COUNT(*) AS n FROM assets WHERE type NOT IN ("+strings.Join(placeholders, ",")+") GROUP BY type", args...); err != nil {
		return nil, fmt.Errorf("failed to count assets of unknown type: %w", err)
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.Type] = r.Count
	}
	return out, nil
}
