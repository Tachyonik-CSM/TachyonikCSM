// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ollama provides a chat client for the Ollama API. Its Chat method
// carries the same signature as the other provider clients under
// tachyonik/lib/aiclient, so it can be used interchangeably with the other AI
// providers for code/text generation. An optional API key is forwarded as a
// Bearer token to support Ollama installs fronted by an authenticating
// reverse proxy.
package ollama

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"tachyonik/lib/internal/safehttp"
	"tachyonik/lib/logger"
)

// maxRespBytes bounds the reply we buffer. Ollama places no ceiling on response
// length, so this is the backstop that stops a hostile or misconfigured
// endpoint from growing the heap without limit.
const maxRespBytes = 16 << 20 // 16 MiB

// Client represents an Ollama API client. apiKey is forwarded as a
// Bearer token on every request when non-empty; the bare Ollama daemon
// has no native auth, but operators commonly front it with an nginx
// that enforces a shared secret (see deployment/ollama/README.md).
// Empty apiKey leaves the Authorization header unset — keeps the
// loopback / unfronted Ollama install working unchanged.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// message represents a chat message
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatRequest represents a chat API request
type chatRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Stream   bool      `json:"stream"`
}

// chatResponse represents a chat API response
type chatResponse struct {
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at"`
	Message   message   `json:"message"`
	Done      bool      `json:"done"`
}

// NewClient creates a new Ollama API client with the default 120s timeout.
// apiKey may be empty — see the Client struct comment for when to populate it.
func NewClient(baseURL, apiKey string) *Client {
	return NewClientWithTimeout(baseURL, apiKey, 120*time.Second) // Longer timeout for AI responses
}

// NewClientWithTimeout creates a new Ollama API client with an explicit HTTP
// client timeout. apiKey may be empty — see the Client struct comment.
func NewClientWithTimeout(baseURL, apiKey string, timeout time.Duration) *Client {
	if safehttp.CredentialExposed(baseURL, apiKey != "") {
		logger.Warnf("Ollama client configured with an API key over a non-TLS URL (%s) — the key will be sent in cleartext", baseURL)
	}
	return &Client{
		baseURL:    baseURL,
		apiKey:     apiKey,
		httpClient: safehttp.NewClient(timeout),
	}
}

// setAuth applies Bearer auth to the request when an apiKey is
// configured. Called from every method that builds an outbound
// request, so a future caller can't accidentally skip it.
func (c *Client) setAuth(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

// Chat sends a chat request to Ollama and returns the response content.
// The signature is shared with the other provider clients — see the package
// comment. systemPrompt is sent as a leading message with role=system (omitted
// when empty); userPrompt follows with role=user.
func (c *Client) Chat(model string, systemPrompt string, userPrompt string) (string, error) {
	messages := make([]message, 0, 2)
	if systemPrompt != "" {
		messages = append(messages, message{Role: "system", Content: systemPrompt})
	}
	messages = append(messages, message{Role: "user", Content: userPrompt})

	reqBody := chatRequest{
		Model:    model,
		Messages: messages,
		Stream:   false,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", c.baseURL+"/api/chat", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)

	logger.Debugf("Sending chat request to Ollama: model=%s, messages=%d", model, len(messages))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama API returned status %d: %s", resp.StatusCode, safehttp.ErrorBody(resp.Body))
	}

	body, err := safehttp.ReadLimited(resp.Body, maxRespBytes)
	if err != nil {
		return "", err
	}

	var chatResp chatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	logger.Debugf("Received chat response from Ollama: %d characters", len(chatResp.Message.Content))

	return chatResp.Message.Content, nil
}
