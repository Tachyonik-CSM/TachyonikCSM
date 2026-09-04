// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package providererr

import (
	"strings"
	"testing"
)

const jsonType = "application/json"

// The reasons users actually need to see.
func TestDescribeExtractsProviderErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "anthropic credit balance",
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`,
			want: "invalid_request_error: Your credit balance is too low to access the Anthropic API.",
		},
		{
			name: "anthropic max_tokens over the model limit",
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: 8192 > 4096, which is the maximum allowed"}}`,
			want: "invalid_request_error: max_tokens: 8192 > 4096, which is the maximum allowed",
		},
		{
			name: "openai shape",
			body: `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`,
			want: "invalid_request_error: Incorrect API key provided",
		},
		{
			name: "ollama shape: error is a plain string",
			body: `{"error":"model 'llama9' not found, try pulling it first"}`,
			want: "model 'llama9' not found, try pulling it first",
		},
		{
			name: "message without a usable type",
			body: `{"error":{"message":"Something went wrong","type":"a type with spaces"}}`,
			want: "Something went wrong",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Describe(jsonType, []byte(c.body)); got != c.want {
				t.Errorf("Describe() = %q, want %q", got, c.want)
			}
		})
	}
}

// Every gate that stands between an arbitrary host's response and the user.
// Each of these must yield nothing at all.
func TestDescribeRevealsNothingForNonEnvelopes(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		body        string
	}{
		{"html error page", "text/html; charset=utf-8", `<html><body>Internal admin console</body></html>`},
		{"json body served as text", "text/plain", `{"error":{"message":"secret"}}`},
		{"json body with no content type", "", `{"error":{"message":"secret"}}`},
		{"octet stream", "application/octet-stream", `{"error":{"message":"secret"}}`},
		{"cloud metadata document", jsonType, `{"AccessKeyId":"AKIA...","SecretAccessKey":"s3cr3t","Token":"..."}`},
		{"json array", jsonType, `[{"error":{"message":"secret"}}]`},
		{"bare string", jsonType, `"just a string"`},
		{"error is a number", jsonType, `{"error":42}`},
		{"error object without a message", jsonType, `{"error":{"code":"boom","detail":"internal state"}}`},
		{"error object with an empty message", jsonType, `{"error":{"message":"   "}}`},
		{"error object with a non-string message", jsonType, `{"error":{"message":{"nested":"secret"}}}`},
		{"not json at all", jsonType, `not json`},
		{"empty body", jsonType, ``},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Describe(c.contentType, []byte(c.body)); got != "" {
				t.Errorf("Describe() leaked %q, want \"\"", got)
			}
		})
	}
}

// A JSON content type with parameters, and the +json convention, still count.
func TestDescribeAcceptsJSONVariants(t *testing.T) {
	for _, ct := range []string{"application/json", "application/json; charset=utf-8", "application/problem+json"} {
		if got := Describe(ct, []byte(`{"error":"boom"}`)); got != "boom" {
			t.Errorf("content type %q: Describe() = %q, want \"boom\"", ct, got)
		}
	}
}

// Whatever comes back is one line: a message travels into logs and the UI, and
// a provider that could inject newlines could forge log entries.
func TestDescribeFlattensToOneLine(t *testing.T) {
	got := Describe(jsonType, []byte("{\"error\":\"line one\\nline two\\r\\n\\tINFO forged log entry\"}"))
	if strings.ContainsAny(got, "\n\r\t") {
		t.Errorf("Describe() kept control characters: %q", got)
	}
	if got != "line one line two INFO forged log entry" {
		t.Errorf("Describe() = %q", got)
	}
}

// A long message is cut, so a matching envelope cannot be used to siphon a
// document a few hundred characters at a time.
func TestDescribeTruncatesLongMessages(t *testing.T) {
	body := `{"error":"` + strings.Repeat("A", 5000) + `"}`
	got := Describe(jsonType, []byte(body))
	if len([]rune(got)) > MaxMessage+1 { // +1 for the ellipsis
		t.Errorf("Describe() returned %d runes, want at most %d", len([]rune(got)), MaxMessage+1)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a truncated message is not marked: %q", got)
	}
}

// Only the first MaxBody bytes are examined, so an enormous response cannot be
// walked for a matching envelope buried at the end.
func TestDescribeIgnoresBodiesBeyondTheCap(t *testing.T) {
	padded := `{"padding":"` + strings.Repeat("x", MaxBody) + `","error":"buried"}`
	if got := Describe(jsonType, []byte(padded)); got != "" {
		t.Errorf("Describe() read past the cap and returned %q", got)
	}
}
