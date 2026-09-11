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
	"strings"

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

	// Strip markdown fences if the model wrapped the code
	code = stripMarkdownFences(code)

	// Sanitize Unicode quotation marks that break the JS parser
	code = sanitizeQuotes(code)

	// Prepend import rule metadata as a header comment
	header := fmt.Sprintf("// Import Rule ID: %d\n// Type: %s\n// Version: %s\n// Description: %s\n\n",
		rule.ID, rule.Type, rule.Version, rule.Description)
	code = header + code

	logger.Infof("AI generated %d characters of JavaScript code for rule %d", len(code), rule.ID)

	return code, nil
}

// sanitizeQuotes replaces Unicode quotation marks with ASCII equivalents.
// LLMs sometimes copy smart/curly quotes from input data into JS string literals,
// which breaks the goja ES5.1 parser.
func sanitizeQuotes(code string) string {
	replacer := strings.NewReplacer(
		"\u201c", "'", // LEFT DOUBLE QUOTATION MARK
		"\u201d", "'", // RIGHT DOUBLE QUOTATION MARK
		"\u201e", "'", // DOUBLE LOW-9 QUOTATION MARK
		"\u201f", "'", // DOUBLE HIGH-REVERSED-9 QUOTATION MARK
		"\u2018", "'", // LEFT SINGLE QUOTATION MARK
		"\u2019", "'", // RIGHT SINGLE QUOTATION MARK
		"\u201a", "'", // SINGLE LOW-9 QUOTATION MARK
		"\u201b", "'", // SINGLE HIGH-REVERSED-9 QUOTATION MARK
		"\u00ab", "'", // LEFT-POINTING DOUBLE ANGLE QUOTATION MARK
		"\u00bb", "'", // RIGHT-POINTING DOUBLE ANGLE QUOTATION MARK
	)
	return replacer.Replace(code)
}

// stripMarkdownFences removes ```javascript ... ``` or ```js ... ``` wrappers
func stripMarkdownFences(code string) string {
	code = strings.TrimSpace(code)

	// Check for ``` prefix
	if strings.HasPrefix(code, "```") {
		// Find the end of the first line (the opening fence)
		firstNewline := strings.Index(code, "\n")
		if firstNewline == -1 {
			return code
		}
		code = code[firstNewline+1:]

		// Find the closing fence
		lastFence := strings.LastIndex(code, "```")
		if lastFence != -1 {
			code = code[:lastFence]
		}

		code = strings.TrimSpace(code)
	}

	return code
}
