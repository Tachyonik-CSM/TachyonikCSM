// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package aimanager is a thin internal client AssetManager uses to read this
// appliance's per-appliance license (the licensed host-asset cap) from the local
// AIManager, which owns the pairing token and fetches the license from the SaaS.
// The result is cached briefly so the host-asset create path does not make an
// HTTP call per asset. Enforcement is best-effort (honoring a purchase, not a
// security boundary): on any error the caller treats the cap as inactive
// (fail-open) so a transient AIManager blip never blocks asset creation.
package aimanager

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"tachyonik/lib/restclient"
)

// License is the effective per-appliance license. Active is true only when a
// positive, unexpired, entitled subscription applies.
type License struct {
	Active    bool `json:"active"`
	NumAssets int  `json:"numAssets"`
}

type Client struct {
	rest *restclient.Client

	// Short-TTL cache so a bulk import doesn't fetch the license per asset.
	mu       sync.Mutex
	cached   *License
	cachedAt time.Time
	ttl      time.Duration
}

// NewClient returns a client, or nil when baseURL is empty (no local AIManager
// configured → no instance-wide cap, e.g. on the SaaS).
func NewClient(baseURL, internalServiceKey string) *Client {
	if baseURL == "" {
		return nil
	}
	return &Client{
		rest: restclient.New(baseURL, internalServiceKey, 5*time.Second),
		ttl:  30 * time.Second,
	}
}

// InstanceLicense returns the appliance's effective license, served from a
// short-lived cache. On a fetch error it returns the last cached value if any,
// else a nil error with Active=false (fail-open) is signaled by the returned
// error being non-nil and license nil — callers must fail open on error.
func (c *Client) InstanceLicense() (*License, error) {
	c.mu.Lock()
	if c.cached != nil && time.Since(c.cachedAt) < c.ttl {
		lic := *c.cached
		c.mu.Unlock()
		return &lic, nil
	}
	c.mu.Unlock()

	lic, err := c.fetch()
	if err != nil {
		// Serve stale-but-known on error so a blip doesn't flip enforcement.
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.cached != nil {
			stale := *c.cached
			return &stale, nil
		}
		return nil, err
	}

	c.mu.Lock()
	c.cached = lic
	c.cachedAt = time.Now()
	c.mu.Unlock()
	return lic, nil
}

func (c *Client) fetch() (*License, error) {
	var lic License
	if err := c.rest.JSON("GET", "/api/internal/appliance-license", nil, &lic, http.StatusOK); err != nil {
		return nil, fmt.Errorf("AIManager license: %w", err)
	}
	return &lic, nil
}
