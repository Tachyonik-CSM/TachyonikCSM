// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package servicecall makes this module's own requests to another service on
// a user's behalf: with the service's internal key, and the user in
// X-User-ID, so the service applies that user's permissions.
//
// An automatic run needs two of them around the routine — the user's
// automation setting, and deleting the action it handled. A routine's own
// calls go through the platform API bridge instead (TachyonikLib's
// apibridge), which is built from the managers' documentation.
//
// A redirect is never followed, so the key never reaches another address, and
// an answer is read only up to MaxResponseBytes.
package servicecall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tachyonik/lib/logger"
)

// MaxResponseBytes bounds one answer, as TachyonikLib's restclient does.
const MaxResponseBytes = 32 << 20 // 32 MiB

// Timeout is how long one call may take.
const Timeout = 30 * time.Second

// Client makes the calls. The zero value is not usable; use New.
type Client struct {
	http *http.Client
}

// New creates a Client.
func New() *Client {
	return &Client{http: &http.Client{Timeout: Timeout, CheckRedirect: noRedirects}}
}

// noRedirects stops the client following a redirect. Go drops Authorization
// on a redirect to another host but forwards custom headers as they are, so
// following one would hand X-Internal-Service-Key to whatever address a
// Location names. No service redirects legitimately; a 3xx is the failure it
// is.
func noRedirects(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// Call sends method to base+path with key, for userID (0: no user). body,
// when not nil, is sent as JSON; the answer is decoded into out when out is
// not nil. A network failure, a status outside 2xx, an oversized answer or
// one that does not decode is an error.
func (c *Client) Call(method, base, path, key string, userID int64, body, out interface{}) error {
	url := strings.TrimRight(base, "/") + path

	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if key != "" {
		req.Header.Set("X-Internal-Service-Key", key)
	}
	if userID > 0 {
		req.Header.Set("X-User-ID", strconv.FormatInt(userID, 10))
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer resp.Body.Close()

	// One byte past the cap tells an over-limit body apart from one exactly
	// at it.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("%s %s: reading the response: %w", method, url, err)
	}
	if len(respBody) > MaxResponseBytes {
		return fmt.Errorf("%s %s: response larger than %d MiB", method, url, MaxResponseBytes>>20)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logger.Warnf("servicecall ← %s %s status=%d resp=%s", method, url, resp.StatusCode, truncate(string(respBody), 500))
		return fmt.Errorf("%s %s: status %d: %s", method, url, resp.StatusCode, truncate(string(respBody), 500))
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response from %s %s: %w", method, url, err)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
