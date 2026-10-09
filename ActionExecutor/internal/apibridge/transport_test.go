// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the two transport guards of the bridge: a redirect is not followed,
// so the service key never reaches another address, and an oversized answer is
// refused rather than read into memory.

package apibridge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"tachyonik/actionexecutor/internal/config"
)

func TestARedirectIsNotFollowed(t *testing.T) {
	var reached atomic.Value
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(r.Header.Get("X-Internal-Service-Key"))
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(elsewhere.Close)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/steal", http.StatusFound)
	}))
	t.Cleanup(backend.Close)

	cfg := &config.Config{}
	err := New(cfg).do("GET", backend.URL, "/api/assets", "the-key", AuthContext{UserID: 52}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Errorf("err = %v, want the 302 reported as a failure", err)
	}
	if v := reached.Load(); v != nil {
		t.Fatalf("the redirect was followed, carrying key %q", v)
	}
}

func TestAnOversizedAnswerIsRefused(t *testing.T) {
	big := strings.Repeat("x", MaxResponseBytes+1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	t.Cleanup(backend.Close)

	var out map[string]interface{}
	err := New(&config.Config{}).do("GET", backend.URL, "/api/assets", "", AuthContext{UserID: 52}, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("err = %v, want the answer refused as too large", err)
	}
}
