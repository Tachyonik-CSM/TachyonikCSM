// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Vulnerabilities: the row type and its queries. What they share with
// detections is written once in findings.go; these are its per-kind entry
// points, plus creating one.

package database

import (
	"time"

	"tachyonik/lib/listquery"
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
	return db.ListVulnerabilities(userIDs, filter, listquery.Query{})
}

// ListVulnerabilities returns the users' vulnerabilities matching the filter, in the order
// and at most as many as q asks for (AM-API-16); without a sort in the order
// of GetVulnerabilitiesByUsersWithFilter.
func (db *DB) ListVulnerabilities(userIDs []int64, filter *VulnerabilityFilter, q listquery.Query) ([]Vulnerability, error) {
	var vulnerabilities []Vulnerability
	if err := db.listFindings(vulnerabilityKind, &vulnerabilities, userIDs, filter, q); err != nil {
		return nil, err
	}
	return vulnerabilities, nil
}

// CountVulnerabilitiesByUsersWithFilter counts the users' vulnerabilities matching the
// filter.
func (db *DB) CountVulnerabilitiesByUsersWithFilter(userIDs []int64, filter *VulnerabilityFilter) (int, error) {
	return db.countFindings(vulnerabilityKind, userIDs, filter)
}

// CountVulnerabilitiesByUserWithFilter counts the user's vulnerabilities
// matching the filter.
func (db *DB) CountVulnerabilitiesByUserWithFilter(userID int64, filter *VulnerabilityFilter) (int, error) {
	return db.countFindings(vulnerabilityKind, []int64{userID}, filter)
}

// CreateVulnerability reports a vulnerability (AM-DATA-47): it creates it, or reports the
// known one of the same owner, source, host, port and name again, and returns
// the record. If lastSeen is nil, the current time is used.
func (db *DB) CreateVulnerability(name, host, port string, severity int, sourceID int64, sourceLabel string, userID int64, lastSeen *time.Time) (*Vulnerability, error) {
	out, err := db.ReportFinding(VulnerabilityTable, FindingReport{Name: name, Host: host, Port: port, Severity: &severity, SourceID: sourceID, SourceLabel: sourceLabel, UserID: userID, LastSeen: lastSeen})
	if err != nil {
		return nil, err
	}
	return db.GetVulnerabilityByID(out.ID)
}

// DeleteVulnerabilitiesBySource deletes a source's vulnerabilities — the
// user's, or everyone's for userID 0 (an internal service request) — and
// returns their IDs with their owners.
func (db *DB) DeleteVulnerabilitiesBySource(sourceID int64, userID int64) ([]Owned, error) {
	where, args := sourceScope(sourceID, userID, nil)
	return db.deleteFindingsWhere(vulnerabilityKind, where, args)
}

// DeleteStaleVulnerabilitiesBySource deletes a source's vulnerabilities not
// modified since modifiedBefore — what a re-import no longer produced — and
// returns their IDs with their owners.
func (db *DB) DeleteStaleVulnerabilitiesBySource(sourceID int64, userID int64, modifiedBefore time.Time) ([]Owned, error) {
	where, args := sourceScope(sourceID, userID, &modifiedBefore)
	return db.deleteFindingsWhere(vulnerabilityKind, where, args)
}
