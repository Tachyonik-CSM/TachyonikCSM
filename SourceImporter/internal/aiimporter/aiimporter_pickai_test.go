// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that generation uses the AI AIManager names now, and names it when it
// fails.
//
// The case behind it: the module default was changed from AI 6 to AI 8, the
// change notification never arrived, and every generation still went to AI 6 —
// whose account had no credit — with an error that named no AI at all.

package aiimporter

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tachyonik/sourceimporter/internal/aimanager"
	"tachyonik/sourceimporter/internal/codegen"
	"tachyonik/sourceimporter/internal/config"
)

// refusingClient fails every request the way an account without credit does.
type refusingClient struct{ asked *string }

func (c *refusingClient) Chat(model, systemPrompt, userPrompt string) (string, error) {
	*c.asked = systemPrompt
	return "", errors.New("Anthropic API returned status 400: credit balance is too low")
}

func TestGenerationUsesTheCurrentDefaultAndNamesIt(t *testing.T) {
	// AIManager now says: AI 8, with the current system prompt.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/internal/module-ai-settings/sourceimporter" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"moduleName":"sourceimporter","defaultAiId":8,"systemPrompt":"current prompt",
			"ai":{"id":8,"name":"claude opus 5 Tachyonik","provider":"anthropic","model":"claude-opus-5"}}`))
	}))
	defer srv.Close()

	// The module still holds what it loaded at startup: AI 6.
	var askedWith string
	staleCalled := false
	cfg := &config.Config{AI: config.AIConfig{AIName: "claude opus 5", Model: "claude-opus-5", SystemPrompt: "old prompt"}}
	ai := New(nil, aimanager.NewClient(srv.URL, ""), nil, staleClient{called: &staleCalled}, cfg)
	var builtFor string
	ai.SetChatClientFactory(func(e *aimanager.AIEntry) codegen.ChatClient {
		builtFor = e.Name
		return &refusingClient{asked: &askedWith}
	})

	err := ai.GenerateForRule(&aimanager.ImportRule{ID: 12, Type: "OPENVAS GMP Host Assets XML"})
	if err == nil {
		t.Fatal("generation succeeded against a refusing AI")
	}
	if staleCalled {
		t.Error("generation went to the AI loaded at startup, not the current default")
	}
	if builtFor != "claude opus 5 Tachyonik" || askedWith != "current prompt" {
		t.Errorf("generated with AI %q and prompt %q, want AIManager's current ones", builtFor, askedWith)
	}
	for _, want := range []string{`AI "claude opus 5 Tachyonik"`, "id 8", "module default", "credit balance"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// staleClient stands in for the AI loaded at startup.
type staleClient struct{ called *bool }

func (c staleClient) Chat(string, string, string) (string, error) {
	*c.called = true
	return "", errors.New("the stale AI was used")
}
