// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Recomputing what an asset shows from what still refers to it.
//
// An asset's severity, score, last seen and last scanned are not entered; they
// follow from its evidence — the sources that list it (asset_source) and the
// vulnerabilities and detections on its host. Whenever evidence arrives or goes
// away, the assets it belongs to are recomputed here, in one place and by one
// rule:
//
//   - severity: the highest severity of its vulnerabilities, 0 without any
//   - score: 100 - severity; 100 with only zero-severity findings or
//     detections; 0 with no findings at all (unmanaged)
//   - last_seen: the newest last seen of anything that refers to it — any
//     source listing it, any vulnerability, any detection. A link that only
//     records scan coverage (counts_as_seen = 0) is not a sighting and does not
//     count. Kept as it is when nothing counts — an asset about to be deleted,
//     or one known only from a scan schedule, which keeps the date it was
//     created with
//   - last_scanned: the newest last seen of its vulnerabilities and
//     detections, Host assets only (refreshLastScanned)
//
// It used to be spread over three per-host functions that matched assets by
// host name and the caller's user ID. Called on behalf of a service — user ID
// 0, as when ResourceManager deletes a source — they matched no asset at all,
// so nothing was recomputed; and last seen ignored the sources, so an asset
// kept the date of a source deleted long ago.

package database

import (
	"fmt"
	"strings"
	"time"
)

// HostRef names the assets of one host of one user: what a vulnerability or a
// detection refers to.
type HostRef struct {
	Host   string `db:"host"`
	UserID int64  `db:"user_id"`
}

const (
	refreshSeverity = `COALESCE((SELECT MAX(v.severity) FROM vulnerabilities v
		WHERE v.host = assets.name AND v.user_id = assets.user_id), 0)`
	refreshScore = `(CASE
		WHEN ` + refreshSeverity + ` > 0 THEN 100 - ` + refreshSeverity + `
		WHEN EXISTS (SELECT 1 FROM vulnerabilities v WHERE v.host = assets.name AND v.user_id = assets.user_id)
		  OR EXISTS (SELECT 1 FROM detections d WHERE d.host = assets.name AND d.user_id = assets.user_id) THEN 100
		ELSE 0 END)`
	// The newest is picked by julianday() because last_seen is stored in more
	// than one layout; the value itself is copied as stored.
	refreshLastSeen = `COALESCE((SELECT ls FROM (
		SELECT s.last_seen AS ls FROM asset_source s WHERE s.asset_id = assets.id AND s.counts_as_seen = 1
		UNION ALL
		SELECT v.last_seen FROM vulnerabilities v WHERE v.host = assets.name AND v.user_id = assets.user_id
		UNION ALL
		SELECT d.last_seen FROM detections d WHERE d.host = assets.name AND d.user_id = assets.user_id
	) ORDER BY julianday(ls) DESC LIMIT 1), assets.last_seen)`
)

// refreshAssets recomputes the assets matching where (a condition on the
// assets table, with args) and returns the IDs of those whose severity, score
// or last seen changed. modified_at moves only for those. last_scanned is
// recomputed as well (it bumps modified_at itself where it changes).
func (db *DB) refreshAssets(where string, args ...interface{}) ([]int64, error) {
	var changed []int64
	if err := db.Select(&changed, `SELECT id FROM assets WHERE (`+where+`)
		AND (severity IS NOT `+refreshSeverity+`
		  OR score IS NOT `+refreshScore+`
		  OR last_seen IS NOT `+refreshLastSeen+`)`, args...); err != nil {
		return nil, fmt.Errorf("failed to find assets to refresh: %w", err)
	}
	if len(changed) > 0 {
		placeholders := make([]string, len(changed))
		updArgs := make([]interface{}, 0, len(changed)+1)
		updArgs = append(updArgs, time.Now())
		for i, id := range changed {
			placeholders[i] = "?"
			updArgs = append(updArgs, id)
		}
		if _, err := db.Exec(`UPDATE assets SET
			severity = `+refreshSeverity+`,
			score = `+refreshScore+`,
			last_seen = `+refreshLastSeen+`,
			modified_at = ?
			WHERE id IN (`+strings.Join(placeholders, ",")+`)`, updArgs...); err != nil {
			return nil, fmt.Errorf("failed to refresh assets: %w", err)
		}
	}
	if err := db.refreshLastScanned(where, args...); err != nil {
		return nil, err
	}
	return changed, nil
}

