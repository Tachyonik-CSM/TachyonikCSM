// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package codegen turns an analysis rule into executable JavaScript by
// prompting an AI chat client with the module system prompt and the rule, then
// cleaning up the model's output — stripping markdown code fences and
// normalizing Unicode smart quotes to ASCII — so the result parses under the
// goja ES5.1 runtime. It defines the ChatClient interface the AI providers
// implement.
package codegen

import (
	"encoding/json"
	"fmt"
	"strings"

	"tachyonik/lib/logger"
)

// ChatClient is the interface for AI chat providers (ollama, claude, etc.)
type ChatClient interface {
	Chat(model string, systemPrompt string, userPrompt string) (string, error)
}

// AnalysisRule contains the fields needed by the code generator.
// Defined here to avoid a circular import with the api package.
type AnalysisRule struct {
	ID          int64  `json:"id"`
	Description string `json:"description"`
	Type        string `json:"type"`
	RulePrompt  string `json:"rulePrompt"`
}

// CodeGenerator generates JavaScript rule code from analysis rules using AI
type CodeGenerator struct {
	chatClient   ChatClient
	model        string
	systemPrompt string
}

// New creates a new CodeGenerator. systemPrompt is the prompt configured
// in AIManager (Settings → SourceAnalyser → System Prompt). Empty is a
// valid input — Generate refuses to call the AI in that case so the
// failure is precise rather than producing analysis code under an
// empty system message.
func New(chatClient ChatClient, model, systemPrompt string) *CodeGenerator {
	return &CodeGenerator{
		chatClient:   chatClient,
		model:        model,
		systemPrompt: systemPrompt,
	}
}

// Generate sends a single rule to the AI and returns generated JavaScript code
func (g *CodeGenerator) Generate(rule AnalysisRule) (string, error) {
	if g.systemPrompt == "" {
		return "", fmt.Errorf("code generation refused: system prompt not configured in AIManager (Settings → SourceAnalyser → System Prompt)")
	}
	ruleJSON, err := json.MarshalIndent(rule, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal rule: %w", err)
	}

	userPrompt := fmt.Sprintf("Generate JavaScript analysis rule code for the following analysis rule:\n\n%s", string(ruleJSON))

	logger.Infof("Sending rule %d (%s) to model %s for code generation...", rule.ID, rule.Description, g.model)

	code, err := g.chatClient.Chat(g.model, g.systemPrompt, userPrompt)
	if err != nil {
		return "", fmt.Errorf("AI code generation failed: %w", err)
	}

	// Strip markdown fences if the model wrapped the code
	code = stripMarkdownFences(code)

	// Sanitize Unicode quotation marks that break the JS parser
	code = sanitizeQuotes(code)

	// Prepend metadata header comment
	header := fmt.Sprintf("// Analysis Rule ID: %d\n// Description: %s\n// Type: %s\n", rule.ID, rule.Description, rule.Type)
	code = header + code

	logger.Infof("AI generated %d characters of JavaScript code for rule %d", len(code), rule.ID)

	return code, nil
}

// sanitizeQuotes replaces Unicode quotation marks with ASCII equivalents.
// LLMs sometimes copy smart/curly quotes from input data into JS string literals,
// which breaks the goja ES5.1 parser.
func sanitizeQuotes(code string) string {
	replacer := strings.NewReplacer(
		"\u201c", "'", // " LEFT DOUBLE QUOTATION MARK
		"\u201d", "'", // " RIGHT DOUBLE QUOTATION MARK
		"\u201e", "'", // „ DOUBLE LOW-9 QUOTATION MARK
		"\u201f", "'", // ‟ DOUBLE HIGH-REVERSED-9 QUOTATION MARK
		"\u2018", "'", // ' LEFT SINGLE QUOTATION MARK
		"\u2019", "'", // ' RIGHT SINGLE QUOTATION MARK
		"\u201a", "'", // ‚ SINGLE LOW-9 QUOTATION MARK
		"\u201b", "'", // ‛ SINGLE HIGH-REVERSED-9 QUOTATION MARK
		"\u00ab", "'", // « LEFT-POINTING DOUBLE ANGLE QUOTATION MARK
		"\u00bb", "'", // » RIGHT-POINTING DOUBLE ANGLE QUOTATION MARK
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
