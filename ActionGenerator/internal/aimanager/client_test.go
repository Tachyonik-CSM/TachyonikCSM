// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Characterisation tests for the AIManager client.
//
// Written against the hand-rolled implementation before it moved onto
// TachyonikLib's restclient, and left in place afterwards: they describe what
// each endpoint asks for and what it does with the answer, which is exactly
// what a rewrite of the request plumbing must not change. What they pin is the
// observable contract — method, path, the service-key header, the status each
// call treats as success, the envelope field the result is read out of, and
// which failures are distinguishable from which.
//
// They deliberately do NOT pin error message wording, except where a caller
// reacts to the distinction (the AI lookups' "not found").

package aimanager

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recorded is what the fake service saw, so a test can assert on the request
// rather than only on the reply.
type recorded struct {
	method string
	path   string
	query  string
	key    string
	body   string
}

// serve stands up a fake AIManager that answers every request with status and
// body, and records the last request it received.
func serve(t *testing.T, status int, body string) (*Client, *recorded) {
	t.Helper()
	var got recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, r.ContentLength)
		if r.ContentLength > 0 {
			r.Body.Read(raw)
		}
		got = recorded{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
			key:    r.Header.Get("X-Internal-Service-Key"),
			body:   string(raw),
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "test-key"), &got
}

func TestGetActionRules(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"actionRules":[{"id":7,"title":"Rule seven"}],"total":1}`)

	rules, err := c.GetActionRules()
	if err != nil {
		t.Fatalf("GetActionRules: %v", err)
	}
	if len(rules) != 1 || rules[0].ID != 7 || rules[0].Title != "Rule seven" {
		t.Errorf("rules = %+v", rules)
	}
	if got.method != "GET" || got.path != "/api/internal/action-rules" {
		t.Errorf("request = %s %s", got.method, got.path)
	}
	if got.key != "test-key" {
		t.Errorf("service key header = %q", got.key)
	}
}

// The list lives under an envelope key; reading the body as a bare array would
// silently yield nothing.
func TestGetActionRulesReadsTheEnvelope(t *testing.T) {
	c, _ := serve(t, http.StatusOK, `[{"id":7}]`)
	rules, err := c.GetActionRules()
	if err == nil && len(rules) != 0 {
		t.Errorf("a bare array decoded to %+v; the envelope key is the contract", rules)
	}
}

func TestGetActionRulesRejectsNonOK(t *testing.T) {
	c, _ := serve(t, http.StatusInternalServerError, `{}`)
	if _, err := c.GetActionRules(); err == nil {
		t.Error("a 500 was accepted")
	}
}

func TestGetModuleAISetting(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"moduleName":"actiongenerator","systemPrompt":"hello","ai":{"id":3,"name":"claude","model":"opus"}}`)

	setting, err := c.GetModuleAISetting("actiongenerator")
	if err != nil {
		t.Fatalf("GetModuleAISetting: %v", err)
	}
	if setting.SystemPrompt != "hello" || setting.AI == nil || setting.AI.Name != "claude" {
		t.Errorf("setting = %+v", setting)
	}
	if got.path != "/api/internal/module-ai-settings/actiongenerator" {
		t.Errorf("path = %q", got.path)
	}
}

// Creating a routine succeeds on 201, not 200 — a rewrite that normalised
// every call to "expect 200" would break it.
func TestCreateRoutineWantsCreated(t *testing.T) {
	c, got := serve(t, http.StatusCreated, `{"id":11,"status":"passed"}`)

	routine, err := c.CreateRoutine(CreateRoutineRequest{Rule: 7, Code: "var rules=[];", Version: "v1"})
	if err != nil {
		t.Fatalf("CreateRoutine: %v", err)
	}
	if routine.ID != 11 {
		t.Errorf("routine = %+v", routine)
	}
	if got.method != "POST" || got.path != "/api/internal/routines" {
		t.Errorf("request = %s %s", got.method, got.path)
	}
	if !strings.Contains(got.body, `"code"`) {
		t.Errorf("body did not carry the routine: %s", got.body)
	}
}

func TestCreateRoutineRejectsPlainOK(t *testing.T) {
	c, _ := serve(t, http.StatusOK, `{"id":11}`)
	if _, err := c.CreateRoutine(CreateRoutineRequest{Rule: 7}); err == nil {
		t.Error("a 200 was accepted where the API answers 201")
	}
}

func TestGetRoutine(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"id":11,"code":"var rules=[];","status":"passed"}`)

	routine, err := c.GetRoutine(11)
	if err != nil {
		t.Fatalf("GetRoutine: %v", err)
	}
	if routine.Code == "" || routine.Status != "passed" {
		t.Errorf("routine = %+v", routine)
	}
	if got.path != "/api/internal/routines/11" {
		t.Errorf("path = %q", got.path)
	}
}

// The generator reports back through this call, so both the flag and the
// reason have to reach the wire.
func TestClearGenerateRequest(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{}`)

	if err := c.ClearGenerateRequest(7, "no AI configured"); err != nil {
		t.Fatalf("ClearGenerateRequest: %v", err)
	}
	if got.method != "PATCH" || got.path != "/api/internal/action-rules/7" {
		t.Errorf("request = %s %s", got.method, got.path)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(got.body), &payload); err != nil {
		t.Fatalf("body was not JSON: %s", got.body)
	}
	if payload["clearGenerateRequest"] != true {
		t.Errorf("payload = %v, want the clear flag set", payload)
	}
	if payload["generateError"] != "no AI configured" {
		t.Errorf("payload = %v, want the reason carried", payload)
	}
}

