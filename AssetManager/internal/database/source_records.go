// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// What the sources' sweep reads and removes (AM-PUR-20, AM-DATA-46): the
// source ids the records still refer to, so each can be checked against
// ResourceManager, and the records that refer to no source id at all — links
// and findings known by a label only, findings entered by hand, assets left
// without a link — which no source can ever remove.

package database

import (
	"fmt"
)

// SourceIDsInUse returns every source id a link or a finding refers to, in
// order.
func (db *DB) SourceIDsInUse() ([]int64, error) {
	ids := []int64{}
	err := db.Select(&ids, `
		SELECT source_id FROM asset_source WHERE source_id IS NOT NULL
		UNION SELECT source_id FROM vulnerabilities WHERE source_id IS NOT NULL
		UNION SELECT source_id FROM detections WHERE source_id IS NOT NULL
		ORDER BY 1`)
	if err != nil {
		return nil, fmt.Errorf("failed to list the source ids in use: %w", err)
	}
	return ids, nil
}

// SourcelessRemoval is what DeleteSourcelessRecords removed.
type SourcelessRemoval struct {
	Vulnerabilities []Owned
	Detections      []Owned
	// Hosts are the hosts of the removed findings, to recompute.
	Hosts []HostRef
	// DeletedAssets lost their last link or had none; KeptAssets lost a
	// label-only link and keep others.
	DeletedAssets []Owned
	KeptAssets    []Owned
}

// DeleteSourcelessRecords removes, of every owner, the findings without a
// source id, the links without a source id, and then every asset left without
// a link, recomputing those that keep one.
func (db *DB) DeleteSourcelessRecords() (SourcelessRemoval, error) {
	out := SourcelessRemoval{Vulnerabilities: []Owned{}, Detections: []Owned{}, Hosts: []HostRef{}}
	if err := db.Select(&out.Hosts, `
		SELECT DISTINCT host, user_id FROM vulnerabilities WHERE source_id IS NULL
		UNION SELECT DISTINCT host, user_id FROM detections WHERE source_id IS NULL`); err != nil {
		return out, fmt.Errorf("failed to find the hosts of findings without a source: %w", err)
	}
	for _, f := range []struct {
		table string
		into  *[]Owned
	}{{"vulnerabilities", &out.Vulnerabilities}, {"detections", &out.Detections}} {
		if err := db.Select(f.into, "SELECT id, user_id FROM "+f.table+" WHERE source_id IS NULL ORDER BY id"); err != nil {
			return out, fmt.Errorf("failed to find %s without a source: %w", f.table, err)
		}
		if _, err := db.Exec("DELETE FROM " + f.table + " WHERE source_id IS NULL"); err != nil {
			return out, fmt.Errorf("failed to delete %s without a source: %w", f.table, err)
		}
	}

	// The assets concerned: those with a label-only link, and those with no
	// link at all.
	var candidates []int64
	if err := db.Select(&candidates, `
		SELECT DISTINCT asset_id FROM asset_source WHERE source_id IS NULL
		UNION SELECT id FROM assets a WHERE NOT EXISTS (SELECT 1 FROM asset_source s WHERE s.asset_id = a.id)
		ORDER BY 1`); err != nil {
		return out, fmt.Errorf("failed to find assets without a source: %w", err)
	}
	if _, err := db.Exec("DELETE FROM asset_source WHERE source_id IS NULL"); err != nil {
		return out, fmt.Errorf("failed to delete links without a source: %w", err)
	}
	deleted, kept, err := db.deleteOrphanAssets(candidates, 0)
	if err != nil {
		return out, err
	}
	out.DeletedAssets, out.KeptAssets = deleted, kept
	return out, nil
}

// Empty reports whether nothing was removed.
func (r SourcelessRemoval) Empty() bool {
	return len(r.Vulnerabilities) == 0 && len(r.Detections) == 0 && len(r.DeletedAssets) == 0 && len(r.KeptAssets) == 0
}
