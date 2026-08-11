// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package restclient carries the request mechanics this module's two outbound
// API clients share: join a path onto the base URL, attach the internal service
// key, send, check the status, and decode JSON. AIManager and ResourceManager
// sit behind the same X-Internal-Service-Key scheme, so collecting the plumbing
// here leaves the aimanager and api packages holding only their DTOs and
// one-line endpoint methods.
package restclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// StatusError reports a response that did not carry the expected status. It is
// a typed error so a caller can react to one specific code — the AI lookups
// turn 404 into their own "not found" wording — without matching on message
// text.
type StatusError struct {
	Method     string
	URL        string
	StatusCode int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("unexpected status code: %d", e.StatusCode)
}

// MaxResponseBytes bounds a decoded JSON response. Rule lists and routine
// bodies are far smaller; this exists so a malfunctioning or hostile upstream
// cannot grow the daemon's heap without limit through a reply.
const MaxResponseBytes = 32 << 20 // 32 MiB

// Client issues authenticated JSON requests against one service.
type Client struct {
	baseURL    string
	serviceKey string
	httpClient *http.Client
}

// New creates a client for baseURL. serviceKey may be empty, in which case no
// authentication header is sent.
func New(baseURL, serviceKey string, timeout time.Duration) *Client {
	return &Client{
		baseURL:    baseURL,
		serviceKey: serviceKey,
		httpClient: &http.Client{
			Timeout: timeout,
			// Never follow redirects. Go strips Authorization across hosts but
			// forwards custom headers verbatim, so a 302 would hand
			// X-Internal-Service-Key to whatever host it names. Neither
			// ResourceManager nor AIManager legitimately redirects.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Request issues method against path (joined onto the base URL) and returns the
// raw response, for callers that need the body or headers themselves — those
// callers own resp.Body. body, when non-nil, is sent JSON-encoded. A status
// other than wantStatus closes the body and returns a *StatusError, so a
// failed call never leaks a connection back to the caller.
func (c *Client) Request(method, path string, body any, wantStatus int) (*http.Response, error) {
	url := c.baseURL + path

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal payload: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, url, payload)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.serviceKey != "" {
		req.Header.Set("X-Internal-Service-Key", c.serviceKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	if resp.StatusCode != wantStatus {
		resp.Body.Close()
		return nil, &StatusError{Method: method, URL: url, StatusCode: resp.StatusCode}
	}
	return resp, nil
}

// JSON issues a request and decodes the response into out. Pass a nil out when
// the call has no response body worth reading — the body is drained and closed
// either way.
func (c *Client) JSON(method, path string, body, out any, wantStatus int) error {
	resp, err := c.Request(method, path, body, wantStatus)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if out == nil {
		return nil
	}

	// Read one byte past the cap so an over-limit body is reported as such
	// rather than handed to the decoder as truncated JSON, which would surface
	// as a confusing "unexpected EOF" blamed on the upstream's formatting.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}
	if len(raw) > MaxResponseBytes {
		return fmt.Errorf("response body exceeds the %d byte limit", MaxResponseBytes)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}
	return nil
}
