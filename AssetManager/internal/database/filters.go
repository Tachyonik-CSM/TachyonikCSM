// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The filter every list takes — a set of rules joined by AND or OR — and the
// SQL of each kind's rules: assets, vulnerabilities and detections.

package database

import (
	"fmt"
	"strconv"
	"strings"
)

// FilterRule is one condition of a list filter. Which columns and operators
// it accepts depends on what is listed; see the per-kind rule functions.
type FilterRule struct {
	Column   string `json:"column"`
	Operator string `json:"operator"`
	Value    string `json:"value"` // parsed as a number for the numeric columns; empty for "is set" / "is not set"
}

// Filter is a list filter: its rules joined by AND (MatchAll) or OR.
type Filter struct {
	Rules    []FilterRule `json:"rules"`
	MatchAll bool         `json:"matchAll"`
}

// The per-kind names, kept so callers read as what they filter.
type (
	// AssetFilterRule columns: "name", "type", "severity", "score",
	// "lastSeenDays", "lastScannedDays", "coverageVerifiedDays",
	// "scanCoverageIntervalDays", "detections". Operators: ">", "<", ">=",
	// "<=", "=", "contains", "does not contain"; "is set" and "is not set"
	// (no value) on lastScannedDays, coverageVerifiedDays and
	// scanCoverageIntervalDays.
	AssetFilterRule = FilterRule
	AssetFilter     = Filter

	// VulnerabilityFilterRule columns: "severity" (numeric operators), "name"
	// and "host" ("contains", "does not contain").
	VulnerabilityFilterRule = FilterRule
	VulnerabilityFilter     = Filter

	// DetectionFilterRule columns: "name" and "host" ("contains", "does not
	// contain").
	DetectionFilterRule = FilterRule
	DetectionFilter     = Filter
)

// buildFilterSQL joins the conditions ruleSQL gives for the filter's rules. A
// rule it cannot read (an unknown column or operator, an unparsable value)
// gives no condition and is left out; with none left there is no condition.
func buildFilterSQL(filter *Filter, ruleSQL func(FilterRule) (string, []interface{})) (string, []interface{}) {
	if filter == nil {
		return "", nil
	}
	var conditions []string
	var args []interface{}
	for _, rule := range filter.Rules {
		if cond, condArgs := ruleSQL(rule); cond != "" {
			conditions = append(conditions, cond)
			args = append(args, condArgs...)
		}
	}
	joiner := " OR "
	if filter.MatchAll {
		joiner = " AND "
	}
	return strings.Join(conditions, joiner), args
}

// textRuleSQL is a "contains" / "does not contain" rule on a text column.
func textRuleSQL(column string, rule FilterRule) (string, []interface{}) {
	switch rule.Operator {
	case "contains":
		return column + " LIKE ?", []interface{}{"%" + rule.Value + "%"}
	case "does not contain":
		return column + " NOT LIKE ?", []interface{}{"%" + rule.Value + "%"}
	}
	return "", nil
}

// presenceSQL is the condition of an "is set" or "is not set" rule, which
// takes no value, on expr. Not set is exactly what the numeric operators treat
// as 1e9: for a date that is NULL or one julianday() cannot read, so "is not
// set" selects the same assets as "> 99999". ok is false for any other
// operator.
func presenceSQL(expr, operator string) (cond string, ok bool) {
	switch operator {
	case "is set":
		return expr + " IS NOT NULL", true
	case "is not set":
		return expr + " IS NULL", true
	}
	return "", false
}

// buildAssetFilterSQL constructs the SQL WHERE clause for the asset filter.
// userIDs are the users whose assets are listed: a rule that looks at
// detections looks at theirs, as the statistics do (GetAssetStatsByUsers).
func buildAssetFilterSQL(filter *AssetFilter, userIDs []int64) (string, []interface{}) {
	return buildFilterSQL(filter, func(rule FilterRule) (string, []interface{}) {
		return buildAssetRuleSQL(rule, userIDs)
	})
}

