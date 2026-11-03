// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The aggregates the statistics endpoint reports: assets by severity band, host
// assets by when they were last seen, last scanned and how often a scheduled
// scan covers them, and the sums the organisation's scores are computed from.

package database

import (
	"database/sql"
	"fmt"
	"strings"
)

// AssetStats represents aggregated asset statistics by severity band
type AssetStats struct {
	Critical  int `json:"critical"`  // assets with severity >= 90
	High      int `json:"high"`      // assets with severity 70-89
	Medium    int `json:"medium"`    // assets with severity 40-69
	Low       int `json:"low"`       // assets with severity 1-39
	NoThreat  int `json:"noThreat"`  // assets with no vulnerabilities but has detections
	Unmanaged int `json:"unmanaged"` // assets with no vulnerabilities and no detections
	Total     int `json:"total"`

	// Host assets by how long ago they were last seen, for the dashboard's
	// asset inventory. Together they are every asset of type Host.
	HostsSeen30d   int `json:"hostsSeen30d"`   // last seen at most 30 days ago
	HostsSeen180d  int `json:"hostsSeen180d"`  // more than 30, at most 180 days ago
	HostsSeenOlder int `json:"hostsSeenOlder"` // more than 180 days ago

	// Host assets by how long ago they were last scanned (last_scanned),
	// for the dashboard's scanning coverage. Together they are every asset
	// of type Host.
	HostsScanned2d    int `json:"hostsScanned2d"`    // last scanned at most 2 days ago
	HostsScanned10d   int `json:"hostsScanned10d"`   // more than 2, at most 10 days ago
	HostsScannedOlder int `json:"hostsScannedOlder"` // more than 10 days ago
	HostsNeverScanned int `json:"hostsNeverScanned"` // no last_scanned, or one that cannot be read

	// Host assets by their scan coverage interval (scan_coverage_interval_days,
	// set by imports only), for the dashboard's asset monitoring. Together
	// they are every asset of type Host.
	HostsMonitored2d    int `json:"hostsMonitored2d"`    // scanned every 1 or 2 days
	HostsMonitored10d   int `json:"hostsMonitored10d"`   // every 3 to 10 days
	HostsMonitoredOlder int `json:"hostsMonitoredOlder"` // less often than every 10 days
	HostsUnmonitored    int `json:"hostsUnmonitored"`    // no interval: in no scheduled scan
}

