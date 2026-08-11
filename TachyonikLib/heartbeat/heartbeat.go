// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package heartbeat sends periodic liveness signals from a daemon (one
// that doesn't run its own HTTP server) to SystemManager, so the WebUI's
// System Health view can show whether the daemon is alive. The schedule is
// driven by the daemon itself: every tick we POST `nextHeartbeatAt = now +
// intervalSeconds`, and SystemManager flips the daemon to "Disconnected"
// once that timestamp plus its configured grace period is in the past.
package heartbeat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"tachyonik/lib/internal/safehttp"
)

// Config carries the per-service knobs Start needs. ServiceName must be one
// of the names known to SystemManager (see SystemManager handlers'
// knownServices) — anything else is rejected with 400.
type Config struct {
	ServiceName        string
	IntervalSeconds    int
	SystemManagerURL   string
	InternalServiceKey string
	HTTPClient         *http.Client // optional; defaults to a 5s-timeout client
}

// LogFn matches the signature of each service's logger.Errorf so callers
// can pass it directly without an interface adapter.
type LogFn func(format string, v ...interface{})

// Start launches a goroutine that POSTs heartbeats to SystemManager at the
// configured interval. It returns immediately. The goroutine exits when ctx
// is cancelled. errorf is called for transport / HTTP errors only; success
// is silent.
func Start(ctx context.Context, cfg Config, errorf LogFn) {
	if cfg.IntervalSeconds <= 0 {
		cfg.IntervalSeconds = 10
	}
	client := cfg.HTTPClient
	if client == nil {
		client = safehttp.NewClient(5 * time.Second)
	}
	// errorf is the only log channel this package has (see LogFn) — the
	// alternative would be a logger dependency the package deliberately avoids,
	// so a misconfiguration warning goes out over it too.
	if safehttp.CredentialExposed(cfg.SystemManagerURL, cfg.InternalServiceKey != "") {
		errorf("heartbeat: WARNING: internal service key configured over a non-TLS SystemManager URL (%s) — the key will be sent in cleartext", cfg.SystemManagerURL)
	}
	interval := time.Duration(cfg.IntervalSeconds) * time.Second

	go func() {
		// First heartbeat fires immediately at startup so a freshly
		// restarted service doesn't show as Disconnected for a full
		// interval.
		send(client, cfg, errorf)

		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				send(client, cfg, errorf)
			}
		}
	}()
}

type payload struct {
	Service         string `json:"service"`
	IntervalSeconds int    `json:"intervalSeconds"`
	NextHeartbeatAt string `json:"nextHeartbeatAt"`
}

func send(client *http.Client, cfg Config, errorf LogFn) {
	body, err := json.Marshal(payload{
		Service:         cfg.ServiceName,
		IntervalSeconds: cfg.IntervalSeconds,
		NextHeartbeatAt: time.Now().Add(time.Duration(cfg.IntervalSeconds) * time.Second).UTC().Format(time.RFC3339),
	})
	if err != nil {
		errorf("heartbeat: marshal failed: %v", err)
		return
	}
	url := fmt.Sprintf("%s/api/internal/service-heartbeats", cfg.SystemManagerURL)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		errorf("heartbeat: build request failed: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Service-Key", cfg.InternalServiceKey)
	resp, err := client.Do(req)
	if err != nil {
		errorf("heartbeat: POST failed: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		errorf("heartbeat: status %d: %s", resp.StatusCode, safehttp.ErrorBody(resp.Body))
	}
}
