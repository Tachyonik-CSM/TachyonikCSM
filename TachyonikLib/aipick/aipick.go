// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package aipick decides which AI generates a routine, at the moment it is
// generated, and says which one it was.
//
// Every module that generates code — SourceAnalyser, SourceImporter,
// ActionGenerator, ActionExecutor, ToolManager's scan rules — used to load its
// default AI at startup and rely on AIManager's change notification to learn of
// a new one. A notification that never arrived left a module generating with
// an AI nobody had configured any more, and its error said nothing about which
// AI that was. So the choice is now made per generation, from what AIManager
// says right then, and a Choice can describe itself for errors and logs.
//
// What is asked for, in order: the AI set on the rule, if any; otherwise the
// module's current default. When AIManager cannot be asked, the AI the module
// last loaded is used, and its description says so.
package aipick

import (
	"errors"
	"fmt"

	"tachyonik/lib/aiclient"
	"tachyonik/lib/logger"
)

// Entry is an AI provider record as AIManager holds it. Modules convert their
// own wire types into this.
type Entry struct {
	ID       int64
	Name     string
	Provider string
	Model    string
	URL      string
	APIKey   string
}

// ModuleSetting is the part of a module's AI setting a generation needs.
type ModuleSetting struct {
	// AI is the module's default AI, or nil when none is set.
	AI *Entry
	// SystemPrompt is the module's system prompt.
	SystemPrompt string
}

// Reason says why an AI was chosen.
type Reason string

const (
	// ReasonRule: the rule names its own AI.
	ReasonRule Reason = "set on this rule"
	// ReasonDefault: the module's default, as AIManager reports it now.
	ReasonDefault Reason = "module default"
	// ReasonLoaded: the module's default as last loaded, because AIManager
	// could not be asked. It may be out of date.
	ReasonLoaded Reason = "module default as last loaded — AIManager could not be asked, so it may be out of date"
)

// Choice is the AI a generation uses, and why.
type Choice struct {
	Client aiclient.ChatClient
	Entry  Entry
	Reason Reason
	// Note adds what the reason alone does not say — that a rule's own AI
	// could not be loaded, say, so the default was used instead.
	Note string
	// SystemPrompt is the module's system prompt to generate with.
	SystemPrompt string
}

// Describe names the AI for an error or a log line, e.g.
//
//	AI "claude opus 5" (id 6, anthropic, model claude-opus-5; module default)
//
// Never includes the URL or the key.
func (c Choice) Describe() string {
	var detail string
	if c.Entry.ID != 0 {
		detail = fmt.Sprintf("id %d, ", c.Entry.ID)
	}
	if c.Entry.Provider != "" {
		detail += c.Entry.Provider + ", "
	}
	detail += "model " + c.Entry.Model + "; " + string(c.Reason)
	if c.Note != "" {
		detail += "; " + c.Note
	}
	if c.Entry.Name == "" {
		// A module that loaded its AI before it kept the name.
		return fmt.Sprintf("AI (%s)", detail)
	}
	return fmt.Sprintf("AI %q (%s)", c.Entry.Name, detail)
}

// Lookup is how a module asks AIManager. Both functions are required.
type Lookup struct {
	// ByID fetches one AI provider record.
	ByID func(id int64) (*Entry, error)
	// ModuleSetting fetches the module's current AI setting.
	ModuleSetting func() (*ModuleSetting, error)
	// Build makes a client for an entry, or returns nil for a provider it does
	// not know. Nil means aiclient.ForProvider with the default timeout.
	Build func(Entry) aiclient.ChatClient
}

// ErrNoAI is returned when no AI can be used at all.
var ErrNoAI = errors.New("no AI configured")

// Pick chooses the AI for one generation.
//
// ruleAI is the AI set on the rule, or nil. loaded is what the module loaded
// last — its client, entry and system prompt — and is used only when AIManager
// cannot be asked; a nil Client in it means there is nothing to fall back to.
func (l Lookup) Pick(ruleAI *int64, loaded Choice) (Choice, error) {
	build := l.Build
	if build == nil {
		build = func(e Entry) aiclient.ChatClient { return aiclient.ForProvider(e.Provider, e.URL, e.APIKey, 0) }
	}

	setting, settingErr := l.ModuleSetting()
	systemPrompt := loaded.SystemPrompt
	if settingErr == nil {
		systemPrompt = setting.SystemPrompt
	}

	var note string
	if ruleAI != nil {
		entry, err := l.ByID(*ruleAI)
		switch {
		case err != nil:
			note = fmt.Sprintf("the AI set on this rule (id %d) could not be loaded: %v", *ruleAI, err)
		default:
			if client := build(*entry); client != nil {
				return Choice{Client: client, Entry: *entry, Reason: ReasonRule, SystemPrompt: systemPrompt}, nil
			}
			note = fmt.Sprintf("the AI set on this rule, %q, has a provider this module does not support (%q)", entry.Name, entry.Provider)
		}
		logger.Warnf("%s; using the module default", note)
	}

	if settingErr != nil {
		if loaded.Client == nil {
			return Choice{}, fmt.Errorf("%w: AIManager could not be asked for this module's AI: %v", ErrNoAI, settingErr)
		}
		logger.Warnf("Could not ask AIManager for this module's AI, using the one last loaded: %v", settingErr)
		c := loaded
		c.Reason = ReasonLoaded
		c.Note = note
		return c, nil
	}
	if setting.AI == nil {
		return Choice{}, fmt.Errorf("%w: this module has no default AI in AIManager", ErrNoAI)
	}
	client := build(*setting.AI)
	if client == nil {
		return Choice{}, fmt.Errorf("%w: the module default AI %q has a provider this module does not support (%q)", ErrNoAI, setting.AI.Name, setting.AI.Provider)
	}
	return Choice{Client: client, Entry: *setting.AI, Reason: ReasonDefault, Note: note, SystemPrompt: systemPrompt}, nil
}
