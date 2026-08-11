// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package claude provides a chat client for the Anthropic (Claude) Messages
// API. Its Chat method carries the same signature as the other provider
// clients under tachyonik/lib/aiclient, so a caller can dispatch on the
// configured provider behind a local interface of that shape and treat the
// result uniformly for code/text generation.
package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"tachyonik/lib/internal/safehttp"
	"tachyonik/lib/logger"
)

// maxRespBytes bounds the reply we buffer. Responses are capped at 8192 output
// tokens, so this is far above any real answer — it exists to stop a hostile or
// misconfigured endpoint from growing the heap without limit.
const maxRespBytes = 16 << 20 // 16 MiB

// Client represents a Claude (Anthropic) API client
type Client struct {
	apiURL     string
	apiKey     string
	httpClient *http.Client
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system"`
	Messages  []message `json:"messages"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type response struct {
	Content []contentBlock `json:"content"`
}

// NewClient creates a new Anthropic API client with the default 120s timeout.
func NewClient(apiURL, apiKey string) *Client {
	return NewClientWithTimeout(apiURL, apiKey, 120*time.Second)
}

// NewClientWithTimeout creates a new Anthropic API client with an explicit
// HTTP client timeout.
func NewClientWithTimeout(apiURL, apiKey string, timeout time.Duration) *Client {
	if apiURL == "" {
		apiURL = "https://api.anthropic.com"
	}
	if safehttp.CredentialExposed(apiURL, apiKey != "") {
		logger.Warnf("Anthropic client configured with an API key over a non-TLS URL (%s) — the key will be sent in cleartext", apiURL)
	}
	return &Client{
		apiURL:     apiURL,
		apiKey:     apiKey,
		httpClient: safehttp.NewClient(timeout),
	}
}

// Chat sends a chat request to the Anthropic API and returns the response content.
// The signature is shared with the other provider clients — see the package comment.
func (c *Client) Chat(model string, systemPrompt string, userPrompt string) (string, error) {
	reqBody := request{
		Model:     model,
		MaxTokens: 8192,
		System:    systemPrompt,
		Messages:  []message{{Role: "user", Content: userPrompt}},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", c.apiURL+"/v1/messages", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	logger.Debugf("Sending chat request to Anthropic API: model=%s", model)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Anthropic API returned status %d: %s", resp.StatusCode, safehttp.ErrorBody(resp.Body))
	}

	body, err := safehttp.ReadLimited(resp.Body, maxRespBytes)
	if err != nil {
		return "", err
	}

	var chatResp response
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	var content string
	for _, block := range chatResp.Content {
		if block.Type == "text" {
			content += block.Text
		}
	}

	logger.Debugf("Received chat response from Anthropic API: %d characters", len(content))

	return content, nil
}
