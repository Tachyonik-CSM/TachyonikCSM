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

// FilterError says why a filter cannot be read: which rule, and what is wrong
// with it. The lists refuse such a filter with 400 (AM-API-05) rather than
// leaving the rule out, which would list more than was asked for.
type FilterError struct {
	Rule    int // 1-based
	Problem string
}

func (e *FilterError) Error() string {
	return fmt.Sprintf("Filter rule %d: %s", e.Rule, e.Problem)
}

// ruleBuilder gives a rule's condition, or why the rule cannot be read.
type ruleBuilder func(FilterRule) (cond string, args []interface{}, problem string)

// buildFilterSQL joins the conditions of the filter's rules, or returns the
// first rule that cannot be read.
func buildFilterSQL(filter *Filter, ruleSQL ruleBuilder) (string, []interface{}, error) {
	if filter == nil {
		return "", nil, nil
	}
	var conditions []string
	var args []interface{}
	for i, rule := range filter.Rules {
		cond, condArgs, problem := ruleSQL(rule)
		if problem != "" {
			return "", nil, &FilterError{Rule: i + 1, Problem: problem}
		}
		if cond != "" {
			conditions = append(conditions, cond)
			args = append(args, condArgs...)
		}
	}
	joiner := " OR "
	if filter.MatchAll {
		joiner = " AND "
	}
	return strings.Join(conditions, joiner), args, nil
}

var (
	numericOperators = []string{">", "<", ">=", "<=", "="}
	textOperators    = []string{"contains", "does not contain"}
	presenceOps      = []string{"is set", "is not set"}

	assetColumns_ = []string{"name", "type", "severity", "score", "lastSeenDays", "lastScannedDays",
		"coverageVerifiedDays", "scanCoverageIntervalDays", "detections"}
	vulnerabilityColumns = []string{"severity", "name", "host"}
	detectionColumns     = []string{"name", "host"}
)

func unknownColumn(column string, columns []string) string {
	return fmt.Sprintf("unknown column %q; columns: %s", column, strings.Join(columns, ", "))
}

func unknownOperator(column, operator string, operators []string) string {
	return fmt.Sprintf("%q takes %s, not %q", column, strings.Join(operators, ", "), operator)
}

