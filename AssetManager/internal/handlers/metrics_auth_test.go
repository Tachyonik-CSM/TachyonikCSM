// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests who may read /metrics (AM-OPS-17): the internal service key or any
// session, an anonymous one included, as for the API — and that /health and
// /ready stay open, as Docker's health checks and Compose's start order need.

package handlers

import (
	"net/http"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func TestMetricsNeedAuthenticationButHealthDoesNot(t *testing.T) {
	srv, _, _ := authedServer(t)
	anonymous := session(7)
	anonymous.Role = "anonymous"
	for name, c := range map[string]struct {
		header map[string]string
		want   int
	}{
		"nothing":               {nil, http.StatusUnauthorized},
		"a wrong service key":   {map[string]string{"X-Internal-Service-Key": "wrong"}, http.StatusUnauthorized},
		"the service key":       {map[string]string{"X-Internal-Service-Key": "svc-key"}, http.StatusOK},
		"a session":             {bearer(token(t, jwt.SigningMethodHS256, session(7))), http.StatusOK},
		"an anonymous session":  {bearer(token(t, jwt.SigningMethodHS256, anonymous)), http.StatusOK},
		"a deactivated account": {bearer(token(t, jwt.SigningMethodHS256, session(8))), http.StatusForbidden},
	} {
		if got := get(t, srv, "/metrics", c.header); got != c.want {
			t.Errorf("/metrics with %s: %d, want %d", name, got, c.want)
		}
	}
	if got := get(t, srv, "/health", nil); got != http.StatusOK {
		t.Errorf("/health without authentication: %d, want 200", got)
	}
	if got := get(t, srv, "/ready", nil); got == http.StatusUnauthorized || got == http.StatusForbidden {
		t.Errorf("/ready without authentication: %d, want it open", got)
	}
}
