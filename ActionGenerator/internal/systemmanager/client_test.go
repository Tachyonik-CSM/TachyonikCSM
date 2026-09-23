// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Characterisation tests for the SystemManager client.
//
// Written against the hand-rolled implementation before it moved onto
// TachyonikLib's restclient, and kept afterwards: they describe method, path,
// the service-key header, the status each call treats as success, and the
// envelope the result is read out of — the contract a rewrite of the request
// plumbing must preserve. Error wording is deliberately not pinned.

package systemmanager

import (
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

// The user list comes back as a bare array, unlike most of the platform's
// list endpoints — worth pinning precisely because it is the odd one out.
func TestGetAllUsers(t *testing.T) {
	c, got := serve(t, http.StatusOK, `[{"id":1,"username":"a","role":"admin"},{"id":2,"username":"b","role":"registered"}]`)

	users, err := c.GetAllUsers()
	if err != nil {
		t.Fatalf("GetAllUsers: %v", err)
	}
	if len(users) != 2 || users[0].Username != "a" {
		t.Errorf("users = %+v", users)
	}
	if got.method != "GET" || got.path != "/api/internal/users" {
		t.Errorf("request = %s %s", got.method, got.path)
	}
	if got.key != "test-key" {
		t.Errorf("service key header = %q", got.key)
	}
}

func TestGetUser(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"id":52,"username":"jdoe","role":"registered","maxHostAssets":100}`)

	user, err := c.GetUser(52)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if user.Username != "jdoe" || user.MaxHostAssets != 100 {
		t.Errorf("user = %+v", user)
	}
	if got.path != "/api/internal/users/52" {
		t.Errorf("path = %q", got.path)
	}
}

func TestGetOrganisationForUser(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"name":"Acme","assetsHosts":12,"score":66,"naceCode":"62.01"}`)

	org, err := c.GetOrganisationForUser(52)
	if err != nil {
		t.Fatalf("GetOrganisationForUser: %v", err)
	}
	if org.Name != "Acme" || org.AssetsHosts != 12 || org.NaceCode != "62.01" {
		t.Errorf("org = %+v", org)
	}
	if got.path != "/api/internal/organisation" || got.query != "userId=52" {
		t.Errorf("request = %s?%s", got.path, got.query)
	}
}

func TestGetUserSettings(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"ai":{"research":"personal","chat":"internal","dashboard":"internal"},"language":"de"}`)

	settings, err := c.GetUserSettings(52)
	if err != nil {
		t.Fatalf("GetUserSettings: %v", err)
	}
	if settings.AI.Research != "personal" || settings.Language != "de" {
		t.Errorf("settings = %+v", settings)
	}
	if got.path != "/api/internal/users/52/settings" {
		t.Errorf("path = %q", got.path)
	}
}

func TestGetWorkspace(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"instanceMode":"appliance","primaryUserId":52}`)

	ws, err := c.GetWorkspace()
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if ws.PrimaryUserID != 52 {
		t.Errorf("workspace = %+v", ws)
	}
	// The mode is what decides whether every user shares one context, so the
	// derived answer is part of the contract, not just the raw field.
	if !ws.IsAppliance() {
		t.Errorf("instanceMode %q did not read as an appliance", ws.InstanceMode)
	}
	if got.path != "/api/internal/workspace" {
		t.Errorf("path = %q", got.path)
	}
}

func TestRejectsNonOK(t *testing.T) {
	c, _ := serve(t, http.StatusUnauthorized, `{}`)
	if _, err := c.GetAllUsers(); err == nil {
		t.Error("a 401 was accepted")
	}
	if _, err := c.GetOrganisationForUser(52); err == nil {
		t.Error("a 401 was accepted by GetOrganisationForUser")
	}
}
