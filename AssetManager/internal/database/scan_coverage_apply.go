// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Applying a scanner's scan coverage table to the host assets it covers.
//
// A coverage table says which targets a scanner scans on a schedule, and how
// often: a target is an address, a CIDR network, an address range or a
// hostname. Unlike an asset listing it names no hosts it has seen, so it is
// applied differently:
//
//   - an address names one host asset, which is created when it is missing;
//   - a network or range covers every existing host asset whose name is an
//     address inside it, and creates none;
//   - a hostname is skipped — host assets are named by address only.
//
// Each covered asset is linked to the table's source with counts_as_seen = 0,
// so the link carries the coverage without counting as a sighting: an asset's
// last seen is not moved by a schedule saying the host would be scanned. The
// link's own last_seen is the time it was applied; like every link it carries
// modified_at, by which a re-import of the same source finds the links that
// are no longer in the table.
//
// Where the same source has listed the asset itself, its link stays a sighting
// and only gets the coverage.

package database

import (
	"database/sql"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// CoverageEntry is one row of a coverage table.
type CoverageEntry struct {
	Target       string
	IntervalDays int
	VerifiedAt   *time.Time
}

// CoverageOutcome is what applying a table did.
type CoverageOutcome struct {
	// Covered is the IDs of the host assets that got the coverage.
	Covered []int64 `json:"covered"`
	// Created is the IDs of host assets created for an address not known yet.
	Created []int64 `json:"created"`
	// Hostnames is how many entries named a host rather than an address.
	Hostnames int `json:"hostnames"`
	// Unmatched is how many networks and ranges covered no known host asset.
	Unmatched int `json:"unmatched"`
	// Invalid is how many entries could not be read: a target that is no
	// address form or hostname, or an interval below 1.
	Invalid int `json:"invalid"`
	// Refused is how many addresses were not created because the host asset
	// limit was reached.
	Refused int `json:"refused"`
}

// coverageTarget is a target read as an address span, or a hostname.
type coverageTarget struct {
	lo, hi   netip.Addr
	single   bool
	hostname bool
}

// parseCoverageTarget reads a target in the table's forms: "10.0.0.5",
// "10.0.0.0/24", "10.0.0.1-10.0.0.9" or a hostname.
func parseCoverageTarget(s string) (coverageTarget, bool) {
	s = strings.TrimSpace(s)
	if a, err := netip.ParseAddr(s); err == nil {
		a = a.WithZone("").Unmap()
		return coverageTarget{lo: a, hi: a, single: true}, true
	}
	if p, err := netip.ParsePrefix(s); err == nil {
		p = p.Masked()
		if p.IsSingleIP() {
			return coverageTarget{lo: p.Addr(), hi: p.Addr(), single: true}, true
		}
		return coverageTarget{lo: p.Addr(), hi: prefixLast(p)}, true
	}
	if i := strings.Index(s, "-"); i > 0 {
		lo, err1 := netip.ParseAddr(strings.TrimSpace(s[:i]))
		hi, err2 := netip.ParseAddr(strings.TrimSpace(s[i+1:]))
		if err1 == nil && err2 == nil {
			if lo.Is4() != hi.Is4() || hi.Less(lo) {
				return coverageTarget{}, false
			}
			return coverageTarget{lo: lo, hi: hi, single: lo == hi}, true
		}
	}
	if s != "" && !strings.ContainsAny(s, " \t|/,") {
		return coverageTarget{hostname: true}, true
	}
	return coverageTarget{}, false
}

func prefixLast(p netip.Prefix) netip.Addr {
	b := p.Addr().As16()
	bits := p.Bits()
	if p.Addr().Is4() {
		bits += 96
	}
	for i := bits; i < 128; i++ {
		b[i/8] |= 1 << (7 - uint(i%8))
	}
	a := netip.AddrFrom16(b)
	if p.Addr().Is4() {
		a = a.Unmap()
	}
	return a
}

func (t coverageTarget) contains(a netip.Addr) bool {
	return a.Is4() == t.lo.Is4() && !a.Less(t.lo) && !t.hi.Less(a)
}

// CoverageAllowance decides whether one more host asset may be created; it
// is how the host asset limits reach ApplyScanCoverage.
type CoverageAllowance func() bool

// ApplyScanCoverage applies a coverage table, from the given source, to the
// host assets of userID. allow is asked before each host asset is created;
// nil allows all.
func (db *DB) ApplyScanCoverage(userID, sourceID int64, sourceLabel string, entries []CoverageEntry, allow CoverageAllowance) (*CoverageOutcome, error) {
	out := &CoverageOutcome{Covered: []int64{}, Created: []int64{}}
	if sourceID <= 0 && sourceLabel == "" {
		return nil, fmt.Errorf("a source is required")
	}

	var hosts []struct {
		ID   int64  `db:"id"`
		Name string `db:"name"`
	}
	if err := db.Select(&hosts, "SELECT id, name FROM assets WHERE user_id = ? AND type = 'Host'", userID); err != nil {
		return nil, fmt.Errorf("failed to read host assets: %w", err)
	}
	type known struct {
		id   int64
		addr netip.Addr
	}
	var byAddr []known
	addrIndex := map[netip.Addr]int64{}
	for _, h := range hosts {
		if a, err := netip.ParseAddr(strings.TrimSpace(h.Name)); err == nil {
			a = a.WithZone("").Unmap()
			byAddr = append(byAddr, known{h.ID, a})
			if _, dup := addrIndex[a]; !dup {
				addrIndex[a] = h.ID
			}
		}
	}

	// What each asset gets: the shortest interval and the newest verification
	// over every entry covering it.
	type coverage struct {
		interval int
		verified *time.Time
	}
	got := map[int64]*coverage{}
	var order []int64
	give := func(id int64, e CoverageEntry) {
		c, ok := got[id]
		if !ok {
			c = &coverage{interval: e.IntervalDays}
			got[id] = c
			order = append(order, id)
		}
		if e.IntervalDays < c.interval {
			c.interval = e.IntervalDays
		}
		if e.VerifiedAt != nil && (c.verified == nil || e.VerifiedAt.After(*c.verified)) {
			v := *e.VerifiedAt
			c.verified = &v
		}
	}

	now := time.Now()
	for _, e := range entries {
		t, ok := parseCoverageTarget(e.Target)
		if !ok || e.IntervalDays < 1 {
			out.Invalid++
			continue
		}
		switch {
		case t.hostname:
			out.Hostnames++
		case t.single:
			id, ok := addrIndex[t.lo]
			if !ok {
				if allow != nil && !allow() {
					out.Refused++
					continue
				}
				// Not seen, only scheduled: the asset keeps the date it was
				// created with as its last seen until something sees it.
				created := now
				if e.VerifiedAt != nil {
					created = *e.VerifiedAt
				}
				res, err := db.Exec(
					"INSERT INTO assets (name, type, severity, score, user_id, created_at, modified_at, last_seen) VALUES (?, 'Host', 0, 0, ?, ?, ?, ?)",
					t.lo.String(), userID, now, now, created)
				if err != nil {
					return nil, fmt.Errorf("failed to create host asset %s: %w", t.lo, err)
				}
				if id, err = res.LastInsertId(); err != nil {
					return nil, fmt.Errorf("failed to get asset ID: %w", err)
				}
				addrIndex[t.lo] = id
				byAddr = append(byAddr, known{id, t.lo})
				out.Created = append(out.Created, id)
			}
			give(id, e)
		default:
			matched := false
			for _, k := range byAddr {
				if t.contains(k.addr) {
					give(k.id, e)
					matched = true
				}
			}
			if !matched {
				out.Unmatched++
			}
		}
	}

	for _, id := range order {
		c := got[id]
		if err := db.linkCoverage(id, sourceID, sourceLabel, now, c.verified, c.interval); err != nil {
			return nil, err
		}
		out.Covered = append(out.Covered, id)
	}
	if err := db.combineScanCoverage(order); err != nil {
		return nil, err
	}
	if len(order) > 0 {
		if _, err := db.RefreshAssetsByIDs(order); err != nil {
			return nil, err
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(order)), ",")
		args := []interface{}{now}
		for _, id := range order {
			args = append(args, id)
		}
		if _, err := db.Exec("UPDATE assets SET modified_at = ? WHERE id IN ("+placeholders+")", args...); err != nil {
			return nil, fmt.Errorf("failed to mark assets modified: %w", err)
		}
	}
	return out, nil
}

