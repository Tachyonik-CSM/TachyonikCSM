// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package openai provides a chat client for OpenAI-compatible APIs
// (OpenAI, Mistral, Google's OpenAI-compat endpoint, manual self-hosted).
// The wire shape is the OpenAI Chat Completions API; baseURL must include
// the version segment (e.g. https://api.mistral.ai/v1) because providers
// disagree on where /chat/completions hangs in the path. Its Chat method
// carries the same signature as the other provider clients under
// tachyonik/lib/aiclient.
package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"tachyonik/lib/internal/safehttp"
	"tachyonik/lib/logger"
)

// maxRespBytes bounds the reply we buffer. These APIs place no ceiling on
// response length, so this is the backstop that stops a hostile or
// misconfigured endpoint from growing the heap without limit.
const maxRespBytes = 16 << 20 // 16 MiB

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
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
}

type response struct {
	Choices []struct {
		Message message `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// NewClient creates a new OpenAI-compatible API client with the default 120s timeout.
func NewClient(apiURL, apiKey string) *Client {
	return NewClientWithTimeout(apiURL, apiKey, 120*time.Second)
}

// NewClientWithTimeout creates a new OpenAI-compatible API client with an
// explicit HTTP client timeout.
func NewClientWithTimeout(apiURL, apiKey string, timeout time.Duration) *Client {
	if safehttp.CredentialExposed(apiURL, apiKey != "") {
		logger.Warnf("OpenAI-compatible client configured with an API key over a non-TLS URL (%s) — the key will be sent in cleartext", apiURL)
	}
	return &Client{
		apiURL:     apiURL,
		apiKey:     apiKey,
		httpClient: safehttp.NewClient(timeout),
	}
}

// Chat sends a chat request to the OpenAI-compatible API. systemPrompt is
// flattened into the first message with role=system, mirroring how
// OpenAI/Mistral/etc. accept system instructions.
func (c *Client) Chat(model string, systemPrompt string, userPrompt string) (string, error) {
	msgs := make([]message, 0, 2)
	if systemPrompt != "" {
		msgs = append(msgs, message{Role: "system", Content: systemPrompt})
	}
	msgs = append(msgs, message{Role: "user", Content: userPrompt})

	jsonData, err := json.Marshal(request{Model: model, Messages: msgs})
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", c.apiURL+"/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	logger.Debugf("Sending chat request to OpenAI-compatible API: model=%s", model)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OpenAI-compatible API returned status %d: %s", resp.StatusCode, safehttp.ErrorBody(resp.Body))
	}

	body, err := safehttp.ReadLimited(resp.Body, maxRespBytes)
	if err != nil {
		return "", err
	}

	var chatResp response
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if chatResp.Error != nil {
		return "", fmt.Errorf("API error: %s", chatResp.Error.Message)
	}
	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("API returned no choices")
	}

	content := chatResp.Choices[0].Message.Content
	logger.Debugf("Received chat response from OpenAI-compatible API: %d characters", len(content))
	return content, nil
}