func notANumber(column, value string) string {
	return fmt.Sprintf("%q needs a number, not %q", column, value)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// textRuleSQL is a "contains" / "does not contain" rule on a text column.
func textRuleSQL(column string, rule FilterRule, extra ...string) (string, []interface{}, string) {
	switch rule.Operator {
	case "contains":
		return column + " LIKE ?", []interface{}{"%" + rule.Value + "%"}, ""
	case "does not contain":
		return column + " NOT LIKE ?", []interface{}{"%" + rule.Value + "%"}, ""
	}
	return "", nil, unknownOperator(rule.Column, rule.Operator, append(append([]string{}, textOperators...), extra...))
}

// numericRuleSQL is a comparison rule whose value must be a number; format
// gets the operator.
func numericRuleSQL(rule FilterRule, format string, integer bool, extraOps ...string) (string, []interface{}, string) {
	if !contains(numericOperators, rule.Operator) {
		return "", nil, unknownOperator(rule.Column, rule.Operator, append(append([]string{}, numericOperators...), extraOps...))
	}
	var value interface{}
	if integer {
		n, err := strconv.ParseInt(strings.TrimSpace(rule.Value), 10, 64)
		if err != nil {
			return "", nil, fmt.Sprintf("%q needs a whole number, not %q", rule.Column, rule.Value)
		}
		value = n
	} else {
		f, err := strconv.ParseFloat(strings.TrimSpace(rule.Value), 64)
		if err != nil {
			return "", nil, notANumber(rule.Column, rule.Value)
		}
		value = f
	}
	return fmt.Sprintf(format, rule.Operator), []interface{}{value}, ""
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
func buildAssetFilterSQL(filter *AssetFilter, userIDs []int64) (string, []interface{}, error) {
	return buildFilterSQL(filter, func(rule FilterRule) (string, []interface{}, string) {
		return buildAssetRuleSQL(rule, userIDs)
	})
}

// CheckAssetFilter returns why an asset filter cannot be read, or nil.
func CheckAssetFilter(filter *AssetFilter) error {
	_, _, err := buildAssetFilterSQL(filter, []int64{0})
	return err
}

// CheckVulnerabilityFilter returns why a vulnerability filter cannot be read, or nil.
func CheckVulnerabilityFilter(filter *VulnerabilityFilter) error {
	_, _, err := buildFilterSQL(filter, buildVulnerabilityRuleSQL)
	return err
}

// CheckDetectionFilter returns why a detection filter cannot be read, or nil.
func CheckDetectionFilter(filter *DetectionFilter) error {
	_, _, err := buildFilterSQL(filter, buildDetectionRuleSQL)
	return err
}

// buildAssetRuleSQL constructs the SQL for a single asset filter rule.
func buildAssetRuleSQL(rule AssetFilterRule, userIDs []int64) (string, []interface{}, string) {
	switch rule.Column {
	case "detections":
		// How many detections name the asset's host, among the listed users'
		// — the correlation the statistics use to tell No Threat (severity 0,
		// detections) from Unmanaged (severity 0, none): "> 0" and "= 0".
		clause, args := buildUserIDsClause(userIDs)
		clause = strings.Replace(clause, "user_id", "d.user_id", 1)
		cond, vargs, problem := numericRuleSQL(rule,
			"(SELECT COUNT(*) FROM detections d WHERE d.host = assets.name AND "+clause+") %s ?", true)
		if problem != "" {
			return "", nil, problem
		}
		return cond, append(args, vargs...), ""
	case "severity":
		// The filter speaks 0.0–10.0; the column stores 0–100.
		return numericRuleSQL(rule, "(severity / 10.0) %s ?", false)
	case "score":
		// 0–10000.
		return numericRuleSQL(rule, "score %s ?", true)
	case "name":
		return textRuleSQL("name", rule)
	case "type":
		// Types are a fixed set (Host, …), so a type can also be matched exactly.
		if rule.Operator == "=" {
			return "type = ? COLLATE NOCASE", []interface{}{rule.Value}, ""
		}
		return textRuleSQL("type", rule, "=")
	case "lastSeenDays":
		// How many days ago the asset was last seen, fractions included, so
		// "<= 30" and "> 30" split the assets without overlap. A last_seen
		// that cannot be read counts as seen very long ago, as in the
		// statistics' age buckets (hostAgeBuckets).
		return numericRuleSQL(rule, "COALESCE(julianday('now') - julianday(last_seen), 1e9) %s ?", false)
	case "lastScannedDays":
		// As lastSeenDays, on last_scanned. An asset never scanned (NULL)
		// counts as scanned very long ago.
		if cond, ok := presenceSQL("julianday(last_scanned)", rule.Operator); ok {
			return cond, nil, ""
		}
		return numericRuleSQL(rule, "COALESCE(julianday('now') - julianday(last_scanned), 1e9) %s ?", false, presenceOps...)
	case "coverageVerifiedDays":
		// As lastSeenDays, on the last scan coverage verification. Never
		// verified counts as verified very long ago.
		if cond, ok := presenceSQL("julianday(last_scan_coverage_verification)", rule.Operator); ok {
			return cond, nil, ""
		}
		return numericRuleSQL(rule, "COALESCE(julianday('now') - julianday(last_scan_coverage_verification), 1e9) %s ?", false, presenceOps...)
	case "scanCoverageIntervalDays":
		// An asset without an interval — in no scheduled scan — counts as
		// scanned at a very long interval, as never scanned does for
		// lastScannedDays; "> 99999" is the unmonitored ones, as is "is not set".
		if cond, ok := presenceSQL("scan_coverage_interval_days", rule.Operator); ok {
			return cond, nil, ""
		}
		return numericRuleSQL(rule, "COALESCE(scan_coverage_interval_days, 1e9) %s ?", false, presenceOps...)
	}
	return "", nil, unknownColumn(rule.Column, assetColumns_)
}

// buildVulnerabilityRuleSQL constructs the SQL for a single vulnerability
// filter rule.
func buildVulnerabilityRuleSQL(rule FilterRule) (string, []interface{}, string) {
	switch rule.Column {
	case "severity":
		// The filter speaks 0.0–10.0; the column stores 0–100.
		return numericRuleSQL(rule, "(severity / 10.0) %s ?", false)
	case "name", "host":
		return textRuleSQL(rule.Column, rule)
	}
	return "", nil, unknownColumn(rule.Column, vulnerabilityColumns)
}

// buildDetectionRuleSQL constructs the SQL for a single detection filter rule.
func buildDetectionRuleSQL(rule FilterRule) (string, []interface{}, string) {
	switch rule.Column {
	case "name", "host":
		return textRuleSQL(rule.Column, rule)
	}
	return "", nil, unknownColumn(rule.Column, detectionColumns)
}
