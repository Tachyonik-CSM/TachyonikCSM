// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package systemmanager is a thin outbound client for reporting audit-trail
// events to SystemManager. It is the shared core used by every service; modules
// that need additional SystemManager endpoints embed this Client and add their
// own methods locally (so the shared library stays minimal).
package systemmanager

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"tachyonik/lib/internal/safehttp"
	"tachyonik/lib/logger"
)

type Client struct {
	baseURL            string
	httpClient         *http.Client
	internalServiceKey string
}

func NewClient(baseURL, internalServiceKey string) *Client {
	if safehttp.CredentialExposed(baseURL, internalServiceKey != "") {
		logger.Warnf("SystemManager client configured with an internal service key over a non-TLS URL (%s) — the key will be sent in cleartext", baseURL)
	}
	return &Client{
		baseURL:            baseURL,
		internalServiceKey: internalServiceKey,
		httpClient:         safehttp.NewClient(10 * time.Second),
	}
}

// CreateAuditEvent reports an audit-trail event to SystemManager. Best-effort:
// returns an error so the caller can log it, but emitters should not fail
// their primary operation just because the audit POST failed.
func (c *Client) CreateAuditEvent(userID int64, level, module, message string) error {
	body, err := json.Marshal(map[string]interface{}{
		"userId":  userID,
		"level":   level,
		"module":  module,
		"message": message,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal audit event: %w", err)
	}
	url := fmt.Sprintf("%s/api/internal/audit-events", c.baseURL)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to build audit event request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.internalServiceKey != "" {
		req.Header.Set("X-Internal-Service-Key", c.internalServiceKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send audit event: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("audit event POST status %d: %s", resp.StatusCode, safehttp.ErrorBody(resp.Body))
	}
	return nil
}
