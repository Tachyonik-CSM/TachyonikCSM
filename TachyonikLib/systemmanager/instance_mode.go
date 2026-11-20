// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The instance mode of the setup, as SystemManager rules it
// (docs/plans/instance-mode-from-systemmanager.md). SystemManager's
// instance.mode is the one setting; a service that depends on the mode asks
// for it at startup and serves nothing until it knows, so no service can run
// in another mode than the rest.

package systemmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"tachyonik/lib/internal/safehttp"
	"tachyonik/lib/logger"
)

// The two instance modes.
const (
	// ModeAppliance is one shared workspace: every user sees the same data.
	ModeAppliance = "appliance"
	// ModeSaaS keeps each user's data to themself (and their group).
	ModeSaaS = "saas"
)

// ErrBadMode is an answer that names neither mode. Asking again does not
// help: the caller stops.
var ErrBadMode = errors.New("SystemManager answered an instance mode that is neither appliance nor saas")

// workspaceAnswer is what InstanceMode reads of GET /api/internal/workspace.
type workspaceAnswer struct {
	InstanceMode string `json:"instanceMode"`
}

// InstanceMode asks SystemManager for the setup's mode: "appliance" or "saas".
func (c *Client) InstanceMode() (string, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/internal/workspace", nil)
	if err != nil {
		return "", fmt.Errorf("failed to build the workspace request: %w", err)
	}
	if c.internalServiceKey != "" {
		req.Header.Set("X-Internal-Service-Key", c.internalServiceKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to ask SystemManager for the instance mode: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("workspace GET status %d: %s", resp.StatusCode, safehttp.ErrorBody(resp.Body))
	}
	var a workspaceAnswer
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		return "", fmt.Errorf("failed to read SystemManager's workspace answer: %w", err)
	}
	mode := strings.ToLower(strings.TrimSpace(a.InstanceMode))
	if mode != ModeAppliance && mode != ModeSaaS {
		return "", fmt.Errorf("%w: %q", ErrBadMode, a.InstanceMode)
	}
	return mode, nil
}

// WaitForInstanceMode asks every `every` until SystemManager answers, logging
// every `logEvery` that it is still waiting. It returns the mode, an error
// wrapping ErrBadMode when the answer names no mode, or ctx's error.
func (c *Client) WaitForInstanceMode(ctx context.Context, every, logEvery time.Duration) (string, error) {
	start := time.Now()
	lastLog := start
	for {
		mode, err := c.InstanceMode()
		if err == nil || errors.Is(err, ErrBadMode) {
			return mode, err
		}
		if time.Since(lastLog) >= logEvery {
			logger.Warnf("Waiting for SystemManager to tell the instance mode (%s so far): %v", time.Since(start).Round(time.Second), err)
			lastLog = time.Now()
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(every):
		}
	}
}

// StartupInstanceMode is how a service learns its mode at startup: it waits
// for SystemManager at baseURL (asking every 2 seconds, logging every 30) and
// logs the mode. configured is the service's own, deprecated instance.mode
// setting and configuredFrom where it came from ("" when unset): it is not
// used, and a warning says so, naming a disagreement with SystemManager.
func StartupInstanceMode(ctx context.Context, baseURL, internalServiceKey, configured, configuredFrom string) (string, error) {
	if strings.TrimSpace(baseURL) == "" {
		return "", errors.New("no SystemManager URL is set, so the instance mode cannot be learned")
	}
	mode, err := NewClient(baseURL, internalServiceKey).WaitForInstanceMode(ctx, 2*time.Second, 30*time.Second)
	if err != nil {
		return "", err
	}
	logger.Infof("Instance mode: %s (from SystemManager)", mode)
	if c := strings.ToLower(strings.TrimSpace(configured)); c != "" {
		if c == mode {
			logger.Warnf("%s is set to %q but no longer used: SystemManager's instance.mode rules the setup. Remove it; the next release refuses it.", configuredFrom, configured)
		} else {
			logger.Warnf("%s says %q, but SystemManager says %q, which is used: SystemManager's instance.mode rules the setup. Remove the setting; the next release refuses it.", configuredFrom, configured, mode)
		}
	}
	return mode, nil
}
