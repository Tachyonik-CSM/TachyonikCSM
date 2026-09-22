// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the tidying applied to generated code.
//
// These pin the behaviour five modules previously each implemented for
// themselves, so the shared version has to do what all five did — including
// the cases that look like edge cases and are not: a fence with a language tag,
// a fence with no newline, and a ``` that appears inside the code rather than
// around it.

package aicode

import "testing"

func TestStripMarkdownFences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no fence", "var rules = [];", "var rules = [];"},
		{"plain fence", "```\nvar rules = [];\n```", "var rules = [];"},
		{"language tag", "```javascript\nvar rules = [];\n```", "var rules = [];"},
		{"leading whitespace", "\n\n  ```js\nvar rules = [];\n```  \n", "var rules = [];"},
		{"no closing fence", "```js\nvar rules = [];", "var rules = [];"},
		{"fence with no newline", "```javascript", "```javascript"},
		{
			// The last fence closes it, so a ``` inside a string survives.
			name: "backticks inside the code",
			in:   "```js\nvar s = \"```\";\nvar rules = [];\n```",
			want: "var s = \"```\";\nvar rules = [];",
		},
		{
			// A ``` that is not at the start is not a wrapper.
			name: "unfenced code containing backticks",
			in:   "var s = \"```\";",
			want: "var s = \"```\";",
		},
		{"empty", "", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := StripMarkdownFences(c.in); got != c.want {
				t.Errorf("StripMarkdownFences(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// Every typographic mark a model reaches for becomes the plain apostrophe;
// nothing else is touched.
func TestSanitizeQuotes(t *testing.T) {
	in := "var a = “hello”; var b = ‘x’; var c = «y»;" +
		" var d = „low‟; var e = ‚s‛;"
	want := "var a = 'hello'; var b = 'x'; var c = 'y'; var d = 'low'; var e = 's';"
	if got := SanitizeQuotes(in); got != want {
		t.Errorf("SanitizeQuotes = %q, want %q", got, want)
	}

	// Straight quotes and other punctuation are left as they are.
	untouched := `var s = "plain"; var t = 'also plain'; // em—dash, ellipsis…`
	if got := SanitizeQuotes(untouched); got != untouched {
		t.Errorf("SanitizeQuotes altered text it should not have: %q", got)
	}
}

// Clean does both, fence first: the language tag is only recognisable while
// the fence is intact.
func TestClean(t *testing.T) {
	in := "```javascript\nvar msg = “hello”;\n```"
	want := "var msg = 'hello';"
	if got := Clean(in); got != want {
		t.Errorf("Clean = %q, want %q", got, want)
	}
}

// What a well-behaved model returns must pass through unchanged.
func TestCleanLeavesGoodCodeAlone(t *testing.T) {
	code := "var rules = [{\n\tname: 'test',\n\tcheck: function (ctx) { return ctx.assetCount > 0; }\n}];"
	if got := Clean(code); got != code {
		t.Errorf("Clean altered already-clean code:\n got %q\nwant %q", got, code)
	}
}
