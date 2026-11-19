// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// A finding's identity (AM-DATA-47): its owner, source, host, port and name.
// Reporting a finding whose identity is known reports that record again —
// same id, the new label and severity, last seen only forward, touched for
// the stale sweep (AM-DATA-28) — instead of storing a copy. An OpenVAS
// report listing a check once per virtual hostname of a host, or a re-import,
// used to leave one record per report. docs/plans/finding-identity.md.

package database

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"tachyonik/lib/logger"
)

// FindingReport is what a source says about one finding.
type FindingReport struct {
	Name, Host, Port string
	// Severity is set for a vulnerability, nil for a detection.
	Severity    *int
	SourceID    int64
	SourceLabel string
	UserID      int64
	LastSeen    *time.Time
}

// FindingOutcome is what reporting a finding did.
type FindingOutcome struct {
	ID int64
	// New is true when the finding was created, false when a known one was
	// reported again.
	New bool
	// Changed is true when something shown changed: the severity, the label
	// or the last seen. Always true for a new finding.
	Changed bool
}

// sqliteTime is the layout julianday() reads for a time argument.
const sqliteTime = "2006-01-02 15:04:05"

// FindingExists reports whether a finding of this identity is on record.
// Reporting it again creates nothing, so no limit applies (AM-DATA-35).
func (db *DB) FindingExists(k FindingTable, r FindingReport) (bool, error) {
	var n int
	if err := db.Get(&n, "SELECT COUNT(*) FROM "+k.kind().table+" WHERE "+identityWhere, identityArgs(r)...); err != nil {
		return false, fmt.Errorf("failed to look up the %s: %w", k.kind().noun, err)
	}
	return n > 0, nil
}

// FindingTable selects a finding table for the identity functions.
type FindingTable int

const (
	VulnerabilityTable FindingTable = iota
	DetectionTable
)

func (t FindingTable) kind() findingKind {
	if t == VulnerabilityTable {
		return vulnerabilityKind
	}
	return detectionKind
}

// identityWhere matches a finding's identity. "IS" also matches a missing
// source id, which records from before AM-PUR-20 may still have.
const identityWhere = "user_id = ? AND source_id IS ? AND host = ? AND port = ? AND name = ?"

func identityArgs(r FindingReport) []interface{} {
	return []interface{}{r.UserID, sourceIDArg(r.SourceID), r.Host, r.Port, r.Name}
}

