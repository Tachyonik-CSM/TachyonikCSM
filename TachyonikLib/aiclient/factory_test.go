// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests which client a provider name selects.
//
// The mapping was duplicated in five modules, and the case that matters most is
// the one that returns nothing: an unrecognised provider must not be quietly
// routed to some other wire format, because the failure then surfaces deep
// inside an API call instead of as "AI is not configured".

package aiclient

import (
	"testing"
	"time"

	"tachyonik/lib/aiclient/claude"
	"tachyonik/lib/aiclient/ollama"
	"tachyonik/lib/aiclient/openai"
)

func TestForProvider(t *testing.T) {
	cases := []struct {
		provider string
		want     string // %T of the expected client
	}{
		{"ollama", "*ollama.Client"},
		{"anthropic", "*claude.Client"},
		{"openai", "*openai.Client"},
		{"mistral", "*openai.Client"},
		{"google", "*openai.Client"},
		{"manual", "*openai.Client"},
	}

	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			got := ForProvider(c.provider, "http://localhost:1234", "key", time.Second)
			if got == nil {
				t.Fatalf("provider %q produced no client", c.provider)
			}
			switch got.(type) {
			case *ollama.Client:
				if c.want != "*ollama.Client" {
					t.Errorf("provider %q produced an ollama client, want %s", c.provider, c.want)
				}
			case *claude.Client:
				if c.want != "*claude.Client" {
					t.Errorf("provider %q produced a claude client, want %s", c.provider, c.want)
				}
			case *openai.Client:
				if c.want != "*openai.Client" {
					t.Errorf("provider %q produced an openai client, want %s", c.provider, c.want)
				}
			default:
				t.Errorf("provider %q produced an unexpected %T", c.provider, got)
			}
		})
	}
}

// An unknown provider yields a genuinely nil interface, so `client == nil` in
// the caller is true and AI is reported as not configured.
func TestUnknownProviderIsNil(t *testing.T) {
	for _, provider := range []string{"", "gemini", "llama", "Anthropic", "OPENAI"} {
		if got := ForProvider(provider, "http://x", "k", time.Second); got != nil {
			t.Errorf("provider %q produced %T, want nil", provider, got)
		}
	}
}

// A zero timeout means the default rather than "time out immediately".
func TestZeroTimeoutUsesTheDefault(t *testing.T) {
	if got := ForProvider("anthropic", "http://x", "k", 0); got == nil {
		t.Fatal("a zero timeout produced no client")
	}
	if got := ForProvider("anthropic", "http://x", "k", -time.Second); got == nil {
		t.Fatal("a negative timeout produced no client")
	}
}
