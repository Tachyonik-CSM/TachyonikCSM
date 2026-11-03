// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Assets: the row type, its sources (asset_source), and its queries — listing
// and counting, creating one or linking an existing one to another source,
// deleting by ID, by source or what a re-import left stale, unlinking a
// source, and the scan coverage and last-scanned values kept on each.

package database

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// GetUserIDsByAssetSource returns distinct user IDs that have assets linked
// to the given source_id via the asset_source junction.
func (db *DB) GetUserIDsByAssetSource(sourceID int64, userID int64) ([]int64, error) {
	if userID != 0 {
		return []int64{userID}, nil
	}
	var userIDs []int64
	err := db.Select(&userIDs, `
		SELECT DISTINCT a.user_id FROM assets a
		INNER JOIN asset_source s ON s.asset_id = a.id
		WHERE s.source_id = ?
	`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user IDs by asset source: %w", err)
	}
	return userIDs, nil
}

// GetUserIDsByStaleAssetSource returns distinct user IDs that have
// asset_source rows for the given source_id whose junction last_seen is older
// than modifiedBefore (i.e. the source didn't re-observe them on re-import).
func (db *DB) GetUserIDsByStaleAssetSource(sourceID int64, modifiedBefore time.Time) ([]int64, error) {
	var userIDs []int64
	err := db.Select(&userIDs, `
		SELECT DISTINCT a.user_id FROM assets a
		INNER JOIN asset_source s ON s.asset_id = a.id
		WHERE s.source_id = ? AND s.last_seen < ?
	`, sourceID, modifiedBefore)
	if err != nil {
		return nil, fmt.Errorf("failed to get user IDs by stale asset source: %w", err)
	}
	return userIDs, nil
}

// Asset represents an asset in the database. Identity is (user_id, name, type) —
// the source-of-observation lives in the asset_source junction table and is
// surfaced here as Sources + SourcesLabel (populated by the list/fetch paths).
type Asset struct {
	ID         int64     `json:"id" db:"id"`
	Name       string    `json:"name" db:"name"`
	Type       string    `json:"type" db:"type"`
	Severity   int       `json:"severity" db:"severity"`
	Score      int       `json:"score" db:"score"`
	UserID     int64     `json:"userId" db:"user_id"`
	CreatedAt  time.Time `json:"createdAt" db:"created_at"`
	ModifiedAt time.Time `json:"modifiedAt" db:"modified_at"`
	LastSeen   time.Time `json:"lastSeen" db:"last_seen"`
	// LastScanned is the newest last_seen of the host's vulnerabilities and
	// detections; nil when it has none, and always nil for other types.
	LastScanned *time.Time `json:"lastScanned" db:"last_scanned"`
	// The scan coverage, as imports report it: when it was last verified that
	// the asset is in a scheduled vulnerability scan, and the shortest
	// interval in days of such a scan. Only ever set by an import (see
	// SetAssetScanCoverage) — never derived from findings or anything else.
	// Combined over the asset's sources; nil until a source reports it.
	LastScanCoverageVerification *time.Time    `json:"lastScanCoverageVerification" db:"last_scan_coverage_verification"`
	ScanCoverageIntervalDays     *int          `json:"scanCoverageIntervalDays" db:"scan_coverage_interval_days"`
	Sources                      []AssetSource `json:"sources" db:"-"`
	SourcesLabel                 string        `json:"sourcesLabel" db:"-"`
}

// AssetSource is one row of the asset_source junction table.
type AssetSource struct {
	SourceID    *int64    `json:"sourceId" db:"source_id"`
	SourceLabel string    `json:"sourceLabel" db:"source_label"`
	LastSeen    time.Time `json:"lastSeen" db:"last_seen"`
}

// assetColumns is the column list for asset SELECTs (sources are filled in
// post-hoc by populateAssetSources).
const assetColumns = "id, name, type, severity, score, user_id, created_at, modified_at, last_seen, last_scanned, last_scan_coverage_verification, scan_coverage_interval_days"

