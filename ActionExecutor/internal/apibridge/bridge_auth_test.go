// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests how the tool lookups behind api.tools.list authenticate.
//
// A manual run once carried the user's login token, which the bridge passed on
// to AIManager's service-only routes; they refused it with 401 and every manual
// run that listed tools failed. Every run now uses the service key on the
// user's behalf, so there is one way to authenticate and it works everywhere.

package apibridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"tachyonik/actionexecutor/internal/config"
)

const testServiceKey = "aim-service-key"

// seen is what one request to the fake AIManager carried.
type seen struct {
	serviceKey string
	bearer     string
	userID     string
}

// fakeAIManager answers the three enrichment lookups and records how each was
// authenticated. Like AIManager, it refuses internal routes without the key.
func fakeAIManager(t *testing.T) (*httptest.Server, map[string]seen) {
	t.Helper()
	var mu sync.Mutex
	got := map[string]seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got[r.URL.Path] = seen{
			serviceKey: r.Header.Get("X-Internal-Service-Key"),
			bearer:     r.Header.Get("Authorization"),
			userID:     r.Header.Get("X-User-ID"),
		}
		mu.Unlock()

		var body interface{}
		switch r.URL.Path {
		case "/api/tool-overview":
			body = map[string]interface{}{"toolOverview": []interface{}{}}
		case "/api/internal/tool-capabilities":
			if r.Header.Get("X-Internal-Service-Key") != testServiceKey {
				http.Error(w, "Internal service authentication required", http.StatusUnauthorized)
				return
			}
			body = map[string]interface{}{"toolCapabilities": []interface{}{}}
		case "/api/internal/tool-rules":
			if r.Header.Get("X-Internal-Service-Key") != testServiceKey {
				http.Error(w, "Internal service authentication required", http.StatusUnauthorized)
				return
			}
			body = map[string]interface{}{"tools": []interface{}{}}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func newTestBridge(aimURL string) *Bridge {
	cfg := &config.Config{}
	cfg.AIManager.URL = aimURL
	cfg.AIManager.InternalServiceKey = testServiceKey
	return New(cfg)
}

// Every lookup behind api.tools.list — the user-facing tool overview and the
// two service-only routes — carries the service key and the run's user, and
// never a user token.
func TestTheToolLookupsCarryTheServiceKeyAndTheUser(t *testing.T) {
	srv, got := fakeAIManager(t)
	b := newTestBridge(srv.URL)
	auth := AuthContext{UserID: 52, UserRole: "user"}

	tools := []map[string]interface{}{{"id": float64(10), "name": "openvas"}}
	if _, err := b.enrichTools(tools, auth); err != nil {
		t.Fatalf("enrichTools: %v", err)
	}
	for _, path := range []string{"/api/tool-overview", "/api/internal/tool-capabilities", "/api/internal/tool-rules"} {
		s := got[path]
		if s.serviceKey != testServiceKey || s.userID != "52" || s.bearer != "" {
			t.Errorf("%s: key %q, user %q, bearer %q; want the service key and user 52, no token", path, s.serviceKey, s.userID, s.bearer)
		}
	}
}
