// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Detections: the row type and its queries. What they share with
// vulnerabilities is written once in findings.go; these are its per-kind entry
// points, plus creating one.

package database

import (
	"fmt"
	"time"
)

// Detection represents a detection (severity 0 finding) in the database
type Detection struct {
	ID         int64     `json:"id" db:"id"`
	Name       string    `json:"name" db:"name"`
	Host       string    `json:"host" db:"host"`
	Port       string    `json:"port" db:"port"`
	Source     string    `json:"source" db:"source"`
	UserID     int64     `json:"userId" db:"user_id"`
	CreatedAt  time.Time `json:"createdAt" db:"created_at"`
	ModifiedAt time.Time `json:"modifiedAt" db:"modified_at"`
	LastSeen   time.Time `json:"lastSeen" db:"last_seen"`
}

// GetDetectionHostsByIDs returns the distinct hosts of the user's detections
// among ids.
func (db *DB) GetDetectionHostsByIDs(ids []int64, userID int64) ([]string, error) {
	return db.findingHostsByIDs(detectionKind, ids, userID)
}

// GetDetectionsByUsersWithFilter returns the users' detections matching the
// filter (all of them for none), newest first.
func (db *DB) GetDetectionsByUsersWithFilter(userIDs []int64, filter *DetectionFilter) ([]Detection, error) {
	var detections []Detection
	if err := db.listFindings(detectionKind, &detections, userIDs, filter); err != nil {
		return nil, err
	}
	return detections, nil
}

// CountDetectionsByUserWithFilter counts the user's detections matching the
// filter.
func (db *DB) CountDetectionsByUserWithFilter(userID int64, filter *DetectionFilter) (int, error) {
	return db.countFindings(detectionKind, []int64{userID}, filter)
}

// CreateDetection creates a new detection and returns it
// If lastSeen is nil, uses current time
func (db *DB) CreateDetection(name, host, port, source string, userID int64, lastSeen *time.Time) (*Detection, error) {
	now := time.Now()
	lastSeenTime := now
	if lastSeen != nil {
		lastSeenTime = *lastSeen
	}

	result, err := db.Exec(
		"INSERT INTO detections (name, host, port, source, user_id, created_at, modified_at, last_seen) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		name, host, port, source, userID, now, now, lastSeenTime,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create detection: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to get detection ID: %w", err)
	}

	return &Detection{
		ID:         id,
		Name:       name,
		Host:       host,
		Port:       port,
		Source:     source,
		UserID:     userID,
		CreatedAt:  now,
		ModifiedAt: now,
		LastSeen:   lastSeenTime,
	}, nil
}

// DeleteDetectionsByIDs deletes those of the user's detections among ids that
// no source owns; see deleteFindingsByIDs.
func (db *DB) DeleteDetectionsByIDs(ids []int64, userID int64) (deleted []int64, skipped []int64, err error) {
	return db.deleteFindingsByIDs(detectionKind, ids, userID)
}

// DeleteDetectionsBySource deletes a source's detections — the user's, or
// everyone's for userID 0 (an internal service request) — and returns their
// IDs.
func (db *DB) DeleteDetectionsBySource(source string, userID int64) ([]int64, error) {
	where, args := sourceScope(source, userID, nil)
	return db.deleteFindingsWhere(detectionKind, where, args)
}

// DeleteStaleDetectionsBySource deletes a source's detections not modified
// since modifiedBefore — what a re-import no longer produced — and returns
// their IDs.
func (db *DB) DeleteStaleDetectionsBySource(source string, userID int64, modifiedBefore time.Time) ([]int64, error) {
	where, args := sourceScope(source, userID, &modifiedBefore)
	return db.deleteFindingsWhere(detectionKind, where, args)
}

// ClearDetectionSourceBySource unlinks a source's detections from it, keeping
// them, and returns their IDs.
func (db *DB) ClearDetectionSourceBySource(source string, userID int64) ([]int64, error) {
	return db.clearFindingSource(detectionKind, source, userID)
}
