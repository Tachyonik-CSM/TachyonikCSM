// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package listquery reads the optional query parameters a manager's list
// operation offers to order and cut its answer — sort, order and limit — and
// the maxPoints that thins a time series.
//
// They exist for callers that cannot sort or cut an answer themselves, above
// all the chat's AI tools, which are generated one-to-one from the operations:
// "the 25 most severe" has to be something the operation can be asked for.
// Every parameter is optional, and an absent one leaves the answer exactly as
// before, so nothing that calls a list today notices them.
//
// A value is checked, never guessed: an unknown sort field, an order other
// than asc or desc, or a limit outside its range is an error naming what is
// allowed, which the handler answers with 400. Leaving a bad value out would
// answer a different question than the one asked.
//
// A sort field names an answer field ("createdAt") and, for a list read from
// a database, the SQL expression it sorts by. That expression comes only from
// the operation's own field list; the parameter's value is used to look it up,
// never written into a query.

package listquery

import (
	"cmp"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// MaxLimit is the largest limit a list accepts.
const MaxLimit = 500

// Field is one field a list may be sorted by.
type Field struct {
	// Name is the field as the answer spells it, e.g. "createdAt".
	Name string
	// Column is the SQL expression it sorts by, for a list read with a
	// database query; empty for a list sorted in Go.
	Column string
	// Text sorts ascending (A first) when no order is given; any other field
	// (numbers, times) sorts descending (highest, newest first).
	Text bool
}

// Query is a list's checked sort, order and limit.
type Query struct {
	// Sort is the field to sort by; "" keeps the operation's own order.
	Sort string
	// Desc is the direction, meaningful with Sort only.
	Desc bool
	// Limit is the most rows to answer with; 0 is all of them.
	Limit int

	column string
}

// Error is a parameter that cannot be used; its text names what is allowed.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func refuse(format string, a ...interface{}) error {
	return &Error{Message: fmt.Sprintf(format, a...)}
}

// Names lists the fields' names, in their order.
func Names(fields []Field) []string {
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	return names
}

// Parse reads sort, order and limit from q. An empty value counts as absent.
// order without sort is refused: there is no field it could apply to.
func Parse(q url.Values, fields []Field) (Query, error) {
	var out Query
	if s := strings.TrimSpace(q.Get("sort")); s != "" {
		i := slices.IndexFunc(fields, func(f Field) bool { return f.Name == s })
		if i < 0 {
			return Query{}, refuse("sort must be one of %s, not %q", strings.Join(Names(fields), ", "), s)
		}
		out.Sort, out.column, out.Desc = s, fields[i].Column, !fields[i].Text
	}
	if o := strings.TrimSpace(q.Get("order")); o != "" {
		switch {
		case out.Sort == "":
			return Query{}, refuse("order needs sort, the field to order by")
		case o == "asc":
			out.Desc = false
		case o == "desc":
			out.Desc = true
		default:
			return Query{}, refuse("order must be asc or desc, not %q", o)
		}
	}
	limit, err := Int(q, "limit", 1, MaxLimit)
	if err != nil {
		return Query{}, err
	}
	out.Limit = limit
	return out, nil
}

// Int reads the whole-number parameter name, which must lie in min..max; 0
// when it is absent or empty.
func Int(q url.Values, name string, min, max int) (int, error) {
	raw := strings.TrimSpace(q.Get(name))
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return 0, refuse("%s must be a whole number from %d to %d, not %q", name, min, max, raw)
	}
	return n, nil
}

// Sorted reports whether the caller asked for an order of its own.
func (q Query) Sorted() bool { return q.Sort != "" }

// OrderBy is the ORDER BY clause (without the keywords) for a database list:
// defaultOrder without a sort; with one, the sort field's column in the asked
// direction, then tieBreak, so rows that compare equal keep a stable order.
func (q Query) OrderBy(defaultOrder, tieBreak string) string {
	if q.column == "" {
		return defaultOrder
	}
	dir := " ASC"
	if q.Desc {
		dir = " DESC"
	}
	if tieBreak == "" {
		return q.column + dir
	}
	return q.column + dir + ", " + tieBreak
}

// LimitSQL is " LIMIT n" when the caller asked for a limit, else "".
func (q Query) LimitSQL() string {
	if q.Limit <= 0 {
		return ""
	}
	return " LIMIT " + strconv.Itoa(q.Limit)
}

// Cut is list cut to the limit.
func Cut[T any](q Query, list []T) []T {
	if q.Limit > 0 && len(list) > q.Limit {
		return list[:q.Limit]
	}
	return list
}

// SortStable sorts list in Go by the asked field, keeping the existing order
// among rows that compare equal. compare(field, a, b) compares two rows by a
// field Parse accepted, ascending. Without a sort the list is left alone.
func SortStable[T any](q Query, list []T, compare func(field string, a, b T) int) {
	if q.Sort == "" {
		return
	}
	slices.SortStableFunc(list, func(a, b T) int {
		c := compare(q.Sort, a, b)
		if q.Desc {
			return -c
		}
		return c
	})
}

// CompareFold compares two strings ignoring case, for a text field.
func CompareFold(a, b string) int {
	return cmp.Compare(strings.ToLower(a), strings.ToLower(b))
}

// Thin is at most n points of pts, evenly spread and always with the first and
// the last; pts itself when it has no more than n. n must be at least 2.
func Thin[T any](pts []T, n int) []T {
	if n < 2 || len(pts) <= n {
		return pts
	}
	out := make([]T, 0, n)
	step := float64(len(pts)-1) / float64(n-1)
	for i := 0; i < n; i++ {
		out = append(out, pts[int(float64(i)*step+0.5)])
	}
	return out
}
