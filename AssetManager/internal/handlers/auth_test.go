// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins who gets past authentication, on the API and on the WebSocket: only a
// session token (HS256, with an expiry) — not one of SystemManager's narrow
// MFA or first-password tokens, which carry a user id too — and only while its
// account is active, which is asked of SystemManager and remembered briefly;
// when SystemManager cannot be asked the request is refused. Also the internal
// service key, the request body limit and the CORS answers.

package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"tachyonik/assetmanager/internal/config"
	"tachyonik/assetmanager/internal/systemmanager"
)

const testSecret = "test-secret-test-secret-test-secret"

// fakeUsers answers GetUser from a map; err fails every lookup.
type fakeUsers struct {
	users   map[int64]*systemmanager.User
	err     error
	lookups int
}

func (f *fakeUsers) GetUser(id int64) (*systemmanager.User, error) {
	f.lookups++
	if f.err != nil {
		return nil, f.err
	}
	return f.users[id], nil
}

func boolPtr(b bool) *bool { return &b }

func authedServer(t *testing.T) (*httptest.Server, *fakeUsers, *accountChecker) {
	t.Helper()
	h := scopedHandler(t)
	h.config = &config.Config{}
	h.config.Auth.JWTSecret = testSecret
	h.config.Auth.InternalServiceKey = "svc-key"
	h.config.CORS.AllowedOrigins = []string{"https://csm.example"}
	users := &fakeUsers{users: map[int64]*systemmanager.User{
		7:  {ID: 7, Active: boolPtr(true)},
		8:  {ID: 8, Active: boolPtr(false)},
		10: {ID: 10}, // from a SystemManager without the field
	}}
	h.accounts = newAccountChecker(users)
	srv := httptest.NewServer(h.SetupRoutes())
	t.Cleanup(srv.Close)
	return srv, users, h.accounts
}

func token(t *testing.T, method jwt.SigningMethod, claims JWTClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(method, claims).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func session(userID int64) JWTClaims {
	return JWTClaims{UserID: userID, Role: "registered", RegisteredClaims: jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}}
}

func get(t *testing.T, srv *httptest.Server, path string, header map[string]string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

func TestOnlyASessionTokenAuthenticates(t *testing.T) {
	srv, _, _ := authedServer(t)
	mfa, enrol, firstPassword, noExpiry, expired := session(7), session(7), session(7), session(7), session(7)
	mfa.MFAPending = true
	enrol.MFASetupRequired = true
	firstPassword.PasswordSetupRequired = true
	noExpiry.ExpiresAt = nil
	expired.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))

	for name, c := range map[string]struct {
		tok  string
		want int
	}{
		"session":                   {token(t, jwt.SigningMethodHS256, session(7)), http.StatusOK},
		"MFA challenge":             {token(t, jwt.SigningMethodHS256, mfa), http.StatusUnauthorized},
		"MFA enrolment":             {token(t, jwt.SigningMethodHS256, enrol), http.StatusUnauthorized},
		"first password":            {token(t, jwt.SigningMethodHS256, firstPassword), http.StatusUnauthorized},
		"another HMAC algorithm":    {token(t, jwt.SigningMethodHS512, session(7)), http.StatusUnauthorized},
		"without an expiry":         {token(t, jwt.SigningMethodHS256, noExpiry), http.StatusUnauthorized},
		"expired":                   {token(t, jwt.SigningMethodHS256, expired), http.StatusUnauthorized},
		"deactivated account":       {token(t, jwt.SigningMethodHS256, session(8)), http.StatusForbidden},
		"deleted account":           {token(t, jwt.SigningMethodHS256, session(9)), http.StatusForbidden},
		"account without the field": {token(t, jwt.SigningMethodHS256, session(10)), http.StatusOK},
	} {
		if got := get(t, srv, "/api/assets", bearer(c.tok)); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
}

