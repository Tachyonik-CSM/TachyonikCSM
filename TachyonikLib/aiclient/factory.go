// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Choosing the wire protocol for a configured AI provider.
//
// Every module that generates code from a prompt had to answer the same
// question — this entry says "mistral", which client speaks that? — and each
// answered it with its own copy of the same switch. The mapping is a property
// of the providers, not of any one module, so it lives here.

package aiclient

import (
	"time"

	"tachyonik/lib/aiclient/claude"
	"tachyonik/lib/aiclient/ollama"
	"tachyonik/lib/aiclient/openai"
)

// ChatClient is the one thing a module needs of a provider: ask a model
// something and get its answer.
//
// Each module declares its own identical interface for its own use; this one
// exists so the factory below has something to return, and any of them is
// satisfied by the same concrete clients.
type ChatClient interface {
	Chat(model string, systemPrompt string, userPrompt string) (string, error)
}

// DefaultTimeout is the HTTP timeout used when a caller passes none. Matches
// what the individual clients' NewClient constructors have always applied.
const DefaultTimeout = 120 * time.Second

// ForProvider returns a client that speaks the named provider's protocol, or
// nil when the name is not one this platform knows.
//
// nil rather than a default client on purpose: routing an unrecognised
// provider to some other wire format produces a confusing failure deep inside
// an API call, where the useful thing is for the caller to see that AI is not
// configured and say so.
//
// openai, mistral, google and manual share the OpenAI-compatible client.
// Google speaks that shape through its /v1beta/openai endpoint, so an entry
// naming google must have a URL pointing there.
//
// A zero timeout means DefaultTimeout.
func ForProvider(provider, url, apiKey string, timeout time.Duration) ChatClient {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	switch provider {
	case "ollama":
		return ollama.NewClientWithTimeout(url, apiKey, timeout)
	case "anthropic":
		return claude.NewClientWithTimeout(url, apiKey, timeout)
	case "openai", "mistral", "google", "manual":
		return openai.NewClientWithTimeout(url, apiKey, timeout)
	default:
		return nil
	}
}
