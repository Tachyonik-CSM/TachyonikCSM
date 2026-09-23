// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package codegen turns an action rule into JavaScript by prompting the
// configured AI provider with the module's system prompt.
//
// It also cleans up after the model. The tidying itself — taking off the
// markdown fences a model wraps code in, and replacing Unicode quotation marks
// with ASCII ones, because a smart quote is a syntax error to the engine that
// has to run the result — is TachyonikLib's aicode, shared with the other
// modules that generate code.
//
// What stays here is the check that the routine declares exactly the rule ID it
// was asked for: a model that invents or renumbers rules would otherwise have
// its output stored against the wrong rule.
package codegen

import (
	"encoding/json"
	"fmt"
	"strings"

	"tachyonik/actiongenerator/internal/aimanager"
	"tachyonik/lib/aicode"
	"tachyonik/lib/logger"
)

// ChatClient is the interface for AI chat providers (ollama, claude, etc.)
type ChatClient interface {
	Chat(model string, systemPrompt string, userPrompt string) (string, error)
}

// CodeGenerator generates JavaScript rule code from action rules using AI
type CodeGenerator struct {
	chatClient   ChatClient
	model        string
	systemPrompt string
}

// New creates a new CodeGenerator. systemPrompt is the prompt
// configured in AIManager (Settings → ActionGenerator → System Prompt).
// Empty is a valid input — Generate refuses to call the AI in that case
// so the failure is precise rather than producing rule code under an
// unknown implicit prompt.
func New(chatClient ChatClient, model, systemPrompt string) *CodeGenerator {
	return &CodeGenerator{
		chatClient:   chatClient,
		model:        model,
		systemPrompt: systemPrompt,
	}
}

// Generate sends a single action rule to the AI and returns generated JavaScript code.
// The generated code contains `var rules = [{ ... }]` with exactly one rule object.
func (g *CodeGenerator) Generate(rule aimanager.ActionRule) (string, error) {
	if g.systemPrompt == "" {
		return "", fmt.Errorf("code generation refused: system prompt not configured in AIManager (Settings → ActionGenerator → System Prompt)")
	}
	ruleJSON, err := json.MarshalIndent(rule, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal rule: %w", err)
	}

	userPrompt := fmt.Sprintf("Generate JavaScript rule code for the following action rule:\n\n%s", string(ruleJSON))

	logger.Infof("Sending rule %d (%s) to model %s for code generation...", rule.ID, rule.Title, g.model)

	code, err := g.chatClient.Chat(g.model, g.systemPrompt, userPrompt)
	if err != nil {
		return "", fmt.Errorf("AI code generation failed: %w", err)
	}

	// The model may have wrapped the code in a markdown fence and reached
	// for typographic quotes; both would fail to parse.
	code = aicode.Clean(code)

	// Add comment header
	header := fmt.Sprintf("// Action Rule ID: %d\n// Title: %s\n", rule.ID, rule.Title)
	code = header + code

	logger.Infof("AI generated %d characters of JavaScript code for rule %d", len(code), rule.ID)

	return code, nil
}

// ValidateRuleID checks that the generated code contains exactly the expected rule ID.
// Returns an error if rule IDs don't match.
func ValidateRuleID(generatedIDs []int64, expectedID int64) error {
	if len(generatedIDs) == 0 {
		return fmt.Errorf("generated code contains no rule IDs, expected rule %d", expectedID)
	}

	found := false
	var hallucinated []int64
	for _, id := range generatedIDs {
		if id == expectedID {
			found = true
		} else {
			hallucinated = append(hallucinated, id)
		}
	}

	var parts []string
	if !found {
		parts = append(parts, fmt.Sprintf("expected rule ID %d not found in generated code", expectedID))
	}
	if len(hallucinated) > 0 {
		parts = append(parts, fmt.Sprintf("hallucinated rule IDs not in input: %v", hallucinated))
	}

	if len(parts) == 0 {
		return nil
	}
	return fmt.Errorf("rule ID validation failed: %s", strings.Join(parts, "; "))
}