// linkCoverage records a source's coverage of an asset on their link,
// creating the link as a coverage-only one (counts_as_seen = 0) when there is
// none. The verification is replaced only when the table states one.
func (db *DB) linkCoverage(assetID, sourceID int64, sourceLabel string, now time.Time, verified *time.Time, interval int) error {
	where, whereArgs := "asset_id = ? AND source_id = ?", []interface{}{assetID, sourceID}
	if sourceID <= 0 {
		where, whereArgs = "asset_id = ? AND source_id IS NULL AND source_label = ?", []interface{}{assetID, sourceLabel}
	}
	var countsAsSeen int
	err := db.Get(&countsAsSeen, "SELECT counts_as_seen FROM asset_source WHERE "+where, whereArgs...)
	switch {
	case err == sql.ErrNoRows:
		var sid interface{}
		if sourceID > 0 {
			sid = sourceID
		}
		if _, err := db.Exec(`INSERT INTO asset_source
			(asset_id, source_id, source_label, last_seen, created_at, modified_at, scan_coverage_verified_at, scan_coverage_interval_days, counts_as_seen)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`,
			assetID, sid, sourceLabel, now, now, now, verified, interval); err != nil {
			return fmt.Errorf("failed to link asset %d to its coverage source: %w", assetID, err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("failed to look up asset_source: %w", err)
	}

	sets := "scan_coverage_interval_days = ?, modified_at = ?"
	args := []interface{}{interval, now}
	if verified != nil {
		sets += ", scan_coverage_verified_at = ?"
		args = append(args, *verified)
	}
	if countsAsSeen == 0 {
		// A coverage link's date is when it was applied; a sighting's date is
		// when the host was seen and is left alone.
		sets += ", last_seen = ?"
		args = append(args, now)
	}
	if _, err := db.Exec("UPDATE asset_source SET "+sets+" WHERE "+where, append(args, whereArgs...)...); err != nil {
		return fmt.Errorf("failed to set the coverage of asset %d: %w", assetID, err)
	}
	return nil
}
