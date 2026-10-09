// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package codegen turns an execution rule into JavaScript by prompting the
// configured AI provider with the module's system prompt.
//
// It also cleans up after the model: stripping the markdown fences that wrap
// generated code, replacing Unicode quotation marks with ASCII equivalents, and
// checking that the routine declares exactly the rule ID it was asked for — a
// model that invents or renumbers rules would otherwise have its output stored
// against the wrong rule.
package codegen

import (
	"encoding/json"
	"fmt"
	"strings"

	"tachyonik/actionexecutor/internal/aimanager"
	"tachyonik/lib/aicode"
	"tachyonik/lib/logger"
)

// ChatClient is the interface for AI chat providers (ollama, claude, etc.)
type ChatClient interface {
	Chat(model string, systemPrompt string, userPrompt string) (string, error)
}

// CodeGenerator generates JavaScript executor code from execution rules using AI
type CodeGenerator struct {
	chatClient   ChatClient
	model        string
	systemPrompt string
}

// New creates a new CodeGenerator. systemPrompt is the prompt
// configured in AIManager (Settings → ActionExecutor → System Prompt).
// Empty is a valid input — Generate refuses to call the AI in that case
// so the failure is precise rather than producing executor code under
// an unknown implicit prompt.
func New(chatClient ChatClient, model, systemPrompt string) *CodeGenerator {
	return &CodeGenerator{
		chatClient:   chatClient,
		model:        model,
		systemPrompt: systemPrompt,
	}
}

// Generate sends an execution rule to the AI and returns generated JavaScript code.
// The generated code contains `var executors = [{ ... }]` with exactly one executor object.
func (g *CodeGenerator) Generate(rule aimanager.ExecutionRule) (string, error) {
	if g.systemPrompt == "" {
		return "", fmt.Errorf("code generation refused: system prompt not configured in AIManager (Settings → ActionExecutor → System Prompt)")
	}
	ruleJSON, err := json.MarshalIndent(rule, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal rule: %w", err)
	}

	userPrompt := fmt.Sprintf("Generate JavaScript executor code for the following execution rule:\n\n%s", string(ruleJSON))

	logger.Infof("Sending execution rule %d (%s) to model %s for code generation...", rule.ID, rule.Title, g.model)

	code, err := g.chatClient.Chat(g.model, g.systemPrompt, userPrompt)
	if err != nil {
		return "", fmt.Errorf("AI code generation failed: %w", err)
	}

	// The model may have wrapped the code in a markdown fence and reached
	// for typographic quotes; both would fail to parse.
	code = aicode.Clean(code)

	header := fmt.Sprintf("// Execution Rule ID: %d\n// Title: %s\n", rule.ID, rule.Title)
	code = header + code

	logger.Infof("AI generated %d characters of JavaScript code for execution rule %d", len(code), rule.ID)

	return code, nil
}

// ValidateRuleID checks that the generated code contains exactly the expected rule ID.
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