// hostAgeBuckets fills the HostsSeen*, HostsScanned* and HostsMonitored*
// counts for the assets matching where.
//
// julianday() rather than comparing text: last_seen is written in more than
// one layout (a space or a "T", with or without fractions and offsets), and
// those do not sort as the times they stand for.
func (db *DB) hostAgeBuckets(stats *AssetStats, where string, args []interface{}) error {
	var b struct {
		Recent sql.NullInt64 `db:"recent"`
		Middle sql.NullInt64 `db:"middle"`
		Older  sql.NullInt64 `db:"older"`

		Scanned2d    sql.NullInt64 `db:"scanned_2d"`
		Scanned10d   sql.NullInt64 `db:"scanned_10d"`
		ScannedOlder sql.NullInt64 `db:"scanned_older"`
		NeverScanned sql.NullInt64 `db:"never_scanned"`

		Monitored2d    sql.NullInt64 `db:"monitored_2d"`
		Monitored10d   sql.NullInt64 `db:"monitored_10d"`
		MonitoredOlder sql.NullInt64 `db:"monitored_older"`
		Unmonitored    sql.NullInt64 `db:"unmonitored"`
	}
	// The scan boundaries match the lastScannedDays filter the dashboard's
	// "Show" applies: older is "> 10 and <= 99999", and anything without a
	// readable last_scanned (the filter's 1e9) is never scanned. The interval
	// boundaries match the scanCoverageIntervalDays filter the same way: no
	// interval is the filter's 1e9.
	err := db.Get(&b, `
		SELECT
			SUM(CASE WHEN age <= 30 THEN 1 ELSE 0 END) AS recent,
			SUM(CASE WHEN age > 30 AND age <= 180 THEN 1 ELSE 0 END) AS middle,
			SUM(CASE WHEN age > 180 OR age IS NULL THEN 1 ELSE 0 END) AS older,
			SUM(CASE WHEN scan_age <= 2 THEN 1 ELSE 0 END) AS scanned_2d,
			SUM(CASE WHEN scan_age > 2 AND scan_age <= 10 THEN 1 ELSE 0 END) AS scanned_10d,
			SUM(CASE WHEN scan_age > 10 AND scan_age <= 99999 THEN 1 ELSE 0 END) AS scanned_older,
			SUM(CASE WHEN scan_age IS NULL OR scan_age > 99999 THEN 1 ELSE 0 END) AS never_scanned,
			SUM(CASE WHEN scan_coverage_interval_days <= 2 THEN 1 ELSE 0 END) AS monitored_2d,
			SUM(CASE WHEN scan_coverage_interval_days > 2 AND scan_coverage_interval_days <= 10 THEN 1 ELSE 0 END) AS monitored_10d,
			SUM(CASE WHEN scan_coverage_interval_days > 10 AND scan_coverage_interval_days <= 99999 THEN 1 ELSE 0 END) AS monitored_older,
			SUM(CASE WHEN scan_coverage_interval_days IS NULL OR scan_coverage_interval_days > 99999 THEN 1 ELSE 0 END) AS unmonitored
		FROM (SELECT julianday('now') - julianday(last_seen) AS age,
		             julianday('now') - julianday(last_scanned) AS scan_age,
		             scan_coverage_interval_days
		      FROM assets WHERE `+where+` AND type = 'Host')`, args...)
	if err != nil {
		return fmt.Errorf("failed to count host assets by last seen, last scanned and scan interval: %w", err)
	}
	stats.HostsSeen30d = int(b.Recent.Int64)
	stats.HostsSeen180d = int(b.Middle.Int64)
	stats.HostsSeenOlder = int(b.Older.Int64)
	stats.HostsScanned2d = int(b.Scanned2d.Int64)
	stats.HostsScanned10d = int(b.Scanned10d.Int64)
	stats.HostsScannedOlder = int(b.ScannedOlder.Int64)
	stats.HostsNeverScanned = int(b.NeverScanned.Int64)
	stats.HostsMonitored2d = int(b.Monitored2d.Int64)
	stats.HostsMonitored10d = int(b.Monitored10d.Int64)
	stats.HostsMonitoredOlder = int(b.MonitoredOlder.Int64)
	stats.HostsUnmonitored = int(b.Unmonitored.Int64)
	return nil
}

// GetAssetStatsByUser returns asset statistics grouped by severity bands for a user
func (db *DB) GetAssetStatsByUser(userID int64) (*AssetStats, error) {
	stats := &AssetStats{}

	// Get total count
	err := db.Get(&stats.Total, "SELECT COUNT(*) FROM assets WHERE user_id = ?", userID)
	if err != nil {
		return nil, fmt.Errorf("failed to count assets: %w", err)
	}

	// Get critical count (severity >= 90)
	err = db.Get(&stats.Critical, "SELECT COUNT(*) FROM assets WHERE user_id = ? AND severity >= 90", userID)
	if err != nil {
		return nil, fmt.Errorf("failed to count critical assets: %w", err)
	}

	// Get high count (severity 70-89)
	err = db.Get(&stats.High, "SELECT COUNT(*) FROM assets WHERE user_id = ? AND severity >= 70 AND severity < 90", userID)
	if err != nil {
		return nil, fmt.Errorf("failed to count high assets: %w", err)
	}

	// Get medium count (severity 40-69)
	err = db.Get(&stats.Medium, "SELECT COUNT(*) FROM assets WHERE user_id = ? AND severity >= 40 AND severity < 70", userID)
	if err != nil {
		return nil, fmt.Errorf("failed to count medium assets: %w", err)
	}

	// Get low count (severity 1-39)
	err = db.Get(&stats.Low, "SELECT COUNT(*) FROM assets WHERE user_id = ? AND severity >= 1 AND severity < 40", userID)
	if err != nil {
		return nil, fmt.Errorf("failed to count low assets: %w", err)
	}

	// Assets with severity 0 need to be checked for detections
	// noThreat = has detections but no vulnerabilities (severity = 0 but has related detections)
	// unmanaged = no vulnerabilities and no detections (severity = 0 and no related detections)

	// Get count of assets with severity 0 that have related detections
	err = db.Get(&stats.NoThreat, `
		SELECT COUNT(DISTINCT a.id) FROM assets a
		INNER JOIN detections d ON d.host = a.name AND d.user_id = a.user_id
		WHERE a.user_id = ? AND a.severity = 0
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to count noThreat assets: %w", err)
	}

	// Get count of assets with severity 0 that have no related detections
	err = db.Get(&stats.Unmanaged, `
		SELECT COUNT(*) FROM assets a
		WHERE a.user_id = ? AND a.severity = 0
		AND NOT EXISTS (SELECT 1 FROM detections d WHERE d.host = a.name AND d.user_id = a.user_id)
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to count unmanaged assets: %w", err)
	}

	if err := db.hostAgeBuckets(stats, "user_id = ?", []interface{}{userID}); err != nil {
		return nil, err
	}

	return stats, nil
}