func TestAccountCheckFailsClosedAndIsRemembered(t *testing.T) {
	srv, users, accounts := authedServer(t)
	now := time.Now()
	accounts.now = func() time.Time { return now }
	tok := token(t, jwt.SigningMethodHS256, session(7))

	for i := 0; i < 3; i++ {
		if got := get(t, srv, "/api/assets", bearer(tok)); got != http.StatusOK {
			t.Fatalf("request %d: %d", i, got)
		}
	}
	if users.lookups != 1 {
		t.Errorf("%d lookups for three requests within the TTL, want 1", users.lookups)
	}

	// Deactivated meanwhile: still remembered as active until the TTL runs out.
	users.users[7].Active = boolPtr(false)
	if got := get(t, srv, "/api/assets", bearer(tok)); got != http.StatusOK {
		t.Errorf("within the TTL: %d, want 200", got)
	}
	now = now.Add(accountCheckTTL)
	if got := get(t, srv, "/api/assets", bearer(tok)); got != http.StatusForbidden {
		t.Errorf("after the TTL: %d, want 403", got)
	}

	// SystemManager unreachable: refused, and the failure is not remembered.
	users.err = errors.New("connection refused")
	now = now.Add(accountCheckTTL)
	if got := get(t, srv, "/api/assets", bearer(tok)); got != http.StatusServiceUnavailable {
		t.Errorf("SystemManager unreachable: %d, want 503", got)
	}
	users.err = nil
	users.users[7].Active = boolPtr(true)
	if got := get(t, srv, "/api/assets", bearer(tok)); got != http.StatusOK {
		t.Errorf("SystemManager back: %d, want 200", got)
	}
}

func TestWebSocketTakesOnlyAnActiveSession(t *testing.T) {
	srv, _, _ := authedServer(t)
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	mfa := session(7)
	mfa.MFAPending = true
	for name, c := range map[string]struct {
		tok  string
		want int
	}{
		"session":             {token(t, jwt.SigningMethodHS256, session(7)), http.StatusSwitchingProtocols},
		"MFA challenge":       {token(t, jwt.SigningMethodHS256, mfa), http.StatusUnauthorized},
		"deactivated account": {token(t, jwt.SigningMethodHS256, session(8)), http.StatusForbidden},
	} {
		dialer := websocket.Dialer{Subprotocols: []string{c.tok}}
		conn, resp, _ := dialer.Dial(url, nil)
		if conn != nil {
			conn.Close()
		}
		if resp == nil || resp.StatusCode != c.want {
			code := 0
			if resp != nil {
				code = resp.StatusCode
			}
			t.Errorf("%s: %d, want %d", name, code, c.want)
		}
	}
}

func TestInternalServiceKey(t *testing.T) {
	srv, _, _ := authedServer(t)
	if got := get(t, srv, "/api/assets?userId=7", map[string]string{"X-Internal-Service-Key": "svc-key"}); got != http.StatusOK {
		t.Errorf("right key: %d, want 200", got)
	}
	if got := get(t, srv, "/api/assets", map[string]string{"X-Internal-Service-Key": "svc-kez"}); got != http.StatusUnauthorized {
		t.Errorf("wrong key: %d, want 401", got)
	}
	if got := get(t, srv, "/api/internal/users/ids", map[string]string{"X-Internal-Service-Key": "svc-kez"}); got != http.StatusUnauthorized {
		t.Errorf("wrong key on an internal route: %d, want 401", got)
	}
	if got := get(t, srv, "/api/internal/users/ids", bearer(token(t, jwt.SigningMethodHS256, session(7)))); got != http.StatusUnauthorized {
		t.Errorf("a session on an internal route: %d, want 401", got)
	}
}

func TestRequestBodyLimit(t *testing.T) {
	srv, _, _ := authedServer(t)
	post := func(path string, size int) int {
		req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(strings.Repeat(" ", size)))
		req.Header.Set("X-Internal-Service-Key", "svc-key")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := post("/api/assets", MaxRequestBytes+1); got != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: %d, want 413", got)
	}
	if got := post("/api/assets", 10); got == http.StatusRequestEntityTooLarge {
		t.Error("small body refused as too large")
	}
	if got := post("/api/assets/scan-coverage", 2*MaxRequestBytes); got == http.StatusRequestEntityTooLarge {
		t.Error("a coverage table over the general limit was refused")
	}
}

func TestCORSAnswers(t *testing.T) {
	srv, _, _ := authedServer(t)
	req, _ := http.NewRequest("OPTIONS", srv.URL+"/api/assets/1", nil)
	req.Header.Set("Origin", "https://csm.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Vary"), "Origin") {
		t.Errorf("Vary: %q, want Origin", resp.Header.Get("Vary"))
	}
	if !strings.Contains(resp.Header.Get("Access-Control-Allow-Methods"), "PATCH") {
		t.Errorf("Allow-Methods: %q, want PATCH among them", resp.Header.Get("Access-Control-Allow-Methods"))
	}
}