// RefreshAssetsByIDs recomputes the given assets; see refreshAssets.
func (db *DB) RefreshAssetsByIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return []int64{}, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return db.refreshAssets("id IN ("+strings.Join(placeholders, ",")+")", args...)
}

// RefreshAssetsForHosts recomputes the assets each ref names — every asset of
// that user named like the host, as the findings are joined on — and returns
// the IDs of those that changed.
func (db *DB) RefreshAssetsForHosts(refs []HostRef) ([]int64, error) {
	changed := []int64{}
	seen := make(map[HostRef]bool, len(refs))
	for _, ref := range refs {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		ids, err := db.refreshAssets("name = ? AND user_id = ?", ref.Host, ref.UserID)
		if err != nil {
			return changed, err
		}
		changed = append(changed, ids...)
	}
	return changed, nil
}

// RefreshAllAssets recomputes every asset. Run at startup, so values left by
// an older version — or by anything that changed the evidence without asking
// for a refresh — are put right.
func (db *DB) RefreshAllAssets() ([]int64, error) {
	return db.refreshAssets("1 = 1")
}

// hostRefs reads the distinct (host, user) pairs of the given query, which
// selects host and user_id.
func (db *DB) hostRefs(query string, args ...interface{}) ([]HostRef, error) {
	var refs []HostRef
	if err := db.Select(&refs, query, args...); err != nil {
		return nil, fmt.Errorf("failed to read affected hosts: %w", err)
	}
	return refs, nil
}

// VulnerabilityHostRefsBySource is the hosts a source's vulnerabilities refer
// to; userID 0 means every user's (the internal service path).
func (db *DB) VulnerabilityHostRefsBySource(sourceID int64, userID int64) ([]HostRef, error) {
	return db.findingHostRefs(vulnerabilityKind, sourceID, userID, nil)
}

// StaleVulnerabilityHostRefsBySource is the hosts the vulnerabilities a
// re-import is about to drop refer to.
func (db *DB) StaleVulnerabilityHostRefsBySource(sourceID int64, modifiedBefore time.Time) ([]HostRef, error) {
	return db.findingHostRefs(vulnerabilityKind, sourceID, 0, &modifiedBefore)
}

// DetectionHostRefsBySource is the hosts a source's detections refer to;
// userID 0 means every user's.
func (db *DB) DetectionHostRefsBySource(sourceID int64, userID int64) ([]HostRef, error) {
	return db.findingHostRefs(detectionKind, sourceID, userID, nil)
}

// StaleDetectionHostRefsBySource is the hosts the detections a re-import is
// about to drop refer to.
func (db *DB) StaleDetectionHostRefsBySource(sourceID int64, modifiedBefore time.Time) ([]HostRef, error) {
	return db.findingHostRefs(detectionKind, sourceID, 0, &modifiedBefore)
}

// findingHostRefs is the hosts, with their owners, of the findings in a
// source's scope (see sourceScope).
func (db *DB) findingHostRefs(k findingKind, sourceID int64, userID int64, modifiedBefore *time.Time) ([]HostRef, error) {
	where, args := sourceScope(sourceID, userID, modifiedBefore)
	return db.hostRefs("SELECT DISTINCT host, user_id FROM "+k.table+" WHERE "+where, args...)
}

// HostRefsOf names one user's hosts as refs.
func HostRefsOf(hosts []string, userID int64) []HostRef {
	refs := make([]HostRef, len(hosts))
	for i, h := range hosts {
		refs[i] = HostRef{Host: h, UserID: userID}
	}
	return refs
}
