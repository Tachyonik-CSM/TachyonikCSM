// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package safehttp holds the three precautions every outbound client in this
// library needs, so they are stated once instead of copied five times: refuse
// redirects (they leak credentials), bound how much of a response we buffer,
// and notice when a credential is about to travel in cleartext.
//
// It is internal to tachyonik/lib — consumers get the behaviour through the
// clients, not by importing this.
package safehttp

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// MaxErrorBody bounds how much of a non-2xx response we buffer to build an
// error message. Enough to carry a provider's explanation, small enough that a
// hostile endpoint cannot grow the heap through the error path.
const MaxErrorBody = 32 << 10 // 32 KiB

// NoRedirect is the CheckRedirect policy for every client here: return the 3xx
// to the caller instead of following it.
//
// Go's http.Client strips Authorization on a cross-host redirect but forwards
// custom headers verbatim, so a client authenticating with x-api-key or
// X-Internal-Service-Key hands its credential to whatever host a 302 names.
// None of the JSON APIs this library talks to legitimately redirect, so the
// safe policy is also the correct one.
func NoRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// NewClient returns an http.Client with the given timeout and the NoRedirect
// policy applied.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: NoRedirect}
}

// ReadLimited buffers at most max bytes from r. A body larger than max is
// reported as an explicit error rather than silently truncated, so a caller
// never hands half a JSON document to a decoder and reports the resulting
// "unexpected EOF" as if the endpoint were malformed.
func ReadLimited(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("response body exceeds the %d byte limit", max)
	}
	return b, nil
}

// ErrorBody reads up to MaxErrorBody of r for use in an error message. Read
// failures yield the empty string: the caller is already reporting a problem
// and a nested read error would only obscure it.
func ErrorBody(r io.Reader) string {
	b, err := io.ReadAll(io.LimitReader(r, MaxErrorBody))
	if err != nil {
		return ""
	}
	return string(b)
}

// CredentialExposed reports whether sending a credential to rawURL would put it
// on the wire unencrypted. It is false when no credential is configured, so the
// deployments that legitimately run without one — a bare Ollama on
// http://localhost:11434, say — stay quiet.
//
// A URL with no scheme, or one this library does not recognise, counts as
// exposed: it is not demonstrably TLS.
func CredentialExposed(rawURL string, hasCredential bool) bool {
	if !hasCredential {
		return false
	}
	u := strings.ToLower(strings.TrimSpace(rawURL))
	return !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "wss://")
}
