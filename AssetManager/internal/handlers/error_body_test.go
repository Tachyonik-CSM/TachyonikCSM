// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins that every refusal is answered as {"error": "<message>"} (AM-API-02):
// the authentication's 401 and 403, the WebSocket handshake's, the body
// limit's 413, and the 403 of a reached vulnerability or detection limit —
// once plain text (03-api.md, former D1). A guard keeps http.Error, which
// answers in plain text, out of the handlers.

package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"tachyonik/assetmanager/internal/systemmanager"
)

// checkErrorBody fails unless resp is JSON carrying {"error": "..."}.
func checkErrorBody(t *testing.T, what string, resp *http.Response, wantStatus int) {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Errorf("%s: status %d, want %d", what, resp.StatusCode, wantStatus)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("%s: Content-Type %q, want application/json", what, ct)
	}
	raw, _ := io.ReadAll(resp.Body)
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Error == "" {
		t.Errorf("%s: body %q is not {\"error\": \"...\"}", what, raw)
	}
}

func TestAuthenticationRefusalsAreJSON(t *testing.T) {
	srv, _, _ := authedServer(t)
	for name, c := range map[string]struct {
		path   string
		header map[string]string
		want   int
	}{
		"no credentials":            {"/api/assets", nil, http.StatusUnauthorized},
		"a malformed header":        {"/api/assets", map[string]string{"Authorization": "Token x"}, http.StatusUnauthorized},
		"an invalid token":          {"/api/assets", bearer("not-a-token"), http.StatusUnauthorized},
		"a wrong service key":       {"/api/assets", map[string]string{"X-Internal-Service-Key": "wrong"}, http.StatusUnauthorized},
		"an internal route, no key": {"/api/internal/users/ids", nil, http.StatusUnauthorized},
		"a deactivated account":     {"/api/assets", bearer(token(t, jwt.SigningMethodHS256, session(8))), http.StatusForbidden},
	} {
		req, _ := http.NewRequest("GET", srv.URL+c.path, nil)
		for k, v := range c.header {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		checkErrorBody(t, name, resp, c.want)
	}
}

func TestWebSocketRefusalsAreJSON(t *testing.T) {
	srv, _, _ := authedServer(t)
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	for name, c := range map[string]struct {
		protocols []string
		want      int
	}{
		"no token":            {nil, http.StatusUnauthorized},
		"an invalid token":    {[]string{"not-a-token"}, http.StatusUnauthorized},
		"deactivated account": {[]string{token(t, jwt.SigningMethodHS256, session(8))}, http.StatusForbidden},
	} {
		dialer := websocket.Dialer{Subprotocols: c.protocols}
		conn, resp, _ := dialer.Dial(url, nil)
		if conn != nil {
			conn.Close()
		}
		if resp == nil {
			t.Errorf("%s: no answer", name)
			continue
		}
		checkErrorBody(t, name, resp, c.want)
	}
}

func TestTheBodyLimitRefusalIsJSON(t *testing.T) {
	srv, _, _ := authedServer(t)
	req, _ := http.NewRequest("POST", srv.URL+"/api/assets", strings.NewReader(strings.Repeat(" ", MaxRequestBytes+1)))
	req.Header.Set("X-Internal-Service-Key", "svc-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	checkErrorBody(t, "oversized body", resp, http.StatusRequestEntityTooLarge)
}

// limitedSystemManager answers every user with a limit of one vulnerability
// and one detection.
func limitedSystemManager(t *testing.T) *systemmanager.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/internal/users/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/internal/users/")
		w.Write([]byte(`{"id":` + id + `,"active":true,"maxVulnerabilities":1,"maxDetections":1}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusCreated) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return systemmanager.NewClient(srv.URL, "")
}

func TestReachedLimitsAreJSON(t *testing.T) {
	h := scopedHandler(t)
	h.systemManagerClient = limitedSystemManager(t)
	for _, c := range []struct {
		what    string
		path    string
		handler http.HandlerFunc
		body    func(n int) string
	}{
		{"vulnerability limit", "/api/vulnerabilities", h.CreateVulnerability, func(n int) string {
			return `{"name":"Weak cipher ` + string(rune('a'+n)) + `","host":"10.0.0.7","port":"443/tcp","severity":40,"sourceId":5,"sourceLabel":"scan.xml","userId":7,"lastSeen":"2026-11-01T00:00:00Z"}`
		}},
		{"detection limit", "/api/detections", h.CreateDetection, func(n int) string {
			return `{"name":"HTTPS ` + string(rune('a'+n)) + `","host":"10.0.0.7","port":"443/tcp","sourceId":5,"sourceLabel":"scan.xml","userId":7,"lastSeen":"2026-11-01T00:00:00Z"}`
		}},
	} {
		first := httptest.NewRecorder()
		c.handler(first, callAs("POST", c.path, c.body(0), 7, true))
		if first.Code != http.StatusCreated {
			t.Fatalf("%s: the first record answered %d: %s", c.what, first.Code, first.Body)
		}
		second := httptest.NewRecorder()
		c.handler(second, callAs("POST", c.path, c.body(1), 7, true))
		checkErrorBody(t, c.what, second.Result(), http.StatusForbidden)
		if !strings.Contains(second.Body.String(), "limit reached") {
			t.Errorf("%s: body %q lost its message", c.what, second.Body)
		}
	}
}

// http.Error answers in plain text; the handlers answer with respondError.
func TestNoHandlerAnswersInPlainText(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, "http.Error(") {
				t.Errorf("%s:%d answers in plain text (AM-API-02); use respondError", f, i+1)
			}
		}
	}
}