// populateAssetSources fills the Sources + SourcesLabel fields on the given
// assets with one extra query. Uses a single IN() lookup so it's O(1) extra
// round-trips regardless of list size.
func (db *DB) populateAssetSources(assets []Asset) error {
	if len(assets) == 0 {
		return nil
	}
	ids := make([]int64, len(assets))
	idx := make(map[int64]int, len(assets))
	for i := range assets {
		ids[i] = assets[i].ID
		idx[assets[i].ID] = i
		assets[i].Sources = []AssetSource{} // default to empty, never nil
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(`
		SELECT asset_id, source_id, source_label, last_seen
		FROM asset_source
		WHERE asset_id IN (%s)
		ORDER BY source_label ASC
	`, strings.Join(placeholders, ","))

	type row struct {
		AssetID     int64     `db:"asset_id"`
		SourceID    *int64    `db:"source_id"`
		SourceLabel string    `db:"source_label"`
		LastSeen    time.Time `db:"last_seen"`
	}
	var rows []row
	if err := db.Select(&rows, query, args...); err != nil {
		return fmt.Errorf("failed to load asset sources: %w", err)
	}

	for _, r := range rows {
		i, ok := idx[r.AssetID]
		if !ok {
			continue
		}
		assets[i].Sources = append(assets[i].Sources, AssetSource{
			SourceID:    r.SourceID,
			SourceLabel: r.SourceLabel,
			LastSeen:    r.LastSeen,
		})
	}

	// Precompute the comma-separated label for the list view. A link without
	// a label is rendered as "(unknown)" so the UI doesn't show a trailing
	// comma.
	for i := range assets {
		labels := make([]string, 0, len(assets[i].Sources))
		for _, s := range assets[i].Sources {
			if s.SourceLabel == "" {
				labels = append(labels, "(unknown)")
			} else {
				labels = append(labels, s.SourceLabel)
			}
		}
		assets[i].SourcesLabel = strings.Join(labels, ", ")
	}
	return nil
}

// GetAllAssetsByUsers returns all assets for multiple users
func (db *DB) GetAllAssetsByUsers(userIDs []int64) ([]Asset, error) {
	clause, args := buildUserIDsClause(userIDs)
	var assets []Asset
	err := db.Select(&assets, "SELECT "+assetColumns+" FROM assets WHERE "+clause+" ORDER BY created_at DESC", args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get assets: %w", err)
	}
	if err := db.populateAssetSources(assets); err != nil {
		return nil, err
	}
	return assets, nil
}

// CountAssetsByUsers returns the total number of assets for multiple users
func (db *DB) CountAssetsByUsers(userIDs []int64) (int, error) {
	clause, args := buildUserIDsClause(userIDs)
	var count int
	err := db.Get(&count, "SELECT COUNT(*) FROM assets WHERE "+clause, args...)
	if err != nil {
		return 0, fmt.Errorf("failed to count assets: %w", err)
	}
	return count, nil
}

// GetAssetsByUsersWithFilter returns assets for multiple users with optional filtering
func (db *DB) GetAssetsByUsersWithFilter(userIDs []int64, filter *AssetFilter) ([]Asset, error) {
	if filter == nil || len(filter.Rules) == 0 {
		return db.GetAllAssetsByUsers(userIDs)
	}

	clause, args := buildUserIDsClause(userIDs)
	query := "SELECT " + assetColumns + " FROM assets WHERE " + clause

	filterSQL, filterArgs := buildAssetFilterSQL(filter, userIDs)
	if filterSQL != "" {
		query += " AND (" + filterSQL + ")"
		args = append(args, filterArgs...)
	}

	query += " ORDER BY severity DESC, created_at DESC"

	var assets []Asset
	err := db.Select(&assets, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get filtered assets: %w", err)
	}
	if err := db.populateAssetSources(assets); err != nil {
		return nil, err
	}
	return assets, nil
}

// CountAssetsByUserWithFilter returns the count of assets matching the filter
func (db *DB) CountAssetsByUserWithFilter(userID int64, filter *AssetFilter) (int, error) {
	return db.CountAssetsByUsersWithFilter([]int64{userID}, filter)
}

// CountAssetsByUsersWithFilter returns the count of assets for multiple users matching the filter
func (db *DB) CountAssetsByUsersWithFilter(userIDs []int64, filter *AssetFilter) (int, error) {
	if filter == nil || len(filter.Rules) == 0 {
		return db.CountAssetsByUsers(userIDs)
	}

	clause, args := buildUserIDsClause(userIDs)
	query := "SELECT COUNT(*) FROM assets WHERE " + clause

	filterSQL, filterArgs := buildAssetFilterSQL(filter, userIDs)
	if filterSQL != "" {
		query += " AND (" + filterSQL + ")"
		args = append(args, filterArgs...)
	}

	var count int
	err := db.Get(&count, query, args...)
	if err != nil {
		return 0, fmt.Errorf("failed to count filtered assets: %w", err)
	}
	return count, nil
}

// CountHostAssetsByUser returns the number of Host type assets for a specific user
func (db *DB) CountHostAssetsByUser(userID int64) (int, error) {
	var count int
	err := db.Get(&count, "SELECT COUNT(*) FROM assets WHERE user_id = ? AND type = 'Host'", userID)
	if err != nil {
		return 0, fmt.Errorf("failed to count host assets: %w", err)
	}
	return count, nil
}

// CountHostAssetsAllUsers returns the number of Host type assets across every
// user on this instance. Used to enforce a per-appliance license cap that
// applies instance-wide (the licensed asset count is for the appliance, not a
// single user).
func (db *DB) CountHostAssetsAllUsers() (int, error) {
	var count int
	err := db.Get(&count, "SELECT COUNT(*) FROM assets WHERE type = 'Host'")
	if err != nil {
		return 0, fmt.Errorf("failed to count host assets: %w", err)
	}
	return count, nil
}

// DeleteAssetsByIDs deletes assets by their IDs, but only if they have no
// asset_source rows (i.e. no longer linked to any source). Returns the list
// of deleted IDs and the list of skipped IDs (still linked to a source, or
// not owned by this user).
func (db *DB) DeleteAssetsByIDs(ids []int64, userID int64) (deleted []int64, skipped []int64, err error) {
	if len(ids) == 0 {
		return []int64{}, []int64{}, nil
	}

	for _, id := range ids {
		// Confirm ownership.
		var owner int64
		err := db.Get(&owner, "SELECT user_id FROM assets WHERE id = ? AND user_id = ?", id, userID)
		if err != nil {
			skipped = append(skipped, id)
			continue
		}

		// Skip if any source still references this asset.
		var hasSource bool
		if err := db.Get(&hasSource, "SELECT EXISTS(SELECT 1 FROM asset_source WHERE asset_id = ?)", id); err != nil {
			skipped = append(skipped, id)
			continue
		}
		if hasSource {
			skipped = append(skipped, id)
			continue
		}

		if _, err := db.Exec("DELETE FROM assets WHERE id = ? AND user_id = ?", id, userID); err != nil {
			skipped = append(skipped, id)
			continue
		}
		deleted = append(deleted, id)
	}

	return deleted, skipped, nil
}

// CreateAssetResult reports the outcome of a CreateAsset call so the HTTP
// layer can broadcast AssetCreated vs AssetUpdated correctly.
type CreateAssetResult struct {
	Asset        *Asset
	AssetWasNew  bool // true if the asset row was inserted this call
	SourceWasNew bool // true if the asset_source row was inserted this call
}

// CreateAsset upserts an asset keyed on (user_id, name, type) and links it to
// the given source via the asset_source junction. Re-observing an existing
// asset from an existing source bumps the junction's last_seen; re-observing
// from a new source adds a junction row. sourceID==0 means "no ResourceManager
// id known" — the junction row is stored with source_id = NULL and the label
// alone identifies the observation. If sourceLabel is empty and sourceID is 0,
// no junction row is written (manual creation).
func (db *DB) CreateAsset(name, assetType string, userID int64, sourceID int64, sourceLabel string, lastSeen *time.Time) (*CreateAssetResult, error) {
	canonical, ok := CanonicalAssetType(assetType)
	if !ok {
		return nil, unknownAssetType(assetType)
	}
	assetType = canonical
	now := time.Now()
	lastSeenTime := now
	if lastSeen != nil {
		lastSeenTime = *lastSeen
	}

	// Upsert the asset by (user_id, name, type).
	var asset Asset
	err := db.Get(&asset, "SELECT "+assetColumns+" FROM assets WHERE user_id = ? AND name = ? AND type = ?", userID, name, assetType)

	assetWasNew := false
	if err == sql.ErrNoRows {
		result, ierr := db.Exec(
			"INSERT INTO assets (name, type, severity, score, user_id, created_at, modified_at, last_seen) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			name, assetType, 0, 0, userID, now, now, lastSeenTime,
		)
		if ierr != nil {
			return nil, fmt.Errorf("failed to create asset: %w", ierr)
		}
		id, ierr := result.LastInsertId()
		if ierr != nil {
			return nil, fmt.Errorf("failed to get asset ID: %w", ierr)
		}
		asset = Asset{
			ID:         id,
			Name:       name,
			Type:       assetType,
			UserID:     userID,
			CreatedAt:  now,
			ModifiedAt: now,
			LastSeen:   lastSeenTime,
		}
		assetWasNew = true
	} else if err != nil {
		return nil, fmt.Errorf("failed to look up asset: %w", err)
	}

	// Upsert the asset_source junction row.
	sourceWasNew := false
	if sourceID > 0 || sourceLabel != "" {
		var sidArg interface{}
		if sourceID > 0 {
			sidArg = sourceID
		} else {
			sidArg = nil
		}

		// Does a junction row already exist? Match on (asset_id, source_id)
		// including the NULL case — SQL NULL = NULL is NULL, so handle it
		// explicitly with IS.
		var existing struct {
			LastSeen     time.Time `db:"last_seen"`
			CountsAsSeen int       `db:"counts_as_seen"`
		}
		where, whereArgs := "asset_id = ? AND source_id = ?", []interface{}{asset.ID, sourceID}
		if sourceID <= 0 {
			where, whereArgs = "asset_id = ? AND source_id IS NULL AND source_label = ?", []interface{}{asset.ID, sourceLabel}
		}
		err = db.Get(&existing, "SELECT last_seen, counts_as_seen FROM asset_source WHERE "+where, whereArgs...)
		hasRow := err == nil
		if err != nil && err != sql.ErrNoRows {
			return nil, fmt.Errorf("failed to look up asset_source: %w", err)
		}

		if !hasRow {
			if _, err := db.Exec(
				"INSERT INTO asset_source (asset_id, source_id, source_label, last_seen, created_at, modified_at) VALUES (?, ?, ?, ?, ?, ?)",
				asset.ID, sidArg, sourceLabel, lastSeenTime, now, now,
			); err != nil {
				return nil, fmt.Errorf("failed to insert asset_source: %w", err)
			}
			sourceWasNew = true
		} else {
			// Touched by this import whatever the date: a re-import tells the
			// links it no longer produces by modified_at (linkUntouchedSince).
			// The sighting date only moves forward — except on a link that was
			// coverage only, whose date was never a sighting: listing the host
			// makes it one, dated by this listing.
			seen := existing.LastSeen
			if existing.CountsAsSeen == 0 || lastSeenTime.After(seen) {
				seen = lastSeenTime
			}
			if _, err := db.Exec(
				"UPDATE asset_source SET last_seen = ?, source_label = ?, counts_as_seen = 1, modified_at = ? WHERE "+where,
				append([]interface{}{seen, sourceLabel, now}, whereArgs...)...); err != nil {
				return nil, fmt.Errorf("failed to update asset_source: %w", err)
			}
		}
	}

	// Everything shown follows from the evidence: this observation, the
	// other sources, and findings that may have arrived before the host itself
	// did. See asset_refresh.go.
	if _, err := db.refreshAssets("id = ?", asset.ID); err != nil {
		return nil, err
	}
	if err := db.Get(&asset, "SELECT "+assetColumns+" FROM assets WHERE id = ?", asset.ID); err != nil {
		return nil, fmt.Errorf("failed to read asset: %w", err)
	}

	// Load sources for the return value.
	assets := []Asset{asset}
	if err := db.populateAssetSources(assets); err != nil {
		return nil, err
	}
	return &CreateAssetResult{
		Asset:        &assets[0],
		AssetWasNew:  assetWasNew,
		SourceWasNew: sourceWasNew,
	}, nil
}

// refreshLastScanned sets last_scanned of the assets matching where (a
// condition on the assets table, with args) to the newest last_seen of their
// vulnerabilities and detections, or NULL when they have none or are not of
// type Host. The
// newest is picked by julianday() because last_seen is stored in more than
// one layout; the value itself is copied as stored. modified_at moves only
// for assets whose value changes.
func (db *DB) refreshLastScanned(where string, args ...interface{}) error {
	newest := `(CASE WHEN type = 'Host' THEN (SELECT ls FROM (
		SELECT v.last_seen AS ls FROM vulnerabilities v WHERE v.host = assets.name AND v.user_id = assets.user_id
		UNION ALL
		SELECT d.last_seen FROM detections d WHERE d.host = assets.name AND d.user_id = assets.user_id
	) ORDER BY julianday(ls) DESC LIMIT 1) END)`
	_, err := db.Exec(`UPDATE assets SET last_scanned = `+newest+`, modified_at = ?
		WHERE (`+where+`) AND last_scanned IS NOT `+newest,
		append([]interface{}{time.Now()}, args...)...)
	if err != nil {
		return fmt.Errorf("failed to update asset last_scanned: %w", err)
	}
	return nil
}

// UpdateAllAssetLastScanned recomputes last_scanned of every asset, for
// changes not tracked per host (stale deletions during a re-import) and at
// startup.
func (db *DB) UpdateAllAssetLastScanned() error {
	return db.refreshLastScanned("1 = 1")
}

// SetAssetScanCoverage records the scan coverage one source reports for an
// asset: when it was verified that the asset is in a scheduled vulnerability
// scan, and that scan's shortest interval in days. A nil value leaves what
// the source reported before. The source's asset_source row must exist
// (CreateAsset makes it). Then the asset's own values are combined anew.
//
// These values are only ever set this way, by an import or a client of the
// API. Nothing derives them from findings, last_seen or anything else.
func (db *DB) SetAssetScanCoverage(assetID, sourceID int64, sourceLabel string, verifiedAt *time.Time, intervalDays *int) error {
	if verifiedAt == nil && intervalDays == nil {
		return nil
	}
	var sets []string
	var args []interface{}
	if verifiedAt != nil {
		sets = append(sets, "scan_coverage_verified_at = ?")
		args = append(args, *verifiedAt)
	}
	if intervalDays != nil {
		sets = append(sets, "scan_coverage_interval_days = ?")
		args = append(args, *intervalDays)
	}
	query := "UPDATE asset_source SET " + strings.Join(sets, ", ") + " WHERE asset_id = ?"
	args = append(args, assetID)
	if sourceID > 0 {
		query += " AND source_id = ?"
		args = append(args, sourceID)
	} else {
		query += " AND source_id IS NULL AND source_label = ?"
		args = append(args, sourceLabel)
	}
	if _, err := db.Exec(query, args...); err != nil {
		return fmt.Errorf("failed to set asset_source scan coverage: %w", err)
	}
	return db.combineScanCoverage([]int64{assetID})
}

// combineScanCoverage sets the scan coverage of the given assets from what
// their sources reported: the newest verification and the shortest interval
// over all of them, NULL when none reported one. This only combines the
// imported values — it is what keeps a removed source's coverage from
// lingering. The newest is picked by julianday(), the value copied as stored.
func (db *DB) combineScanCoverage(assetIDs []int64) error {
	if len(assetIDs) == 0 {
		return nil
	}
	placeholders := make([]string, len(assetIDs))
	args := make([]interface{}, 0, len(assetIDs))
	for i, id := range assetIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	_, err := db.Exec(`UPDATE assets SET
		last_scan_coverage_verification = (
			SELECT s.scan_coverage_verified_at FROM asset_source s
			WHERE s.asset_id = assets.id AND s.scan_coverage_verified_at IS NOT NULL
			ORDER BY julianday(s.scan_coverage_verified_at) DESC LIMIT 1),
		scan_coverage_interval_days = (
			SELECT MIN(s.scan_coverage_interval_days) FROM asset_source s
			WHERE s.asset_id = assets.id)
		WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return fmt.Errorf("failed to combine asset scan coverage: %w", err)
	}
	return nil
}

// DeleteAssetsBySource unlinks the given source from all assets it's attached
// to, then deletes any asset that ends up with no sources left. If userID > 0,
// the scope is restricted to that user; userID == 0 means "all users"
// (internal service path). Returns the IDs of assets that were fully deleted.
func (db *DB) DeleteAssetsBySource(sourceID int64, userID int64) ([]int64, []int64, error) {
	// Capture which assets will be touched so we can find the orphans afterward.
	var candidateIDs []int64
	var err error
	if userID == 0 {
		err = db.Select(&candidateIDs, `
			SELECT a.id FROM assets a
			INNER JOIN asset_source s ON s.asset_id = a.id
			WHERE s.source_id = ?
		`, sourceID)
	} else {
		err = db.Select(&candidateIDs, `
			SELECT a.id FROM assets a
			INNER JOIN asset_source s ON s.asset_id = a.id
			WHERE s.source_id = ? AND a.user_id = ?
		`, sourceID, userID)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get asset candidates: %w", err)
	}
	if len(candidateIDs) == 0 {
		return []int64{}, []int64{}, nil
	}

	// Drop the junction rows scoped to this source.
	if userID == 0 {
		_, err = db.Exec("DELETE FROM asset_source WHERE source_id = ?", sourceID)
	} else {
		_, err = db.Exec(`
			DELETE FROM asset_source
			WHERE source_id = ? AND asset_id IN (SELECT id FROM assets WHERE user_id = ?)
		`, sourceID, userID)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to remove asset_source rows: %w", err)
	}

	// Delete orphan assets (those that have no remaining junction rows).
	return db.deleteOrphanAssets(candidateIDs, userID)
}

// linkUntouchedSince is the condition, on an asset_source row aliased s and
// one time argument, that an import has not touched the link since then.
// modified_at, not last_seen: last_seen is when the file says the host was
// seen, and re-importing the same file does not move it.
const linkUntouchedSince = "COALESCE(s.modified_at, s.created_at) < ?"

// DeleteStaleAssetsBySource removes asset_source rows for this source that no
// import has touched since modifiedBefore, then deletes orphan assets.
// Used by SourceImporter re-import to clean up hosts the new scan didn't see.
func (db *DB) DeleteStaleAssetsBySource(sourceID int64, userID int64, modifiedBefore time.Time) ([]int64, []int64, error) {
	var candidateIDs []int64
	var err error
	if userID == 0 {
		err = db.Select(&candidateIDs, `
			SELECT a.id FROM assets a
			INNER JOIN asset_source s ON s.asset_id = a.id
			WHERE s.source_id = ? AND `+linkUntouchedSince+`
		`, sourceID, modifiedBefore)
	} else {
		err = db.Select(&candidateIDs, `
			SELECT a.id FROM assets a
			INNER JOIN asset_source s ON s.asset_id = a.id
			WHERE s.source_id = ? AND a.user_id = ? AND `+linkUntouchedSince+`
		`, sourceID, userID, modifiedBefore)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get stale asset candidates: %w", err)
	}
	if len(candidateIDs) == 0 {
		return []int64{}, []int64{}, nil
	}

	if userID == 0 {
		_, err = db.Exec(
			"DELETE FROM asset_source AS s WHERE s.source_id = ? AND "+linkUntouchedSince,
			sourceID, modifiedBefore)
	} else {
		_, err = db.Exec(`
			DELETE FROM asset_source AS s
			WHERE s.source_id = ? AND `+linkUntouchedSince+`
			  AND s.asset_id IN (SELECT id FROM assets WHERE user_id = ?)
		`, sourceID, modifiedBefore, userID)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to remove stale asset_source rows: %w", err)
	}

	return db.deleteOrphanAssets(candidateIDs, userID)
}

// deleteOrphanAssets deletes any asset in `ids` that no longer has a matching
// asset_source row, and recomputes the others from the sources they keep
// (asset_refresh.go). Returns the IDs of the assets deleted and of those whose
// values changed.
func (db *DB) deleteOrphanAssets(ids []int64, userID int64) (deleted, refreshed []int64, err error) {
	deleted, err = db.deleteOrphans(ids, userID)
	if err != nil {
		return nil, nil, err
	}
	gone := make(map[int64]bool, len(deleted))
	for _, id := range deleted {
		gone[id] = true
	}
	survivors := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !gone[id] {
			survivors = append(survivors, id)
		}
	}
	refreshed, err = db.RefreshAssetsByIDs(survivors)
	if err != nil {
		return nil, nil, err
	}
	return deleted, refreshed, nil
}

func (db *DB) deleteOrphans(ids []int64, userID int64) ([]int64, error) {
	if len(ids) == 0 {
		return []int64{}, nil
	}
	// The assets that keep other sources lose this one's scan coverage.
	if err := db.combineScanCoverage(ids); err != nil {
		return nil, err
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}

	query := fmt.Sprintf(`
		SELECT a.id FROM assets a
		WHERE a.id IN (%s)
		  AND NOT EXISTS (SELECT 1 FROM asset_source s WHERE s.asset_id = a.id)
	`, strings.Join(placeholders, ","))
	userScope := ""
	if userID != 0 {
		userScope = " AND a.user_id = ?"
		args = append(args, userID)
	}

	var orphanIDs []int64
	if err := db.Select(&orphanIDs, query+userScope, args...); err != nil {
		return nil, fmt.Errorf("failed to find orphan assets: %w", err)
	}
	if len(orphanIDs) == 0 {
		return []int64{}, nil
	}

	delPlaceholders := make([]string, len(orphanIDs))
	delArgs := make([]interface{}, 0, len(orphanIDs))
	for i, id := range orphanIDs {
		delPlaceholders[i] = "?"
		delArgs = append(delArgs, id)
	}
	if _, err := db.Exec("DELETE FROM assets WHERE id IN ("+strings.Join(delPlaceholders, ",")+")", delArgs...); err != nil {
		return nil, fmt.Errorf("failed to delete orphan assets: %w", err)
	}
	return orphanIDs, nil
}

// ClearAssetSourceBySource unlinks the given source from all assets it's
// attached to, WITHOUT deleting the assets themselves. Used when the user
// wants to keep the asset records after the originating source is gone.
// Returns the list of asset IDs whose link was removed.
func (db *DB) ClearAssetSourceBySource(sourceID int64, userID int64) ([]int64, error) {
	var assetIDs []int64
	var err error
	if userID == 0 {
		err = db.Select(&assetIDs, `
			SELECT a.id FROM assets a
			INNER JOIN asset_source s ON s.asset_id = a.id
			WHERE s.source_id = ?
		`, sourceID)
	} else {
		err = db.Select(&assetIDs, `
			SELECT a.id FROM assets a
			INNER JOIN asset_source s ON s.asset_id = a.id
			WHERE s.source_id = ? AND a.user_id = ?
		`, sourceID, userID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get asset IDs: %w", err)
	}
	if len(assetIDs) == 0 {
		return []int64{}, nil
	}

	now := time.Now()
	if userID == 0 {
		_, err = db.Exec("DELETE FROM asset_source WHERE source_id = ?", sourceID)
	} else {
		_, err = db.Exec(`
			DELETE FROM asset_source
			WHERE source_id = ? AND asset_id IN (SELECT id FROM assets WHERE user_id = ?)
		`, sourceID, userID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to clear asset sources: %w", err)
	}
	if err := db.combineScanCoverage(assetIDs); err != nil {
		return nil, err
	}
	// The rest of what they show follows from the sources they keep.
	if _, err := db.RefreshAssetsByIDs(assetIDs); err != nil {
		return nil, err
	}

	// Touch modified_at so list views reflect the change.
	placeholders := make([]string, len(assetIDs))
	args := make([]interface{}, 0, len(assetIDs)+1)
	args = append(args, now)
	for i, id := range assetIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	if _, err := db.Exec("UPDATE assets SET modified_at = ? WHERE id IN ("+strings.Join(placeholders, ",")+")", args...); err != nil {
		return nil, fmt.Errorf("failed to bump modified_at: %w", err)
	}

	return assetIDs, nil
}