// ReportFinding creates the finding, or reports the known one of the same
// identity again.
func (db *DB) ReportFinding(t FindingTable, r FindingReport) (FindingOutcome, error) {
	k := t.kind()
	now := time.Now()
	lastSeen := now
	if r.LastSeen != nil {
		lastSeen = *r.LastSeen
	}
	isVuln := t == VulnerabilityTable
	if isVuln && (r.Severity == nil || *r.Severity < 0 || *r.Severity > 100) {
		return FindingOutcome{}, fmt.Errorf("severity must be between 0 and 100")
	}

	for attempt := 0; attempt < 2; attempt++ {
		var known struct {
			ID       int64         `db:"id"`
			Label    string        `db:"source_label"`
			Severity sql.NullInt64 `db:"severity"`
			Newer    bool          `db:"newer"`
		}
		sevCol := "NULL"
		if isVuln {
			sevCol = "severity"
		}
		err := db.Get(&known, "SELECT id, source_label, "+sevCol+" AS severity, "+
			"COALESCE(julianday(?) > julianday(last_seen), 1) AS newer FROM "+k.table+" WHERE "+identityWhere,
			append([]interface{}{lastSeen.UTC().Format(sqliteTime)}, identityArgs(r)...)...)
		switch {
		case err == nil:
			set := "source_label = ?, modified_at = ?"
			args := []interface{}{r.SourceLabel, now}
			if known.Newer {
				set += ", last_seen = ?"
				args = append(args, lastSeen)
			}
			if isVuln {
				set += ", severity = ?"
				args = append(args, *r.Severity)
			}
			if _, err := db.Exec("UPDATE "+k.table+" SET "+set+" WHERE id = ?", append(args, known.ID)...); err != nil {
				return FindingOutcome{}, fmt.Errorf("failed to report the %s again: %w", k.noun, err)
			}
			changed := known.Newer || known.Label != r.SourceLabel || (isVuln && known.Severity.Int64 != int64(*r.Severity))
			return FindingOutcome{ID: known.ID, Changed: changed}, nil
		case !errors.Is(err, sql.ErrNoRows):
			return FindingOutcome{}, fmt.Errorf("failed to look up the %s: %w", k.noun, err)
		}

		var res sql.Result
		if isVuln {
			res, err = db.Exec("INSERT INTO vulnerabilities (name, host, port, severity, source_id, source_label, user_id, created_at, modified_at, last_seen) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
				r.Name, r.Host, r.Port, *r.Severity, sourceIDArg(r.SourceID), r.SourceLabel, r.UserID, now, now, lastSeen)
		} else {
			res, err = db.Exec("INSERT INTO detections (name, host, port, source_id, source_label, user_id, created_at, modified_at, last_seen) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
				r.Name, r.Host, r.Port, sourceIDArg(r.SourceID), r.SourceLabel, r.UserID, now, now, lastSeen)
		}
		if err != nil {
			// Another import stored the same identity between the look-up
			// and the insert: report that one again.
			if strings.Contains(err.Error(), "UNIQUE constraint failed") && attempt == 0 {
				continue
			}
			return FindingOutcome{}, fmt.Errorf("failed to create the %s: %w", k.noun, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return FindingOutcome{}, fmt.Errorf("failed to get the %s id: %w", k.noun, err)
		}
		return FindingOutcome{ID: id, New: true, Changed: true}, nil
	}
	return FindingOutcome{}, fmt.Errorf("failed to report the %s", k.noun)
}

// GetVulnerabilityByID returns one vulnerability.
func (db *DB) GetVulnerabilityByID(id int64) (*Vulnerability, error) {
	var v Vulnerability
	if err := db.Get(&v, "SELECT "+vulnerabilityKind.columns+" FROM vulnerabilities WHERE id = ?", id); err != nil {
		return nil, fmt.Errorf("failed to read vulnerability %d: %w", id, err)
	}
	return &v, nil
}

// GetDetectionByID returns one detection.
func (db *DB) GetDetectionByID(id int64) (*Detection, error) {
	var d Detection
	if err := db.Get(&d, "SELECT "+detectionKind.columns+" FROM detections WHERE id = ?", id); err != nil {
		return nil, fmt.Errorf("failed to read detection %d: %w", id, err)
	}
	return &d, nil
}

// migrateFindingIdentity (v8) merges the findings of one identity — owner,
// source, host, port and name — into the oldest of them, which takes the
// newest last seen and touch and the highest severity, deletes the others,
// and adds a unique index per table so the identity holds from now on. The
// assets are recomputed from what is left at the next start
// (RefreshAllAssets). Records without a source id are not merged: the index
// treats them as distinct, and the sources' sweep removes them (AM-DATA-46).
func migrateFindingIdentity(db *DB) error {
	for _, t := range []struct {
		table, extra string
	}{
		{"vulnerabilities", "severity = (SELECT MAX(w.severity) FROM vulnerabilities w WHERE " + sameIdentity + "),"},
		{"detections", ""},
	} {
		tx, err := db.Beginx()
		if err != nil {
			return err
		}
		keepers := "SELECT MIN(id) FROM " + t.table + " WHERE source_id IS NOT NULL GROUP BY user_id, source_id, host, port, name HAVING COUNT(*) > 1"
		_, err = tx.Exec(`UPDATE ` + t.table + ` AS f SET ` + strings.ReplaceAll(t.extra, "vulnerabilities w", t.table+" w") + `
			last_seen = (SELECT w.last_seen FROM ` + t.table + ` w WHERE ` + sameIdentity + ` ORDER BY julianday(w.last_seen) DESC LIMIT 1),
			modified_at = (SELECT w.modified_at FROM ` + t.table + ` w WHERE ` + sameIdentity + ` ORDER BY julianday(w.modified_at) DESC LIMIT 1)
			WHERE f.id IN (` + keepers + `)`)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("merge %s: %w", t.table, err)
		}
		res, err := tx.Exec(`DELETE FROM ` + t.table + ` WHERE source_id IS NOT NULL AND id NOT IN (
			SELECT MIN(id) FROM ` + t.table + ` WHERE source_id IS NOT NULL GROUP BY user_id, source_id, host, port, name)`)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("delete the merged %s: %w", t.table, err)
		}
		if _, err := tx.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_" + t.table + "_identity ON " + t.table + " (user_id, source_id, host, port, name)"); err != nil {
			tx.Rollback()
			return fmt.Errorf("index %s: %w", t.table, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			logger.Infof("Migration v8: merged %d %s that repeated another of the same owner, source, host, port and name (AM-DATA-47)", n, t.table)
		}
	}
	return nil
}

// sameIdentity matches, in a subquery aliased w, the findings of the outer
// record f's identity.
const sameIdentity = "w.user_id = f.user_id AND w.source_id = f.source_id AND w.host = f.host AND w.port = f.port AND w.name = f.name"
