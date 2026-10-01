// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests which AI a generation uses, and how it is named.
//
// The case these are written around: a module's default was changed in
// AIManager, the change notification never reached the module, and it went on
// generating with the old AI — an account without credit — while its error
// named no AI at all.

package aipick

import (
	"errors"
	"strings"
	"testing"

	"tachyonik/lib/aiclient"
)

type fakeClient struct{ name string }

func (f *fakeClient) Chat(string, string, string) (string, error) { return f.name, nil }

func buildFake(e Entry) aiclient.ChatClient {
	if e.Provider == "unknown" {
		return nil
	}
	return &fakeClient{name: e.Name}
}

var (
	oldAI   = Entry{ID: 6, Name: "claude opus 5", Provider: "anthropic", Model: "claude-opus-5", APIKey: "sk-old"}
	newAI   = Entry{ID: 8, Name: "claude opus 5 Tachyonik", Provider: "anthropic", Model: "claude-opus-5", APIKey: "sk-new"}
	mistral = Entry{ID: 2, Name: "Mistral Large 3", Provider: "mistral", Model: "mistral-large-latest"}
)

// loadedOld is what a module holds after loading AI 6 at startup.
func loadedOld() Choice {
	return Choice{Client: &fakeClient{name: oldAI.Name}, Entry: oldAI, SystemPrompt: "old prompt"}
}

func lookup(setting *ModuleSetting, settingErr error, byID map[int64]Entry) Lookup {
	return Lookup{
		ByID: func(id int64) (*Entry, error) {
			if e, ok := byID[id]; ok {
				return &e, nil
			}
			return nil, errors.New("not found")
		},
		ModuleSetting: func() (*ModuleSetting, error) { return setting, settingErr },
		Build:         buildFake,
	}
}

// The regression: the default is what AIManager says now, not what was loaded.
func TestTheCurrentDefaultWinsOverTheLoadedOne(t *testing.T) {
	l := lookup(&ModuleSetting{AI: &newAI, SystemPrompt: "new prompt"}, nil, nil)
	c, err := l.Pick(nil, loadedOld())
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if c.Entry.ID != 8 || c.Reason != ReasonDefault {
		t.Errorf("picked %s, want AI 8 as module default", c.Describe())
	}
	if c.SystemPrompt != "new prompt" {
		t.Errorf("system prompt %q, want the current one", c.SystemPrompt)
	}
}

func TestARulesOwnAIComesFirst(t *testing.T) {
	l := lookup(&ModuleSetting{AI: &newAI, SystemPrompt: "p"}, nil, map[int64]Entry{2: mistral})
	id := int64(2)
	c, err := l.Pick(&id, loadedOld())
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if c.Entry.ID != 2 || c.Reason != ReasonRule {
		t.Errorf("picked %s, want the rule's AI", c.Describe())
	}
	if c.SystemPrompt != "p" {
		t.Errorf("a rule's AI still generates with the module's prompt; got %q", c.SystemPrompt)
	}
}

// A rule's AI that cannot be loaded falls back to the default, and says so.
func TestAMissingRuleAIFallsBackAndSaysSo(t *testing.T) {
	l := lookup(&ModuleSetting{AI: &newAI}, nil, nil)
	id := int64(99)
	c, err := l.Pick(&id, loadedOld())
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if c.Entry.ID != 8 || !strings.Contains(c.Describe(), "id 99") {
		t.Errorf("got %s, want the default with a note about rule AI 99", c.Describe())
	}
}

func TestAnUnreachableAIManagerUsesTheLoadedAI(t *testing.T) {
	l := lookup(nil, errors.New("connection refused"), nil)
	c, err := l.Pick(nil, loadedOld())
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if c.Entry.ID != 6 || c.Reason != ReasonLoaded || c.SystemPrompt != "old prompt" {
		t.Errorf("got %s (prompt %q), want the loaded AI marked as such", c.Describe(), c.SystemPrompt)
	}
	if !strings.Contains(c.Describe(), "out of date") {
		t.Errorf("the description does not warn that it may be stale: %s", c.Describe())
	}

	// With nothing loaded either, there is no AI.
	if _, err := l.Pick(nil, Choice{}); !errors.Is(err, ErrNoAI) {
		t.Errorf("got %v, want ErrNoAI", err)
	}
}

func TestNoDefaultOrAnUnsupportedOneIsNoAI(t *testing.T) {
	if _, err := lookup(&ModuleSetting{}, nil, nil).Pick(nil, loadedOld()); !errors.Is(err, ErrNoAI) {
		t.Errorf("no default: got %v, want ErrNoAI", err)
	}
	odd := Entry{ID: 9, Name: "odd", Provider: "unknown"}
	if _, err := lookup(&ModuleSetting{AI: &odd}, nil, nil).Pick(nil, loadedOld()); !errors.Is(err, ErrNoAI) {
		t.Errorf("unsupported provider: got %v, want ErrNoAI", err)
	}
}

func TestDescribe(t *testing.T) {
	c := Choice{Entry: oldAI, Reason: ReasonDefault}
	want := `AI "claude opus 5" (id 6, anthropic, model claude-opus-5; module default)`
	if got := c.Describe(); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if strings.Contains(c.Describe(), "sk-old") {
		t.Error("the description carries the API key")
	}
}