// buildAssetRuleSQL constructs the SQL for a single asset filter rule
func buildAssetRuleSQL(rule AssetFilterRule, userIDs []int64) (string, []interface{}) {
	switch rule.Column {
	case "detections":
		// How many detections name the asset's host, among the listed users'
		// — the correlation the statistics use to tell No Threat (severity 0,
		// detections) from Unmanaged (severity 0, none): "> 0" and "= 0".
		count, err := strconv.ParseInt(rule.Value, 10, 64)
		if err != nil || len(userIDs) == 0 {
			return "", nil
		}
		switch rule.Operator {
		case ">", "<", ">=", "<=", "=":
			clause, args := buildUserIDsClause(userIDs)
			clause = strings.Replace(clause, "user_id", "d.user_id", 1)
			return fmt.Sprintf("(SELECT COUNT(*) FROM detections d WHERE d.host = assets.name AND %s) %s ?", clause, rule.Operator),
				append(args, count)
		}
	case "severity":
		// Severity comparisons - value is expected to be 0.0-10.0, database stores 0-100
		severityValue, err := strconv.ParseFloat(rule.Value, 64)
		if err != nil {
			return "", nil // Invalid severity value, skip this rule
		}
		switch rule.Operator {
		case ">", "<", ">=", "<=", "=":
			return fmt.Sprintf("(severity / 10.0) %s ?", rule.Operator), []interface{}{severityValue}
		}
	case "score":
		// Score comparisons - value is expected to be 0-10000
		scoreValue, err := strconv.ParseInt(rule.Value, 10, 64)
		if err != nil {
			return "", nil // Invalid score value, skip this rule
		}
		switch rule.Operator {
		case ">", "<", ">=", "<=", "=":
			return fmt.Sprintf("score %s ?", rule.Operator), []interface{}{scoreValue}
		}
	case "name":
		return textRuleSQL("name", rule)
	case "type":
		// Types are a fixed set (Host, …), so a type can also be matched exactly.
		if rule.Operator == "=" {
			return "type = ? COLLATE NOCASE", []interface{}{rule.Value}
		}
		return textRuleSQL("type", rule)
	case "lastSeenDays":
		// How many days ago the asset was last seen, fractions included, so
		// "<= 30" and "> 30" split the assets without overlap. A last_seen
		// that cannot be read counts as seen very long ago, as in the
		// statistics' age buckets (hostAgeBuckets).
		days, err := strconv.ParseFloat(rule.Value, 64)
		if err != nil {
			return "", nil
		}
		switch rule.Operator {
		case ">", "<", ">=", "<=", "=":
			return fmt.Sprintf("COALESCE(julianday('now') - julianday(last_seen), 1e9) %s ?", rule.Operator), []interface{}{days}
		}
	case "lastScannedDays":
		// As lastSeenDays, on last_scanned. An asset never scanned (NULL)
		// counts as scanned very long ago.
		if cond, ok := presenceSQL("julianday(last_scanned)", rule.Operator); ok {
			return cond, nil
		}
		days, err := strconv.ParseFloat(rule.Value, 64)
		if err != nil {
			return "", nil
		}
		switch rule.Operator {
		case ">", "<", ">=", "<=", "=":
			return fmt.Sprintf("COALESCE(julianday('now') - julianday(last_scanned), 1e9) %s ?", rule.Operator), []interface{}{days}
		}
	case "coverageVerifiedDays":
		// As lastSeenDays, on the last scan coverage verification. Never
		// verified counts as verified very long ago.
		if cond, ok := presenceSQL("julianday(last_scan_coverage_verification)", rule.Operator); ok {
			return cond, nil
		}
		days, err := strconv.ParseFloat(rule.Value, 64)
		if err != nil {
			return "", nil
		}
		switch rule.Operator {
		case ">", "<", ">=", "<=", "=":
			return fmt.Sprintf("COALESCE(julianday('now') - julianday(last_scan_coverage_verification), 1e9) %s ?", rule.Operator), []interface{}{days}
		}
	case "scanCoverageIntervalDays":
		// An asset without an interval — in no scheduled scan — counts as
		// scanned at a very long interval, as never scanned does for
		// lastScannedDays; "> 99999" is the unmonitored ones, as is "is not set".
		if cond, ok := presenceSQL("scan_coverage_interval_days", rule.Operator); ok {
			return cond, nil
		}
		days, err := strconv.ParseFloat(rule.Value, 64)
		if err != nil {
			return "", nil
		}
		switch rule.Operator {
		case ">", "<", ">=", "<=", "=":
			return fmt.Sprintf("COALESCE(scan_coverage_interval_days, 1e9) %s ?", rule.Operator), []interface{}{days}
		}
	}
	return "", nil
}

// buildVulnerabilityRuleSQL constructs the SQL for a single vulnerability
// filter rule.
func buildVulnerabilityRuleSQL(rule FilterRule) (string, []interface{}) {
	switch rule.Column {
	case "severity":
		// The filter speaks 0.0–10.0; the column stores 0–100.
		severityValue, err := strconv.ParseFloat(rule.Value, 64)
		if err != nil {
			return "", nil
		}
		switch rule.Operator {
		case ">", "<", ">=", "<=", "=":
			return fmt.Sprintf("(severity / 10.0) %s ?", rule.Operator), []interface{}{severityValue}
		}
	case "name", "host":
		return textRuleSQL(rule.Column, rule)
	}
	return "", nil
}

// buildDetectionRuleSQL constructs the SQL for a single detection filter rule.
func buildDetectionRuleSQL(rule FilterRule) (string, []interface{}) {
	switch rule.Column {
	case "name", "host":
		return textRuleSQL(rule.Column, rule)
	}
	return "", nil
}
