// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for reading a list's sort, order and limit: absent parameters change
// nothing, each field has its default direction, a bad value is refused naming
// what is allowed, the SQL comes from the field list only, and thinning keeps
// the first and the last point.

package listquery

import (
	"net/url"
	"strings"
	"testing"
)

var fields = []Field{
	{Name: "severity", Column: "severity"},
	{Name: "name", Column: "name COLLATE NOCASE", Text: true},
}

func parse(t *testing.T, raw string) (Query, error) {
	t.Helper()
	q, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatal(err)
	}
	return Parse(q, fields)
}

func TestParse(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want Query
		sql  string
	}{
		{"", Query{}, "created_at DESC"},
		{"sort=&order=&limit=", Query{}, "created_at DESC"},
		{"sort=severity", Query{Sort: "severity", Desc: true}, "severity DESC, id DESC"},
		{"sort=severity&order=asc", Query{Sort: "severity"}, "severity ASC, id DESC"},
		{"sort=name", Query{Sort: "name"}, "name COLLATE NOCASE ASC, id DESC"},
		{"sort=name&order=desc&limit=5", Query{Sort: "name", Desc: true, Limit: 5}, "name COLLATE NOCASE DESC, id DESC"},
		{"limit=500", Query{Limit: 500}, "created_at DESC"},
	} {
		got, err := parse(t, c.raw)
		if err != nil {
			t.Errorf("%q: %v", c.raw, err)
			continue
		}
		if got.Sort != c.want.Sort || got.Desc != c.want.Desc || got.Limit != c.want.Limit {
			t.Errorf("%q: got %+v, want %+v", c.raw, got, c.want)
		}
		if sql := got.OrderBy("created_at DESC", "id DESC"); sql != c.sql {
			t.Errorf("%q: ORDER BY %q, want %q", c.raw, sql, c.sql)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	for raw, want := range map[string]string{
		"sort=score":             "sort must be one of severity, name",
		"sort=severity%3BDROP":   "sort must be one of",
		"order=asc":              "order needs sort",
		"sort=name&order=up":     "order must be asc or desc",
		"limit=0":                "limit must be a whole number from 1 to 500",
		"limit=501":              "limit must be a whole number from 1 to 500",
		"limit=2.5":              "limit must be a whole number",
		"limit=ten":              "limit must be a whole number",
		"sort=severity&limit=-1": "limit must be",
	} {
		_, err := parse(t, raw)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %v, want it to contain %q", raw, err, want)
		}
		if _, ok := err.(*Error); err != nil && !ok {
			t.Errorf("%q: error is %T, not *Error", raw, err)
		}
	}
}

func TestLimitSQLAndCut(t *testing.T) {
	if s := (Query{}).LimitSQL(); s != "" {
		t.Errorf("no limit: %q", s)
	}
	if s := (Query{Limit: 7}).LimitSQL(); s != " LIMIT 7" {
		t.Errorf("limit 7: %q", s)
	}
	list := []int{1, 2, 3}
	if got := Cut(Query{Limit: 2}, list); len(got) != 2 {
		t.Errorf("cut to 2: %v", got)
	}
	if got := Cut(Query{}, list); len(got) != 3 {
		t.Errorf("no limit: %v", got)
	}
}

func TestSortStable(t *testing.T) {
	type row struct {
		name string
		sev  int
	}
	compare := func(field string, a, b row) int {
		if field == "name" {
			return CompareFold(a.name, b.name)
		}
		return a.sev - b.sev
	}
	list := []row{{"b", 1}, {"A", 2}, {"c", 2}}
	SortStable(Query{Sort: "severity", Desc: true}, list, compare)
	if list[0].name != "A" || list[1].name != "c" || list[2].name != "b" {
		t.Errorf("by severity, ties in their order: %v", list)
	}
	SortStable(Query{Sort: "name"}, list, compare)
	if list[0].name != "A" || list[1].name != "b" || list[2].name != "c" {
		t.Errorf("by name ignoring case: %v", list)
	}
	SortStable(Query{}, list, func(string, row, row) int { t.Fatal("compared without a sort"); return 0 })
}

func TestThin(t *testing.T) {
	pts := make([]int, 100)
	for i := range pts {
		pts[i] = i
	}
	got := Thin(pts, 10)
	if len(got) != 10 || got[0] != 0 || got[9] != 99 {
		t.Fatalf("thin 100 to 10: %v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("not in order: %v", got)
		}
	}
	if got := Thin(pts, 2); len(got) != 2 || got[0] != 0 || got[1] != 99 {
		t.Errorf("thin to 2: %v", got)
	}
	if got := Thin(pts[:5], 10); len(got) != 5 {
		t.Errorf("fewer than n: %v", got)
	}
}

// A list that pages reads offset, only with a limit; the SQL and Cut skip it.
func TestParseOffset(t *testing.T) {
	parse := func(raw string) (Query, error) {
		v, _ := url.ParseQuery(raw)
		lq, err := Parse(v, fields)
		if err != nil {
			return Query{}, err
		}
		return ParseOffset(v, lq)
	}
	q, err := parse("limit=50&offset=100")
	if err != nil || q.Limit != 50 || q.Offset != 100 || q.LimitSQL() != " LIMIT 50 OFFSET 100" {
		t.Fatalf("limit=50&offset=100: %+v %q %v", q, q.LimitSQL(), err)
	}
	if q, err := parse("limit=50&offset=0"); err != nil || q.LimitSQL() != " LIMIT 50" {
		t.Errorf("offset 0: %q %v", q.LimitSQL(), err)
	}
	for _, raw := range []string{"offset=10", "limit=5&offset=-1", "limit=5&offset=x"} {
		if _, err := parse(raw); err == nil {
			t.Errorf("%s: accepted", raw)
		}
	}
	list := []int{0, 1, 2, 3, 4, 5, 6}
	if got := Cut(Query{Limit: 3, Offset: 3}, list); len(got) != 3 || got[0] != 3 {
		t.Errorf("cut page 2: %v", got)
	}
	if got := Cut(Query{Limit: 3, Offset: 9}, list); len(got) != 0 {
		t.Errorf("cut past the end: %v", got)
	}
}
