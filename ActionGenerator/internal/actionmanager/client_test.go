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

// GetRuleActions sorts an owner's actions of one rule out of the list call:
// the open ones newest first, and whether an uncleared Done one holds the rule
// back. The title plays no part, and other rules' and owners' actions are left
// out.
func TestGetRuleActions(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"actions":[
		{"id":1,"userId":52,"title":"3 hosts","status":"New","actionRuleId":7},
		{"id":4,"userId":52,"title":"4 hosts","status":"InProgress","actionRuleId":7},
		{"id":2,"userId":52,"title":"done","status":"Done","actionRuleId":7,"clearedAt":"2026-11-18T10:00:00Z"},
		{"id":3,"userId":52,"title":"3 hosts","status":"New","actionRuleId":8},
		{"id":5,"userId":52,"title":"3 hosts","status":"New"}]}`)

	ra, err := c.GetRuleActions(52, 7)
	if err != nil {
		t.Fatalf("GetRuleActions: %v", err)
	}
	if got.path != "/api/actions" || got.query != "userId=52" {
		t.Errorf("request = %s?%s, want the list call", got.path, got.query)
	}
	if len(ra.Open) != 2 || ra.Open[0].ID != 4 || ra.Open[1].ID != 1 {
		t.Errorf("open = %v, want 4 then 1", ra.Open)
	}
	if ra.DoneUncleared {
		t.Error("a cleared Done action was taken as holding the rule back")
	}

	c, _ = serve(t, http.StatusOK, `{"actions":[{"id":2,"userId":52,"status":"Done","actionRuleId":7}]}`)
	if ra, _ := c.GetRuleActions(52, 7); !ra.DoneUncleared || len(ra.Open) != 0 {
		t.Errorf("rule actions = %+v, want an uncleared Done and nothing open", ra)
	}
}

func TestRefreshAction(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"changed":true}`)
	hidden := []int64{3}
	changed, err := c.RefreshAction(9, CreateActionRequest{Title: "5 hosts", Type: "Fix", Description: "d", Priority: 60, HiddenOptions: &hidden})
	if err != nil || !changed {
		t.Fatalf("RefreshAction: changed=%v err=%v", changed, err)
	}
	if got.method != "POST" || got.path != "/api/internal/actions/9/refresh" {
		t.Errorf("request = %s %s", got.method, got.path)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(got.body), &payload); err != nil {
		t.Fatalf("body was not JSON: %s", got.body)
	}
	if payload["title"] != "5 hosts" || payload["priority"] != float64(60) || payload["hiddenOptions"] == nil {
		t.Errorf("payload = %v", payload)
	}
	if _, ok := payload["status"]; ok {
		t.Error("the refresh sent a status: the status is the user's")
	}
}

func TestClearDoneActionsByRule(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"cleared":2,"clearedIds":[1,2]}`)
	n, err := c.ClearDoneActionsByRule(7, 0)
	if err != nil || n != 2 {
		t.Fatalf("ClearDoneActionsByRule: n=%d err=%v", n, err)
	}
	if got.path != "/api/internal/actions/clear-done-by-rule" {
		t.Errorf("path = %q", got.path)
	}
	var payload map[string]int64
	_ = json.Unmarshal([]byte(got.body), &payload)
	if payload["actionRuleId"] != 7 || payload["userId"] != 0 {
		t.Errorf("payload = %v", payload)
	}
}

// The two delete calls differ in endpoint and payload; both read the count out
// of the same envelope field.
func TestDeleteNewActionsByRule(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"deleted":3,"deletedIds":[1,2,3]}`)

	n, err := c.DeleteNewActionsByRule(52, 7, 0)
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
	if _, ok := payload["exceptActionId"]; ok {
		t.Error("exceptActionId sent although none was asked for")
	}

	if _, err := c.DeleteNewActionsByRule(52, 7, 9); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(got.body), &payload)
	if payload["exceptActionId"] != 9 {
		t.Errorf("payload = %v, want exceptActionId 9", payload)
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
	if _, err := c.DeleteNewActionsByRule(52, 7, 0); err == nil {
		t.Error("a 500 was accepted by DeleteNewActionsByRule")
	}
}

// ActionManager sends an action's last change as modifiedAt; the client read
// updatedAt, which it never sends, and so always had "".
func TestTheLastChangeIsRead(t *testing.T) {
	c, _ := serve(t, http.StatusOK, `{"actions":[{"id":1,"title":"Existing","createdAt":"2025-07-01T00:00:00Z","modifiedAt":"2025-07-02T00:00:00Z"}],"total":1}`)
	actions, err := c.GetActionsForUser(52)
	if err != nil || len(actions) != 1 || actions[0].ModifiedAt != "2025-07-02T00:00:00Z" {
		t.Errorf("actions %+v, %v", actions, err)
	}
}
