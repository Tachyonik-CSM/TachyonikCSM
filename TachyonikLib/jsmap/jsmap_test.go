// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests reading fields out of a routine's returned map.
//
// The numeric cases are the point: goja yields int64, float64 or int for the
// same JavaScript number depending on how it was arrived at, and four modules
// each had their own copy of this handling.

package jsmap

import "testing"

func TestString(t *testing.T) {
	m := map[string]interface{}{
		"title":  "Update nmap",
		"empty":  "",
		"number": 42,
		"null":   nil,
	}
	cases := []struct{ key, want string }{
		{"title", "Update nmap"},
		{"empty", ""},
		{"number", ""},  // wrong type yields the zero value, not a panic
		{"null", ""},    // a JS null arrives as nil
		{"missing", ""}, // an absent key is not an error
	}
	for _, c := range cases {
		if got := String(m, c.key); got != c.want {
			t.Errorf("String(%q) = %q, want %q", c.key, got, c.want)
		}
	}
	if got := String(nil, "anything"); got != "" {
		t.Errorf("String on a nil map = %q", got)
	}
}

func TestInt(t *testing.T) {
	m := map[string]interface{}{
		"fromLiteral":    int64(50),
		"fromArithmetic": float64(75),
		"fromGo":         int(10),
		"fractional":     float64(7.9),
		"negative":       int64(-3),
		"text":           "50",
		"null":           nil,
	}
	cases := []struct {
		key  string
		want int
	}{
		{"fromLiteral", 50},
		{"fromArithmetic", 75},
		{"fromGo", 10},
		{"fractional", 7}, // truncated, as a Go conversion does
		{"negative", -3},
		{"text", 0}, // a numeric string is not a number
		{"null", 0},
		{"missing", 0},
	}
	for _, c := range cases {
		if got := Int(m, c.key); got != c.want {
			t.Errorf("Int(%q) = %d, want %d", c.key, got, c.want)
		}
	}
	if got := Int(nil, "anything"); got != 0 {
		t.Errorf("Int on a nil map = %d", got)
	}
}
