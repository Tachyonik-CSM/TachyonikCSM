// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the AIManager client's endpoints: each call goes to its route with
// the service key and the right method, and a missing AI is reported in this
// client's own words rather than as a bare status code.

package aimanager

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type call struct{ method, uri, key, body string }

func serve(t *testing.T, status int, answer string) (*Client, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, call{r.Method, r.URL.RequestURI(), r.Header.Get("X-Internal-Service-Key"), string(b)})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "aim-key"), &calls
}

func TestTheEndpoints(t *testing.T) {
	cases := []struct {
		name, method, uri string
		status            int
		answer            string
		do                func(c *Client) error
	}{
		{"rules", "GET", "/api/internal/execution-rules", 200, `{"executionRules":[{"id":4}]}`,
			func(c *Client) error { _, err := c.GetExecutionRules(); return err }},
		{"report", "PATCH", "/api/internal/execution-rules/4/last-execution", 200, `{}`,
			func(c *Client) error { return c.ReportLastExecution(4, ReportLastExecutionRequest{Status: "success"}) }},
		{"module", "GET", "/api/internal/module-ai-settings/actionexecutor", 200, `{"systemPrompt":"p"}`,
			func(c *Client) error { _, err := c.GetModuleAISetting("actionexecutor"); return err }},
		{"create routine", "POST", "/api/internal/routines", 201, `{"id":9}`,
			func(c *Client) error { _, err := c.CreateRoutine(CreateRoutineRequest{Rule: 4}); return err }},
		{"routine", "GET", "/api/internal/routines/9", 200, `{"id":9}`,
			func(c *Client) error { _, err := c.GetRoutine(9); return err }},
		{"options", "GET", "/api/internal/action-options?actionRuleId=8", 200, `{"actionOptions":[]}`,
			func(c *Client) error { _, err := c.GetActionOptionsByRuleID(8); return err }},
		{"clear", "PATCH", "/api/internal/execution-rules/4", 200, `{}`,
			func(c *Client) error { return c.ClearGenerateRequest(4, "no AI") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, calls := serve(t, tc.status, tc.answer)
			if err := tc.do(c); err != nil {
				t.Fatalf("%v", err)
			}
			got := (*calls)[0]
			if got.method != tc.method || got.uri != tc.uri || got.key != "aim-key" {
				t.Errorf("sent %s %s (key %q), want %s %s with the service key", got.method, got.uri, got.key, tc.method, tc.uri)
			}
		})
	}
}

func TestTheGenerateErrorIsSent(t *testing.T) {
	c, calls := serve(t, 200, `{}`)
	_ = c.ClearGenerateRequest(4, "no AI configured")
	if b := (*calls)[0].body; !strings.Contains(b, `"clearGenerateRequest":true`) || !strings.Contains(b, `"generateError":"no AI configured"`) {
		t.Errorf("body %s", b)
	}
}

func TestAMissingAIIsNamed(t *testing.T) {
	c, _ := serve(t, http.StatusNotFound, `{}`)
	if _, err := c.GetAIByID(6); err == nil || !strings.Contains(err.Error(), "AI with ID 6 not found") {
		t.Errorf("err = %v", err)
	}
	if _, err := c.GetAIByName("claude"); err == nil || !strings.Contains(err.Error(), "AI 'claude' not found") {
		t.Errorf("err = %v", err)
	}
}

func TestAnotherStatusIsAnError(t *testing.T) {
	c, _ := serve(t, http.StatusInternalServerError, `{}`)
	if _, err := c.GetExecutionRules(); err == nil {
		t.Error("a 500 was not reported")
	}
}

// An AI name is free text: one holding "/" or "?" stays a single path segment
// and adds no query, instead of reaching another route.
func TestAnAINameStaysOnePathSegment(t *testing.T) {
	var path, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.EscapedPath(), r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":6}`))
	}))
	t.Cleanup(srv.Close)
	if _, err := NewClient(srv.URL, "k").GetAIByName("team/a?x=1"); err != nil {
		t.Fatalf("GetAIByName: %v", err)
	}
	if path != "/api/internal/ais/by-name/team%2Fa%3Fx=1" || query != "" {
		t.Errorf("requested path %q query %q", path, query)
	}
}
