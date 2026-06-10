// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package selfupdate

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// HealthMode selects what "healthy" means after a restart.
type HealthMode string

const (
	// HealthModeOutbound — outbound mode (HTTPS server). We require a
	// completed TLS handshake on the configured listening port. We do
	// NOT require valid mTLS material: the goal is to confirm the new
	// binary came up and bound the listener, not to verify it would
	// accept a real client. A real client failing to handshake is a
	// separate issue diagnosed elsewhere.
	HealthModeOutbound HealthMode = "outbound"
	// HealthModeInbound — inbound mode (reverse-connect). The proxy
	// exposes /health on 127.0.0.1:<port> for diagnostics; we use that
	// as the success signal. Failing to reach it within the window
	// triggers rollback.
	HealthModeInbound HealthMode = "inbound"
)

// HealthOptions are passed to Probe. Window is the total time budget; the
// probe retries every PollInterval until either success or window expiry.
type HealthOptions struct {
	Mode         HealthMode
	Host         string
	Port         string
	Window       time.Duration
	PollInterval time.Duration
}

// Probe blocks until the proxy is healthy or the window expires.
//
// Healthy is intentionally a low bar: the new binary needs to come up,
// open its listener, and respond. Anything more (e.g. successful MCP
// initialize from a real client) requires cooperating party material
// that the updater doesn't have. The bar is calibrated to catch the
// failure modes we care about — the new binary crashes, panics on
// startup, or fails to bind — without flapping on transient client
// issues.
func Probe(ctx context.Context, opts HealthOptions) error {
	if opts.Window <= 0 {
		opts.Window = 30 * time.Second
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 1 * time.Second
	}
	if opts.Port == "" {
		return errors.New("health probe: port is empty")
	}

	deadline := time.Now().Add(opts.Window)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	host := opts.Host
	switch opts.Mode {
	case HealthModeOutbound:
		// The configured server.host may be 0.0.0.0; probe loopback.
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
	case HealthModeInbound:
		host = "127.0.0.1"
	default:
		return fmt.Errorf("health probe: unknown mode %q", opts.Mode)
	}

	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return fmt.Errorf("health probe timed out (%s): last error: %w", opts.Window, lastErr)
			}
			return fmt.Errorf("health probe timed out (%s)", opts.Window)
		}

		switch opts.Mode {
		case HealthModeOutbound:
			lastErr = probeTLS(ctx, net.JoinHostPort(host, opts.Port))
		case HealthModeInbound:
			lastErr = probeHealthHTTP(ctx, "http://"+net.JoinHostPort(host, opts.Port)+"/health")
		}
		if lastErr == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("health probe timed out (%s): last error: %w", opts.Window, lastErr)
		case <-time.After(opts.PollInterval):
		}
	}
}

func probeTLS(ctx context.Context, addr string) error {
	d := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := tls.DialWithDialer(d, "tcp", addr, &tls.Config{
		// We are probing our own listener for liveness, not authenticating
		// it. Skip cert verification so a self-signed enrollment cert is
		// not a probe failure.
		InsecureSkipVerify: true, //nolint:gosec
	})
	if err != nil {
		return err
	}
	defer conn.Close()
	return nil
}

func probeHealthHTTP(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("health endpoint returned HTTP %d", resp.StatusCode)
	}
	return nil
}
