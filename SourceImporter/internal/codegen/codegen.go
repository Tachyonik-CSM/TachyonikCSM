// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package codegen turns an import rule into JavaScript by prompting the
// configured AI provider with the module's system prompt.
//
// It also cleans up after the model: stripping the markdown fences that wrap
// generated code, and replacing Unicode quotation marks with their ASCII
// equivalents, since a smart quote the model produced is a syntax error to the
// JavaScript engine that has to run the result.
package codegen

import (
	"fmt"

	"tachyonik/lib/aicode"
	"tachyonik/lib/logger"
)

// ChatClient is the interface for AI chat providers (ollama, claude, etc.)
type ChatClient interface {
	Chat(model string, systemPrompt string, userPrompt string) (string, error)
}

// ImportRule represents an import rule from ResourceManager (local to codegen)
type ImportRule struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"`
	Version     string `json:"version"`
	Description string `json:"description"`
	RulePrompt  string `json:"rulePrompt"`
}

// CodeGenerator generates JavaScript import code from import rules using AI
type CodeGenerator struct {
	chatClient   ChatClient
	model        string
	systemPrompt string
}

// New creates a new CodeGenerator. systemPrompt is the prompt configured
// in AIManager (Settings → SourceImporter → System Prompt). Empty is a
// valid input — Generate refuses to call the AI in that case so the
// failure is precise rather than producing import code under an unknown
// implicit prompt.
func New(chatClient ChatClient, model, systemPrompt string) *CodeGenerator {
	return &CodeGenerator{
		chatClient:   chatClient,
		model:        model,
		systemPrompt: systemPrompt,
	}
}

// Generate sends an import rule to the AI and returns generated JavaScript code
func (g *CodeGenerator) Generate(rule ImportRule) (string, error) {
	if g.systemPrompt == "" {
		return "", fmt.Errorf("code generation refused: system prompt not configured in AIManager (Settings → SourceImporter → System Prompt)")
	}
	userPrompt := fmt.Sprintf("Generate JavaScript import code for the following import rule:\n\nType: %s\nVersion: %s\nDescription: %s\n\nRule Prompt:\n%s",
		rule.Type, rule.Version, rule.Description, rule.RulePrompt)

	logger.Infof("Sending import rule %d (%s) to model %s for code generation...", rule.ID, rule.Type, g.model)

	code, err := g.chatClient.Chat(g.model, g.systemPrompt, userPrompt)
	if err != nil {
		return "", fmt.Errorf("AI code generation failed: %w", err)
	}

	// The model may have wrapped the code in a markdown fence and reached
	// for typographic quotes; both would fail to parse.
	code = aicode.Clean(code)

	// Prepend import rule metadata as a header comment
	header := fmt.Sprintf("// Import Rule ID: %d\n// Type: %s\n// Version: %s\n// Description: %s\n\n",
		rule.ID, rule.Type, rule.Version, rule.Description)
	code = header + code

	logger.Infof("AI generated %d characters of JavaScript code for rule %d", len(code), rule.ID)

	return code, nil
}
