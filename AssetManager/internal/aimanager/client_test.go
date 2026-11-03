// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the license lookup's caching and its failure behaviour.
//
// Caching matters because this is consulted on the asset-create path, which an
// import drives thousands of times. Failure behaviour matters more: on a fetch
// error the last good answer is served, and with no cached answer at all the
// error surfaces so the caller can fail open. A licensing check that cannot
// reach its authority must not silently become a denial.

package aimanager

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A configured client parses the license and serves it from cache within the
// TTL (one HTTP round-trip for repeated reads — important on the asset-create
// hot path / bulk imports).
func TestInstanceLicenseCaches(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":true,"numAssets":50}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	for i := 0; i < 5; i++ {
		lic, err := c.InstanceLicense()
		if err != nil {
			t.Fatalf("InstanceLicense: %v", err)
		}
		if !lic.Active || lic.NumAssets != 50 {
			t.Fatalf("license = %+v, want active/50", lic)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("server hit %d times, want 1 (cached)", got)
	}
}

// On a fetch error after a good read, the client fails open by serving the last
// known license — a transient AIManager blip must not flip enforcement.
func TestInstanceLicenseServesStaleOnError(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"active":true,"numAssets":50}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	c.ttl = 0 // force a refetch on every call

	if _, err := c.InstanceLicense(); err != nil {
		t.Fatalf("initial fetch: %v", err)
	}
	fail.Store(true)
	lic, err := c.InstanceLicense()
	if err != nil {
		t.Fatalf("expected stale success, got error: %v", err)
	}
	if !lic.Active || lic.NumAssets != 50 {
		t.Fatalf("stale license = %+v, want active/50", lic)
	}
}

// With no cache yet, a fetch error surfaces so the caller fails open (allows
// the create) rather than enforcing a cap it can't read.
func TestInstanceLicenseErrorWithoutCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	if lic, err := c.InstanceLicense(); err == nil || lic != nil {
		t.Fatalf("want (nil, error) with no cache, got (%+v, %v)", lic, err)
	}
}

// No AIManager URL → nil client → no instance-wide cap (the SaaS case).
func TestNewClientEmptyURL(t *testing.T) {
	if c := NewClient("", "key"); c != nil {
		t.Fatalf("NewClient(\"\") = %v, want nil", c)
	}
}
