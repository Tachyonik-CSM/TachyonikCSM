// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Detections: the row type and its queries. What they share with
// vulnerabilities is written once in findings.go; these are its per-kind entry
// points, plus creating one.

package database

import (
	"time"

	"tachyonik/lib/listquery"
)

// Detection represents a detection (severity 0 finding) in the database
type Detection struct {
	ID   int64  `json:"id" db:"id"`
	Name string `json:"name" db:"name"`
	Host string `json:"host" db:"host"`
	Port string `json:"port" db:"port"`
	// The source the finding came from: its ResourceManager id (nil when only
	// its name is known) and its filename. Both empty for one entered by hand.
	SourceID    *int64    `json:"sourceId" db:"source_id"`
	SourceLabel string    `json:"sourceLabel" db:"source_label"`
	UserID      int64     `json:"userId" db:"user_id"`
	CreatedAt   time.Time `json:"createdAt" db:"created_at"`
	ModifiedAt  time.Time `json:"modifiedAt" db:"modified_at"`
	LastSeen    time.Time `json:"lastSeen" db:"last_seen"`
}

// GetDetectionHostsByIDs returns the distinct hosts of the user's detections
// among ids.
func (db *DB) GetDetectionHostsByIDs(ids []int64, userID int64) ([]string, error) {
	return db.findingHostsByIDs(detectionKind, ids, userID)
}

// GetDetectionsByUsersWithFilter returns the users' detections matching the
// filter (all of them for none), newest first.
func (db *DB) GetDetectionsByUsersWithFilter(userIDs []int64, filter *DetectionFilter) ([]Detection, error) {
	return db.ListDetections(userIDs, filter, listquery.Query{})
}

// ListDetections returns the users' detections matching the filter, in the order
// and at most as many as q asks for (AM-API-16); without a sort in the order
// of GetDetectionsByUsersWithFilter.
func (db *DB) ListDetections(userIDs []int64, filter *DetectionFilter, q listquery.Query) ([]Detection, error) {
	var detections []Detection
	if err := db.listFindings(detectionKind, &detections, userIDs, filter, q); err != nil {
		return nil, err
	}
	return detections, nil
}

// CountDetectionsByUsersWithFilter counts the users' detections matching the
// filter.
func (db *DB) CountDetectionsByUsersWithFilter(userIDs []int64, filter *DetectionFilter) (int, error) {
	return db.countFindings(detectionKind, userIDs, filter)
}

// CountDetectionsByUserWithFilter counts the user's detections matching the
// filter.
func (db *DB) CountDetectionsByUserWithFilter(userID int64, filter *DetectionFilter) (int, error) {
	return db.countFindings(detectionKind, []int64{userID}, filter)
}

// CreateDetection reports a detection (AM-DATA-47): it creates it, or reports the
// known one of the same owner, source, host, port and name again, and returns
// the record. If lastSeen is nil, the current time is used.
func (db *DB) CreateDetection(name, host, port string, sourceID int64, sourceLabel string, userID int64, lastSeen *time.Time) (*Detection, error) {
	out, err := db.ReportFinding(DetectionTable, FindingReport{Name: name, Host: host, Port: port, SourceID: sourceID, SourceLabel: sourceLabel, UserID: userID, LastSeen: lastSeen})
	if err != nil {
		return nil, err
	}
	return db.GetDetectionByID(out.ID)
}

// DeleteDetectionsBySource deletes a source's detections — the user's, or
// everyone's for userID 0 (an internal service request) — and returns their
// IDs.
func (db *DB) DeleteDetectionsBySource(sourceID int64, userID int64) ([]Owned, error) {
	where, args := sourceScope(sourceID, userID, nil)
	return db.deleteFindingsWhere(detectionKind, where, args)
}

// DeleteStaleDetectionsBySource deletes a source's detections not modified
// since modifiedBefore — what a re-import no longer produced — and returns
// their IDs.
func (db *DB) DeleteStaleDetectionsBySource(sourceID int64, userID int64, modifiedBefore time.Time) ([]Owned, error) {
	where, args := sourceScope(sourceID, userID, &modifiedBefore)
	return db.deleteFindingsWhere(detectionKind, where, args)
}