// A 404 from the AI lookups is a distinct outcome the daemon reports as "AI
// not available" rather than as a transport failure.
func TestGetAIByIDNotFound(t *testing.T) {
	c, _ := serve(t, http.StatusNotFound, `{}`)
	_, err := c.GetAIByID(3)
	if err == nil {
		t.Fatal("a 404 was accepted")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want it to name the AI as not found", err)
	}
}

func TestGetAIByNameNotFound(t *testing.T) {
	c, got := serve(t, http.StatusNotFound, `{}`)
	_, err := c.GetAIByName("claude")
	if err == nil {
		t.Fatal("a 404 was accepted")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want it to name the AI as not found", err)
	}
	if got.path != "/api/internal/ais/by-name/claude" {
		t.Errorf("path = %q", got.path)
	}
}

func TestGetAIByID(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"id":3,"name":"claude","model":"opus","provider":"claude"}`)
	entry, err := c.GetAIByID(3)
	if err != nil {
		t.Fatalf("GetAIByID: %v", err)
	}
	if entry.Name != "claude" || entry.Model != "opus" {
		t.Errorf("entry = %+v", entry)
	}
	if got.path != "/api/internal/ais/3" {
		t.Errorf("path = %q", got.path)
	}
}

// Each of these reads its list out of a differently named envelope field.
func TestToolEndpointEnvelopes(t *testing.T) {
	t.Run("tool rules", func(t *testing.T) {
		c, got := serve(t, http.StatusOK, `{"tools":[{"id":1,"toolOverviewId":5}]}`)
		rules, err := c.GetEnabledToolRules()
		if err != nil {
			t.Fatalf("GetEnabledToolRules: %v", err)
		}
		if len(rules) != 1 || rules[0].ID != 1 {
			t.Errorf("rules = %+v", rules)
		}
		if got.path != "/api/internal/tool-rules" {
			t.Errorf("path = %q", got.path)
		}
	})

	t.Run("overviews", func(t *testing.T) {
		c, got := serve(t, http.StatusOK, `{"overviews":[{"id":5,"manualCapabilityIds":[9]}]}`)
		overviews, err := c.GetToolOverviews()
		if err != nil {
			t.Fatalf("GetToolOverviews: %v", err)
		}
		if len(overviews) != 1 || overviews[0].ID != 5 {
			t.Errorf("overviews = %+v", overviews)
		}
		if got.path != "/api/internal/tool-overviews" {
			t.Errorf("path = %q", got.path)
		}
	})

	t.Run("capabilities", func(t *testing.T) {
		c, got := serve(t, http.StatusOK, `{"toolCapabilities":[{"id":9,"name":"Vulnerability detection"}]}`)
		caps, err := c.GetToolCapabilities()
		if err != nil {
			t.Fatalf("GetToolCapabilities: %v", err)
		}
		if len(caps) != 1 || caps[0].Name != "Vulnerability detection" {
			t.Errorf("caps = %+v", caps)
		}
		if got.path != "/api/internal/tool-capabilities" {
			t.Errorf("path = %q", got.path)
		}
	})
}

func TestCountPersonalAIs(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"count":4}`)
	count, err := c.CountPersonalAIs(52)
	if err != nil {
		t.Fatalf("CountPersonalAIs: %v", err)
	}
	if count != 4 {
		t.Errorf("count = %d, want 4", count)
	}
	if got.path != "/api/internal/users/52/ais/count" {
		t.Errorf("path = %q", got.path)
	}
}

// The ids ride in the query string, comma-joined.
func TestGetCapabilityNamesForToolRules(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"capabilities":["Vulnerability detection","Asset discovery"]}`)
	names, err := c.GetCapabilityNamesForToolRules([]int64{3, 9})
	if err != nil {
		t.Fatalf("GetCapabilityNamesForToolRules: %v", err)
	}
	if len(names) != 2 {
		t.Errorf("names = %v", names)
	}
	if !strings.Contains(got.query, "3,9") && !strings.Contains(got.query, "3%2C9") {
		t.Errorf("query = %q, want the ids comma-joined", got.query)
	}
}

// No ids means no request at all — asking for the capabilities of nothing is
// not a call worth making.
func TestGetCapabilityNamesForNoRules(t *testing.T) {
	c, got := serve(t, http.StatusInternalServerError, `boom`)
	names, err := c.GetCapabilityNamesForToolRules(nil)
	if err != nil {
		t.Fatalf("an empty id list should not be an error: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("names = %v, want none", names)
	}
	if got.method != "" {
		t.Errorf("a request was sent (%s %s) for an empty id list", got.method, got.path)
	}
}

// A body that is not the expected JSON is an error, not a zero value.
func TestMalformedBodyIsAnError(t *testing.T) {
	c, _ := serve(t, http.StatusOK, `{"actionRules": not json}`)
	if _, err := c.GetActionRules(); err == nil {
		t.Error("a malformed body was accepted")
	}
}

// An empty service key sends no header — the daemon runs unauthenticated
// against a manager that does not require one.
func TestEmptyServiceKeySendsNoHeader(t *testing.T) {
	var key string
	var seen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("X-Internal-Service-Key")
		_, seen = r.Header["X-Internal-Service-Key"]
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"actionRules":[]}`))
	}))
	defer srv.Close()

	if _, err := NewClient(srv.URL, "").GetActionRules(); err != nil {
		t.Fatalf("GetActionRules: %v", err)
	}
	if seen || key != "" {
		t.Errorf("service-key header was sent as %q with no key configured", key)
	}
}
