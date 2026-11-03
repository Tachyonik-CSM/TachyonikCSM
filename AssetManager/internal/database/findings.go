// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// What vulnerabilities and detections share. Both are findings on a host, kept
// in tables of the same shape (name, host, port, source, owner, timestamps;
// vulnerabilities add a severity), and are listed, filtered, deleted by source
// or by ID and unlinked from a source in exactly the same way. Each operation
// is written once here, against a findingKind naming the table; the exported
// per-kind functions in vulnerabilities.go and detections.go are one-line
// wrappers.

package database

import (
	"fmt"
	"strings"
	"time"
)

// findingKind names one finding table and how it is listed.
type findingKind struct {
	table   string // also the plural in error messages
	noun    string // the singular in error messages
	columns string // the columns a list selects, in the struct's order
	order   string
	ruleSQL func(FilterRule) (string, []interface{})
}

var (
	vulnerabilityKind = findingKind{
		table:   "vulnerabilities",
		noun:    "vulnerability",
		columns: "id, name, host, port, severity, source_id, source_label, user_id, created_at, modified_at, last_seen",
		order:   "severity DESC, created_at DESC",
		ruleSQL: buildVulnerabilityRuleSQL,
	}
	detectionKind = findingKind{
		table:   "detections",
		noun:    "detection",
		columns: "id, name, host, port, source_id, source_label, user_id, created_at, modified_at, last_seen",
		order:   "created_at DESC",
		ruleSQL: buildDetectionRuleSQL,
	}
)

// filteredWhere is the WHERE clause of a list or count of the users' findings
// matching the filter.
func (k findingKind) filteredWhere(userIDs []int64, filter *Filter) (string, []interface{}) {
	where, args := buildUserIDsClause(userIDs)
	if filterSQL, filterArgs := buildFilterSQL(filter, k.ruleSQL); filterSQL != "" {
		where += " AND (" + filterSQL + ")"
		args = append(args, filterArgs...)
	}
	return where, args
}

// listFindings selects into dest (a pointer to a slice of the kind's struct)
// the users' findings matching the filter, in the kind's order.
func (db *DB) listFindings(k findingKind, dest interface{}, userIDs []int64, filter *Filter) error {
	where, args := k.filteredWhere(userIDs, filter)
	if err := db.Select(dest, "SELECT "+k.columns+" FROM "+k.table+" WHERE "+where+" ORDER BY "+k.order, args...); err != nil {
		return fmt.Errorf("failed to get %s: %w", k.table, err)
	}
	return nil
}

// countFindings counts the users' findings matching the filter.
func (db *DB) countFindings(k findingKind, userIDs []int64, filter *Filter) (int, error) {
	where, args := k.filteredWhere(userIDs, filter)
	var count int
	if err := db.Get(&count, "SELECT COUNT(*) FROM "+k.table+" WHERE "+where, args...); err != nil {
		return 0, fmt.Errorf("failed to count %s: %w", k.table, err)
	}
	return count, nil
}

// findingHostsByIDs returns the distinct hosts of the user's findings among ids.
func (db *DB) findingHostsByIDs(k findingKind, ids []int64, userID int64) ([]string, error) {
	if len(ids) == 0 {
		return []string{}, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	args = append(args, userID)
	var hosts []string
	query := "SELECT DISTINCT host FROM " + k.table + " WHERE id IN (" + strings.Join(placeholders, ",") + ") AND user_id = ?"
	if err := db.Select(&hosts, query, args...); err != nil {
		return nil, fmt.Errorf("failed to get %s hosts: %w", k.noun, err)
	}
	return hosts, nil
}

// sourceScope is the condition naming a source's findings, by the source's
// ResourceManager id: the user's, or everyone's for userID 0 (an internal
// service request), and with modifiedBefore set only those a re-import did not
// touch since. By id rather than by name, so a renamed file still finds its
// findings.
func sourceScope(sourceID int64, userID int64, modifiedBefore *time.Time) (string, []interface{}) {
	where, args := "source_id = ?", []interface{}{sourceID}
	if userID != 0 {
		where += " AND user_id = ?"
		args = append(args, userID)
	}
	if modifiedBefore != nil {
		where += " AND modified_at < ?"
		args = append(args, *modifiedBefore)
	}
	return where, args
}

// deleteFindingsWhere deletes the findings matching where and returns their
// IDs — an empty slice, never nil, when there were none.
func (db *DB) deleteFindingsWhere(k findingKind, where string, args []interface{}) ([]int64, error) {
	var ids []int64
	if err := db.Select(&ids, "SELECT id FROM "+k.table+" WHERE "+where, args...); err != nil {
		return nil, fmt.Errorf("failed to get %s IDs: %w", k.noun, err)
	}
	if len(ids) == 0 {
		return []int64{}, nil
	}
	if _, err := db.Exec("DELETE FROM "+k.table+" WHERE "+where, args...); err != nil {
		return nil, fmt.Errorf("failed to delete %s: %w", k.table, err)
	}
	return ids, nil
}

// clearFindingSource unlinks the findings matching the source scope from their
// source, keeping the records, and returns their IDs.
func (db *DB) clearFindingSource(k findingKind, sourceID int64, userID int64) ([]int64, error) {
	where, args := sourceScope(sourceID, userID, nil)
	var ids []int64
	if err := db.Select(&ids, "SELECT id FROM "+k.table+" WHERE "+where, args...); err != nil {
		return nil, fmt.Errorf("failed to get %s IDs: %w", k.noun, err)
	}
	if len(ids) == 0 {
		return []int64{}, nil
	}
	if _, err := db.Exec("UPDATE "+k.table+" SET source_id = NULL, source_label = '', modified_at = ? WHERE "+where, append([]interface{}{time.Now()}, args...)...); err != nil {
		return nil, fmt.Errorf("failed to clear %s sources: %w", k.noun, err)
	}
	return ids, nil
}

// deleteFindingsByIDs deletes those of ids that belong to the user and that no
// source owns (no source id, and an empty or blank label): a record an import
// made is
// removed with its source, not one by one. Every other ID — someone else's,
// sourced, unknown, or one whose delete failed — is returned as skipped.
func (db *DB) deleteFindingsByIDs(k findingKind, ids []int64, userID int64) (deleted []int64, skipped []int64, err error) {
	if len(ids) == 0 {
		return []int64{}, []int64{}, nil
	}
	for _, id := range ids {
		var owner struct {
			SourceID    *int64 `db:"source_id"`
			SourceLabel string `db:"source_label"`
		}
		if err := db.Get(&owner, "SELECT source_id, source_label FROM "+k.table+" WHERE id = ? AND user_id = ?", id, userID); err != nil {
			skipped = append(skipped, id)
			continue
		}
		if owner.SourceID != nil || strings.TrimSpace(owner.SourceLabel) != "" {
			skipped = append(skipped, id)
			continue
		}
		if _, err := db.Exec("DELETE FROM "+k.table+" WHERE id = ? AND user_id = ?", id, userID); err != nil {
			skipped = append(skipped, id)
			continue
		}
		deleted = append(deleted, id)
	}
	return deleted, skipped, nil
}

// sourceIDArg is a source id as a query argument: NULL for 0, no id known.
func sourceIDArg(id int64) interface{} {
	if id > 0 {
		return id
	}
	return nil
}

// sourceIDPtr is a source id as a row field: nil for 0, no id known.
func sourceIDPtr(id int64) *int64 {
	if id > 0 {
		return &id
	}
	return nil
}
