// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The ctx.json binding: the parsed view of a JSON source.
//
// Whether a file is JSON is decided by sniffing its content, never its
// extension. Content that fails to parse yields null rather than an error, so a
// routine sees ctx.json === null and can fall back instead of the whole import
// failing on a file that merely looked like JSON.

package jsruntime

import (
	"encoding/json"
	"strings"
	"unicode"

	"github.com/dop251/goja"
)

// LooksLikeJSON sniffs content to decide whether to attempt JSON parsing.
// Only content is consulted — extensions lie. After stripping BOM and
// leading whitespace the first character must be "{" or "[".
func LooksLikeJSON(content string) bool {
	s := strings.TrimLeft(content, "\uFEFF")
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	if s == "" {
		return false
	}
	c := s[0]
	return c == '{' || c == '['
}

// ParseJSON returns the parsed JSON value as interface{}, or nil when the
// content fails to parse. goja accepts maps/slices/primitives via
// vm.ToValue and exposes them as idiomatic JS objects/arrays.
func ParseJSON(content string) interface{} {
	if content == "" {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return nil
	}
	return v
}

// BuildJSONBinding returns the parsed value wrapped for goja. Passing nil
// yields goja.Null so that `ctx.json === null` in JS.
func BuildJSONBinding(vm *goja.Runtime, parsed interface{}) goja.Value {
	if parsed == nil {
		return goja.Null()
	}
	return vm.ToValue(parsed)
}
