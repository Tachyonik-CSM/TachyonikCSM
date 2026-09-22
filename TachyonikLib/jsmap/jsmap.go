// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package jsmap reads fields out of the map a JavaScript routine returned.
//
// goja hands back `map[string]interface{}` with whatever types the routine
// happened to produce, and a routine is written by an AI from a prompt: a field
// may be missing, or a number may arrive as int64, float64 or int depending on
// how it was computed. Every module that runs routines had its own copy of
// these two readers, which is a lot of places for the same "what if it is a
// float64 this time" to be handled slightly differently.
//
// The contract is deliberately forgiving: a missing or wrongly typed field
// yields the zero value rather than an error. The routines are validated before
// they are trusted, and a rule that omits an optional field should not fail the
// whole evaluation.
package jsmap

// String returns m[key] when it is a string, and "" otherwise.
func String(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// Int returns m[key] as an int, and 0 when it is missing or not a number.
//
// All three numeric shapes are accepted because all three occur: goja yields
// int64 for an integer literal, float64 once arithmetic is involved, and int
// for a value Go put into the map on the way in.
func Int(m map[string]interface{}, key string) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case int64:
			return int(n)
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}
