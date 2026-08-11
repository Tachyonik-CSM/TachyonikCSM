// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for the outbound-HTTP precautions: that the redirect policy keeps
// x-api-key and X-Internal-Service-Key from reaching a cross-host redirect
// target, that response reads are bounded and an oversized body surfaces as an
// explicit error rather than a truncated one, and that the cleartext-credential
// check fires only when a credential is actually configured.

package safehttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The reason NoRedirect exists: Go strips Authorization across hosts but
// forwards custom headers, so following a redirect hands x-api-key and
// X-Internal-Service-Key to whatever host the 302 names. Assert the policy
// keeps them at home.
func TestNoRedirect_DoesNotForwardCredentialHeaders(t *testing.T) {
	var reached bool
	var got http.Header

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		got = r.Header.Clone()
	}))
	defer target.Close()

	// A different hostname for the same server, so this is a cross-host redirect.
	elsewhere := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere, http.StatusFound)
	}))
	defer origin.Close()

	req, err := http.NewRequest("POST", origin.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("x-api-key", "SECRET")
	req.Header.Set("X-Internal-Service-Key", "SECRET")

	resp, err := NewClient(5 * time.Second).Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if reached {
		t.Fatalf("redirect was followed; target saw headers %v", got)
	}
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want %d (the 3xx handed back unfollowed)", resp.StatusCode, http.StatusFound)
	}
}

func TestReadLimited(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		max     int64
		wantErr bool
	}{
		{name: "under limit", body: "hello", max: 10},
		{name: "exactly at limit", body: "hello", max: 5},
		{name: "over limit", body: "hello world", max: 5, wantErr: true},
		{name: "empty body", body: "", max: 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadLimited(strings.NewReader(tt.body), tt.max)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ReadLimited(%q, %d) = %q, want an error", tt.body, tt.max, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadLimited(%q, %d): %v", tt.body, tt.max, err)
			}
			if string(got) != tt.body {
				t.Fatalf("ReadLimited = %q, want %q", got, tt.body)
			}
		})
	}
}

func TestErrorBody_Truncates(t *testing.T) {
	got := ErrorBody(strings.NewReader(strings.Repeat("x", MaxErrorBody*2)))
	if len(got) != MaxErrorBody {
		t.Fatalf("len = %d, want %d", len(got), MaxErrorBody)
	}
}

func TestCredentialExposed(t *testing.T) {
	tests := []struct {
		url  string
		cred bool
		want bool
	}{
		{url: "https://api.anthropic.com", cred: true, want: false},
		{url: "wss://aimanager:8085", cred: true, want: false},
		{url: "HTTPS://API.ANTHROPIC.COM", cred: true, want: false},
		{url: "  https://api.anthropic.com  ", cred: true, want: false},
		{url: "http://systemmanager:8083", cred: true, want: true},
		{url: "ws://aimanager:8085", cred: true, want: true},
		{url: "aimanager:8085", cred: true, want: true}, // no scheme: cannot be TLS
		{url: "", cred: true, want: true},
		// No credential configured is the unauthenticated loopback Ollama case:
		// nothing to expose, so nothing to warn about.
		{url: "http://localhost:11434", cred: false, want: false},
		{url: "https://api.anthropic.com", cred: false, want: false},
	}

	for _, tt := range tests {
		if got := CredentialExposed(tt.url, tt.cred); got != tt.want {
			t.Errorf("CredentialExposed(%q, %v) = %v, want %v", tt.url, tt.cred, got, tt.want)
		}
	}
}
