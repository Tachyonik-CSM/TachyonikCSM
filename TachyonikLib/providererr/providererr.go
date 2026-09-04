// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package providererr turns a failed AI-provider HTTP response into a short
// message that is safe to show the user who configured that provider.
//
// The problem it solves: when a user supplies their own provider endpoint, our
// server fetches a URL of their choosing. Returning that response's body to
// them would turn the fetch into a READ primitive — the classic SSRF
// amplification — so the AI clients used to report nothing but "API returned
// status 400". Correct, and useless: the reason a key is refused ("Your credit
// balance is too low", "max_tokens: 8192 > 4096") lives in that body, and
// without it a user cannot tell a billing problem from a typo.
//
// So no body is ever echoed. What is echoed is at most two FIELDS lifted out of
// a document that has to look like a provider error envelope:
//
//	{"error": {"message": "...", "type": "..."}}   // Anthropic, OpenAI, most
//	{"error": "..."}                               // Ollama and some others
//
// A response has to pass every one of these to yield anything at all:
//
//   - it declares a JSON content type;
//   - it parses as a JSON object;
//   - that object has an "error" member which is an object or a string;
//   - an object one has a "message" member which is a non-empty string.
//
// and what comes back is then flattened to one line, stripped of control
// characters and cut to MaxMessage runes. A cloud metadata service, an internal
// admin API, a database or an HTML error page satisfies none of this; a host
// that did would still yield a few hundred characters of a single field rather
// than its document.
//
// This is a deliberate, bounded reduction of a "zero bytes" rule to a "two
// gated fields" rule — not a removal. It sits BEHIND the guards that already
// decide which hosts may be contacted at all (the URL validation for
// user-supplied endpoints and the guarded HTTP client for untrusted configs),
// and changes none of them.
package providererr

import (
	"encoding/json"
	"mime"
	"regexp"
	"strings"
	"unicode"
)

// MaxBody is the most of a response body that is examined. A provider error
// envelope is a few hundred bytes; anything beyond this is not one.
const MaxBody = 8 << 10

// MaxMessage is the longest message returned, in runes.
const MaxMessage = 300

// typePattern is what an error "type" has to look like to be repeated:
// a short machine token such as invalid_request_error, nothing else.
var typePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// envelope is the shape a body must have. `error` is decoded late because
// providers disagree on whether it is an object or a plain string.
type envelope struct {
	Error json.RawMessage `json:"error"`
}

type errorObject struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// Describe extracts a displayable reason from a failed provider response, or
// returns "" when the response is not a recognisable provider error envelope.
//
// contentType is the raw Content-Type header; body is the response body, of
// which at most MaxBody bytes are read.
func Describe(contentType string, body []byte) string {
	if !isJSON(contentType) {
		return ""
	}
	if len(body) > MaxBody {
		body = body[:MaxBody]
	}

	var env envelope
	if err := json.Unmarshal(body, &env); err != nil || len(env.Error) == 0 {
		return ""
	}

	// {"error": "some text"} — Ollama and several OpenAI-compatible servers.
	var asString string
	if err := json.Unmarshal(env.Error, &asString); err == nil {
		return clean(asString)
	}

	// {"error": {"message": "...", "type": "..."}} — Anthropic, OpenAI.
	var obj errorObject
	if err := json.Unmarshal(env.Error, &obj); err != nil {
		return ""
	}
	message := clean(obj.Message)
	if message == "" {
		return ""
	}
	if typePattern.MatchString(obj.Type) {
		return obj.Type + ": " + message
	}
	return message
}

// isJSON reports whether the header names a JSON media type. A body served as
// text/html or application/octet-stream is not a provider error envelope,
// whatever it may contain.
func isJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

// clean flattens a message to one printable line and cuts it to MaxMessage.
//
// Control characters go because a message travels into logs and into the UI:
// newlines would let a provider — or whatever answered in its place — forge log
// lines, and the rest have no business in a sentence shown to a user.
func clean(s string) string {
	var b strings.Builder
	lastSpace := true // trims leading space as a side effect
	for _, r := range s {
		// Control characters, and the zero-width no-break space that would
		// otherwise ride along invisibly.
		if unicode.IsControl(r) || r == '\uFEFF' {
			r = ' '
		}
		if r == ' ' {
			if lastSpace {
				continue
			}
			lastSpace = true
		} else {
			lastSpace = false
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())

	runes := []rune(out)
	if len(runes) > MaxMessage {
		out = strings.TrimSpace(string(runes[:MaxMessage])) + "…"
	}
	return out
}
