// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package apiregister registers a manager's API document with AIManager, which
// keeps the platform API catalogue (docs/specs/01-platform-api-catalogue.md,
// §4). A manager calls Start once at startup with the document embedded in its
// binary, so the catalogue describes the binaries that actually run
// (PA-REG-01).
//
// Start does not wait: the registration runs in the background and is retried
// until AIManager takes it, so a manager that starts before AIManager serves
// anyway. A refusal — the document cannot be read, or it clashes with another
// module's — is not retried: sending the same document again changes nothing,
// and the error says what to fix.
package apiregister

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tachyonik/lib/logger"
)

// Config says what to register and where.
type Config struct {
	// Module is the registering module's name in lower case, e.g.
	// "assetmanager".
	Module string
	// Version is the module's version, as `<module> version` reports it.
	Version string
	// Document is the API document, as embedded in the binary.
	Document []byte
	// AIManagerURL and ServiceKey reach AIManager's internal API.
	AIManagerURL string
	ServiceKey   string
	// HTTPClient replaces the default client (tests).
	HTTPClient *http.Client
	// FirstRetry and MaxRetry bound the wait between attempts; defaults 2s
	// and 60s.
	FirstRetry, MaxRetry time.Duration
}

// Request is the body of PUT /api/internal/api-catalogue/{module}.
type Request struct {
	Version  string `json:"version"`
	SHA256   string `json:"sha256"`
	Document string `json:"document"`
}

// Digest is the SHA-256 of a document, as registered.
func Digest(document []byte) string {
	sum := sha256.Sum256(document)
	return hex.EncodeToString(sum[:])
}

// Start registers in the background until it succeeds, AIManager refuses the
// document, or ctx ends. The returned channel is closed when it is done, with
// the outcome sent before: nil on success.
func Start(ctx context.Context, cfg Config) <-chan error {
	done := make(chan error, 1)
	if cfg.FirstRetry <= 0 {
		cfg.FirstRetry = 2 * time.Second
	}
	if cfg.MaxRetry <= 0 {
		cfg.MaxRetry = 60 * time.Second
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout:       15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	go func() {
		defer close(done)
		wait := cfg.FirstRetry
		for attempt := 1; ; attempt++ {
			permanent, err := register(ctx, client, cfg)
			if err == nil {
				logger.Infof("API document registered with AIManager (%s %s, sha256 %s)", cfg.Module, cfg.Version, Digest(cfg.Document)[:12])
				done <- nil
				return
			}
			if permanent {
				logger.Errorf("AIManager refused the API document of %s: %v — the platform API catalogue does not describe this module until the document is fixed", cfg.Module, err)
				done <- err
				return
			}
			if attempt == 1 || attempt%10 == 0 {
				logger.Warnf("Could not register the API document with AIManager yet (attempt %d): %v; retrying", attempt, err)
			}
			select {
			case <-ctx.Done():
				done <- ctx.Err()
				return
			case <-time.After(wait):
			}
			if wait *= 2; wait > cfg.MaxRetry {
				wait = cfg.MaxRetry
			}
		}
	}()
	return done
}

// register makes one attempt. permanent is true for a refusal (4xx).
func register(ctx context.Context, client *http.Client, cfg Config) (permanent bool, err error) {
	body, _ := json.Marshal(Request{Version: cfg.Version, SHA256: Digest(cfg.Document), Document: string(cfg.Document)})
	u := strings.TrimRight(cfg.AIManagerURL, "/") + "/api/internal/api-catalogue/" + url.PathEscape(cfg.Module)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return true, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Service-Key", cfg.ServiceKey)
	resp, err := client.Do(req)
	if err != nil {
		// Name the address and the reason: a configuration file from an older
		// install that still points at localhost (inside a container, the
		// container itself) is the usual cause, and the address shows it.
		cause := err
		var ue *url.Error
		if errors.As(err, &ue) {
			cause = ue.Err
		}
		return false, fmt.Errorf("AIManager at %s cannot be reached: %v", strings.TrimRight(cfg.AIManagerURL, "/"), cause)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return false, nil
	}
	var e struct {
		Error string `json:"error"`
	}
	msg := fmt.Sprintf("status %d", resp.StatusCode)
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		msg += ": " + e.Error
	}
	// 401/403 is a wrong key, which an operator fixes in the configuration;
	// a 404 an AIManager too old to keep the catalogue. Neither heals by
	// retrying, but both may be fixed by a restart of the other side, so
	// only a refusal of the document itself is final.
	permanent = resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusConflict
	return permanent, fmt.Errorf("%s", msg)
}
