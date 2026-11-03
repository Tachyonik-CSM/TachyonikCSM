// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Vulnerabilities: the row type and its queries. What they share with
// detections is written once in findings.go; these are its per-kind entry
// points, plus creating one.

package database

import (
	"fmt"
	"time"
)

// Vulnerability represents a vulnerability in the database
type Vulnerability struct {
	ID       int64  `json:"id" db:"id"`
	Name     string `json:"name" db:"name"`
	Host     string `json:"host" db:"host"`
	Port     string `json:"port" db:"port"`
	Severity int    `json:"severity" db:"severity"`
	// The source the finding came from: its ResourceManager id (nil when only
	// its name is known) and its filename. Both empty for one entered by hand.
	SourceID    *int64    `json:"sourceId" db:"source_id"`
	SourceLabel string    `json:"sourceLabel" db:"source_label"`
	UserID      int64     `json:"userId" db:"user_id"`
	CreatedAt   time.Time `json:"createdAt" db:"created_at"`
	ModifiedAt  time.Time `json:"modifiedAt" db:"modified_at"`
	LastSeen    time.Time `json:"lastSeen" db:"last_seen"`
}

// GetVulnerabilityHostsByIDs returns the distinct hosts of the user's
// vulnerabilities among ids.
func (db *DB) GetVulnerabilityHostsByIDs(ids []int64, userID int64) ([]string, error) {
	return db.findingHostsByIDs(vulnerabilityKind, ids, userID)
}

// GetVulnerabilitiesByUsersWithFilter returns the users' vulnerabilities
// matching the filter (all of them for none), by severity, then newest first.
func (db *DB) GetVulnerabilitiesByUsersWithFilter(userIDs []int64, filter *VulnerabilityFilter) ([]Vulnerability, error) {
	var vulnerabilities []Vulnerability
	if err := db.listFindings(vulnerabilityKind, &vulnerabilities, userIDs, filter); err != nil {
		return nil, err
	}
	return vulnerabilities, nil
}

// CountVulnerabilitiesByUserWithFilter counts the user's vulnerabilities
// matching the filter.
func (db *DB) CountVulnerabilitiesByUserWithFilter(userID int64, filter *VulnerabilityFilter) (int, error) {
	return db.countFindings(vulnerabilityKind, []int64{userID}, filter)
}

// CreateVulnerability creates a new vulnerability and returns it
// If lastSeen is nil, uses current time
func (db *DB) CreateVulnerability(name, host, port string, severity int, sourceID int64, sourceLabel string, userID int64, lastSeen *time.Time) (*Vulnerability, error) {
	// Validate severity range
	if severity < 0 || severity > 100 {
		return nil, fmt.Errorf("severity must be between 0 and 100")
	}

	// Create new vulnerability
	now := time.Now()
	lastSeenTime := now
	if lastSeen != nil {
		lastSeenTime = *lastSeen
	}

	result, err := db.Exec(
		"INSERT INTO vulnerabilities (name, host, port, severity, source_id, source_label, user_id, created_at, modified_at, last_seen) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		name, host, port, severity, sourceIDArg(sourceID), sourceLabel, userID, now, now, lastSeenTime,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create vulnerability: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to get vulnerability ID: %w", err)
	}

	return &Vulnerability{
		ID:          id,
		Name:        name,
		Host:        host,
		Port:        port,
		Severity:    severity,
		SourceID:    sourceIDPtr(sourceID),
		SourceLabel: sourceLabel,
		UserID:      userID,
		CreatedAt:   now,
		ModifiedAt:  now,
		LastSeen:    lastSeenTime,
	}, nil
}

// DeleteVulnerabilitiesBySource deletes a source's vulnerabilities — the
// user's, or everyone's for userID 0 (an internal service request) — and
// returns their IDs.
func (db *DB) DeleteVulnerabilitiesBySource(sourceID int64, userID int64) ([]int64, error) {
	where, args := sourceScope(sourceID, userID, nil)
	return db.deleteFindingsWhere(vulnerabilityKind, where, args)
}

// DeleteStaleVulnerabilitiesBySource deletes a source's vulnerabilities not
// modified since modifiedBefore — what a re-import no longer produced — and
// returns their IDs.
func (db *DB) DeleteStaleVulnerabilitiesBySource(sourceID int64, userID int64, modifiedBefore time.Time) ([]int64, error) {
	where, args := sourceScope(sourceID, userID, &modifiedBefore)
	return db.deleteFindingsWhere(vulnerabilityKind, where, args)
}

// DeleteVulnerabilitiesByIDs deletes those of the user's vulnerabilities
// among ids that no source owns; see deleteFindingsByIDs.
func (db *DB) DeleteVulnerabilitiesByIDs(ids []int64, userID int64) (deleted []int64, skipped []int64, err error) {
	return db.deleteFindingsByIDs(vulnerabilityKind, ids, userID)
}

// ClearVulnerabilitySourceBySource unlinks a source's vulnerabilities from it,
// keeping them, and returns their IDs.
func (db *DB) ClearVulnerabilitySourceBySource(sourceID int64, userID int64) ([]int64, error) {
	return db.clearFindingSource(vulnerabilityKind, sourceID, userID)
}
