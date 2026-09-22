// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package aicode tidies the JavaScript a model returns when it was asked for
// code, before anything tries to parse it.
//
// Two things go wrong reliably enough to be worth handling once rather than in
// every module that generates routines. A model asked for code often answers
// with the code inside a markdown fence, because that is how code is written
// everywhere it learned from. And it reaches for typographic quotation marks —
// “ ” ‘ ’ « » — which read correctly to a person and are a syntax error to a
// JavaScript parser.
//
// Neither is a fault in the prompt, and neither is fixable by asking more
// firmly, so the output is cleaned rather than rejected.
package aicode

import "strings"

// quoteReplacer maps every typographic quotation mark a model reaches for onto
// the plain apostrophe. Deliberately one-way and lossy: the target is code, so
// what matters is that the parser accepts it, not that the punctuation reads
// well.
var quoteReplacer = strings.NewReplacer(
	"“", "'", // “ LEFT DOUBLE QUOTATION MARK
	"”", "'", // ” RIGHT DOUBLE QUOTATION MARK
	"„", "'", // „ DOUBLE LOW-9 QUOTATION MARK
	"‟", "'", // ‟ DOUBLE HIGH-REVERSED-9 QUOTATION MARK
	"‘", "'", // ‘ LEFT SINGLE QUOTATION MARK
	"’", "'", // ’ RIGHT SINGLE QUOTATION MARK
	"‚", "'", // ‚ SINGLE LOW-9 QUOTATION MARK
	"‛", "'", // ‛ SINGLE HIGH-REVERSED-9 QUOTATION MARK
	"«", "'", // « LEFT-POINTING DOUBLE ANGLE QUOTATION MARK
	"»", "'", // » RIGHT-POINTING DOUBLE ANGLE QUOTATION MARK
)

// Clean prepares generated code for a parser: the markdown fence comes off,
// then the typographic quotes are normalised.
//
// That order matters. The fence's own language tag ("```javascript") is only
// recognisable while the fence is intact, and a quote substitution made first
// could alter it.
func Clean(code string) string {
	return SanitizeQuotes(StripMarkdownFences(code))
}

// StripMarkdownFences removes a surrounding ``` fence, including the language
// tag on the opening line.
//
// Only a fence that starts the text is removed: code that merely contains ```
// inside a string or a comment is left alone, because the opening fence is what
// says the whole thing was wrapped.
func StripMarkdownFences(code string) string {
	code = strings.TrimSpace(code)

	if !strings.HasPrefix(code, "```") {
		return code
	}

	// Drop the opening fence line, tag and all. Without a newline there is no
	// code after the fence to keep, so the text is returned as it came.
	firstNewline := strings.Index(code, "\n")
	if firstNewline == -1 {
		return code
	}
	code = code[firstNewline+1:]

	// The LAST fence closes it: a routine may legitimately contain ``` inside
	// a string, and cutting at the first one would truncate the code.
	if lastFence := strings.LastIndex(code, "```"); lastFence != -1 {
		code = code[:lastFence]
	}

	return strings.TrimSpace(code)
}

// SanitizeQuotes replaces typographic quotation marks with the plain
// apostrophe, which is what a JavaScript parser accepts.
func SanitizeQuotes(code string) string {
	return quoteReplacer.Replace(code)
}
