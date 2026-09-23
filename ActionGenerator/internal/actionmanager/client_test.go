// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Characterisation tests for the ActionManager client.
//
// Written against the hand-rolled implementation before it moved onto
// TachyonikLib's restclient, and kept afterwards: they describe method, path,
// the service-key header, the status each call treats as success, and the
// payload each POST puts on the wire — the contract a rewrite of the request
// plumbing must preserve. Error wording is deliberately not pinned.

package actionmanager

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// serve stands up a fake service answering every request with status and body,
// recording the last request it received.
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

func TestGetActionsForUser(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"actions":[{"id":1,"title":"Existing","status":"New"}],"total":1}`)

	actions, err := c.GetActionsForUser(52)
	if err != nil {
		t.Fatalf("GetActionsForUser: %v", err)
	}
	if len(actions) != 1 || actions[0].Title != "Existing" {
		t.Errorf("actions = %+v", actions)
	}
	if got.method != "GET" || got.path != "/api/actions" || got.query != "userId=52" {
		t.Errorf("request = %s %s?%s", got.method, got.path, got.query)
	}
	if got.key != "test-key" {
		t.Errorf("service key header = %q", got.key)
	}
}

// Creating an action succeeds on 201, not 200.
func TestCreateActionWantsCreated(t *testing.T) {
	c, got := serve(t, http.StatusCreated, `{"id":9,"title":"New action"}`)

	action, err := c.CreateAction(CreateActionRequest{Title: "New action", Type: "Info", Priority: 50})
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	if action.ID != 9 {
		t.Errorf("action = %+v", action)
	}
	if got.method != "POST" || got.path != "/api/actions" {
		t.Errorf("request = %s %s", got.method, got.path)
	}
}

func TestCreateActionRejectsPlainOK(t *testing.T) {
	c, _ := serve(t, http.StatusOK, `{"id":9}`)
	if _, err := c.CreateAction(CreateActionRequest{Title: "x"}); err == nil {
		t.Error("a 200 was accepted where the API answers 201")
	}
}

// ActionExists is a filter over the list call, not an endpoint of its own.
func TestActionExists(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"actions":[{"id":1,"title":"Existing"}]}`)

	exists, err := c.ActionExists(52, "Existing")
	if err != nil {
		t.Fatalf("ActionExists: %v", err)
	}
	if !exists {
		t.Error("an action that is present was reported missing")
	}
	if got.path != "/api/actions" {
		t.Errorf("path = %q — ActionExists should reuse the list endpoint", got.path)
	}

	missing, err := c.ActionExists(52, "Not there")
	if err != nil {
		t.Fatalf("ActionExists: %v", err)
	}
	if missing {
		t.Error("an absent action was reported present")
	}
}

// The two delete calls differ in endpoint and payload; both read the count out
// of the same envelope field.
func TestDeleteNewActionsByRule(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"deleted":3,"deletedIds":[1,2,3]}`)

	n, err := c.DeleteNewActionsByRule(52, 7)
	if err != nil {
		t.Fatalf("DeleteNewActionsByRule: %v", err)
	}
	if n != 3 {
		t.Errorf("deleted = %d, want 3", n)
	}
	if got.method != "POST" || got.path != "/api/internal/actions/delete-by-rule" {
		t.Errorf("request = %s %s", got.method, got.path)
	}

	var payload map[string]int64
	if err := json.Unmarshal([]byte(got.body), &payload); err != nil {
		t.Fatalf("body was not JSON: %s", got.body)
	}
	if payload["userId"] != 52 || payload["actionRuleId"] != 7 {
		t.Errorf("payload = %v", payload)
	}
}

func TestDeleteNewActionsByRuleExceptUser(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"deleted":2}`)

	n, err := c.DeleteNewActionsByRuleExceptUser(7, 52)
	if err != nil {
		t.Fatalf("DeleteNewActionsByRuleExceptUser: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted = %d, want 2", n)
	}
	if got.path != "/api/internal/actions/delete-by-rule-shared" {
		t.Errorf("path = %q", got.path)
	}

	var payload map[string]int64
	if err := json.Unmarshal([]byte(got.body), &payload); err != nil {
		t.Fatalf("body was not JSON: %s", got.body)
	}
	if payload["actionRuleId"] != 7 || payload["keepUserId"] != 52 {
		t.Errorf("payload = %v, want the rule and the user to keep", payload)
	}
}

func TestRejectsNonOK(t *testing.T) {
	c, _ := serve(t, http.StatusInternalServerError, `{}`)
	if _, err := c.GetActionsForUser(52); err == nil {
		t.Error("a 500 was accepted")
	}
	if _, err := c.DeleteNewActionsByRule(52, 7); err == nil {
		t.Error("a 500 was accepted by DeleteNewActionsByRule")
	}
}
