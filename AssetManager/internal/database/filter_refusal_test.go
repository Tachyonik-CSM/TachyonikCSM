// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that a filter rule that cannot be read is refused, naming the rule and
// why (AM-API-05), instead of being left out — which listed more than was
// asked for — while every readable rule still works.

package database

import (
	"strings"
	"testing"
)

func TestUnreadableFilterRulesAreRefused(t *testing.T) {
	for _, c := range []struct {
		what  string
		check func(*Filter) error
		rule  FilterRule
		want  string
	}{
		{"an unknown asset column", CheckAssetFilter, FilterRule{Column: "owner", Operator: "contains", Value: "x"}, `unknown column "owner"`},
		{"an operator the column does not take", CheckAssetFilter, FilterRule{Column: "severity", Operator: "contains", Value: "5"}, `"severity" takes`},
		{"a value that is not a number", CheckAssetFilter, FilterRule{Column: "lastSeenDays", Operator: ">", Value: "abc"}, `needs a number`},
		{"a fraction where a whole number is needed", CheckAssetFilter, FilterRule{Column: "score", Operator: ">", Value: "1.5"}, `whole number`},
		{"an empty number", CheckAssetFilter, FilterRule{Column: "detections", Operator: "=", Value: ""}, `whole number`},
		{"an unknown vulnerability column", CheckVulnerabilityFilter, FilterRule{Column: "port", Operator: "=", Value: "22"}, `unknown column "port"`},
		{"a vulnerability severity that is not a number", CheckVulnerabilityFilter, FilterRule{Column: "severity", Operator: ">", Value: "high"}, `needs a number`},
		{"an unknown detection operator", CheckDetectionFilter, FilterRule{Column: "name", Operator: "=", Value: "ssh"}, `"name" takes contains, does not contain`},
	} {
		err := c.check(&Filter{Rules: []FilterRule{{Column: "name", Operator: "contains", Value: "10."}, c.rule}, MatchAll: true})
		if err == nil {
			t.Errorf("%s: accepted", c.what)
			continue
		}
		if !strings.HasPrefix(err.Error(), "Filter rule 2: ") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %q, want rule 2 and %q", c.what, err, c.want)
		}
	}
}

func TestReadableFilterRulesStillWork(t *testing.T) {
	for _, r := range []FilterRule{
		{Column: "name", Operator: "contains", Value: ""}, // matches everything, as before
		{Column: "type", Operator: "=", Value: "Host"},
		{Column: "severity", Operator: ">=", Value: " 7.5 "},
		{Column: "score", Operator: "<", Value: "500"},
		{Column: "lastScannedDays", Operator: "is not set"},
		{Column: "scanCoverageIntervalDays", Operator: "<=", Value: "7"},
		{Column: "detections", Operator: "=", Value: "0"},
	} {
		if err := CheckAssetFilter(&Filter{Rules: []FilterRule{r}}); err != nil {
			t.Errorf("%+v refused: %v", r, err)
		}
	}
	if err := CheckAssetFilter(nil); err != nil {
		t.Errorf("no filter refused: %v", err)
	}
	if err := CheckVulnerabilityFilter(&Filter{}); err != nil {
		t.Errorf("a filter without rules refused: %v", err)
	}
}