// GetAssetStatsByUsers returns the same severity-band stats across a SET of
// users. In Appliance mode the visible set is every instance user, so the
// dashboard totals match the (already set-scoped) asset list. The detection
// correlation is widened to the same set so noThreat/unmanaged are computed
// across the shared workspace, not per-owner.
func (db *DB) GetAssetStatsByUsers(userIDs []int64) (*AssetStats, error) {
	stats := &AssetStats{}
	if len(userIDs) == 0 {
		return stats, nil
	}
	ph := make([]string, len(userIDs))
	args := make([]interface{}, len(userIDs))
	for i, id := range userIDs {
		ph[i] = "?"
		args[i] = id
	}
	inList := strings.Join(ph, ",")
	uid := "user_id IN (" + inList + ")"
	// args2 is the arg list repeated once (for queries with two IN clauses).
	args2 := append(append([]interface{}{}, args...), args...)

	counts := []struct {
		dest *int
		sql  string
	}{
		{&stats.Total, "SELECT COUNT(*) FROM assets WHERE " + uid},
		{&stats.Critical, "SELECT COUNT(*) FROM assets WHERE " + uid + " AND severity >= 90"},
		{&stats.High, "SELECT COUNT(*) FROM assets WHERE " + uid + " AND severity >= 70 AND severity < 90"},
		{&stats.Medium, "SELECT COUNT(*) FROM assets WHERE " + uid + " AND severity >= 40 AND severity < 70"},
		{&stats.Low, "SELECT COUNT(*) FROM assets WHERE " + uid + " AND severity >= 1 AND severity < 40"},
	}
	for _, c := range counts {
		if err := db.Get(c.dest, c.sql, args...); err != nil {
			return nil, fmt.Errorf("failed to count assets: %w", err)
		}
	}

	if err := db.Get(&stats.NoThreat,
		"SELECT COUNT(DISTINCT a.id) FROM assets a INNER JOIN detections d ON d.host = a.name AND d.user_id IN ("+inList+") WHERE a.user_id IN ("+inList+") AND a.severity = 0",
		args2...); err != nil {
		return nil, fmt.Errorf("failed to count noThreat assets: %w", err)
	}
	if err := db.Get(&stats.Unmanaged,
		"SELECT COUNT(*) FROM assets a WHERE a.user_id IN ("+inList+") AND a.severity = 0 AND NOT EXISTS (SELECT 1 FROM detections d WHERE d.host = a.name AND d.user_id IN ("+inList+"))",
		args2...); err != nil {
		return nil, fmt.Errorf("failed to count unmanaged assets: %w", err)
	}
	if err := db.hostAgeBuckets(stats, uid, args); err != nil {
		return nil, err
	}
	return stats, nil
}

// AssetScoreAggregates holds the count and sum of scores for a user's assets
type AssetScoreAggregates struct {
	Count    int `db:"count"`
	ScoreSum int `db:"score_sum"`
}

// GetAssetScoreAggregatesByUser returns the count and sum of per-asset scores.
// Each asset's score is 0 (unmanaged) or 100-severity (managed), stored in the score column.
func (db *DB) GetAssetScoreAggregatesByUser(userID int64) (*AssetScoreAggregates, error) {
	agg := &AssetScoreAggregates{}
	err := db.Get(agg, "SELECT COUNT(*) as count, COALESCE(SUM(score), 0) as score_sum FROM assets WHERE user_id = ?", userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get asset score aggregates: %w", err)
	}
	return agg, nil
}
