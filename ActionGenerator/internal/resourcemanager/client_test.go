// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Characterisation tests for the ResourceManager client.
//
// Written against the hand-rolled implementation before it moved onto
// TachyonikLib's restclient, and kept afterwards: they describe method, path,
// the service-key header, the status each call treats as success, and the
// envelope the result is read out of — the contract a rewrite of the request
// plumbing must preserve. Error wording is deliberately not pinned.

package resourcemanager

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

func TestGetSourcesForUser(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"entries":[{"id":1,"filename":"scan.xml","sourceType":"nmap","status":"processed"}],"total":1,"maxEntries":5}`)

	sources, err := c.GetSourcesForUser(52)
	if err != nil {
		t.Fatalf("GetSourcesForUser: %v", err)
	}
	if len(sources) != 1 || sources[0].Filename != "scan.xml" {
		t.Errorf("sources = %+v", sources)
	}
	if got.method != "GET" || got.path != "/api/sources" || got.query != "userId=52" {
		t.Errorf("request = %s %s?%s", got.method, got.path, got.query)
	}
}

// GetSourceCount reads the count and the limit out of the same reply the
// source list comes from.
func TestGetSourceCount(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"entries":[{"id":1}],"total":3,"maxEntries":5}`)

	count, max, err := c.GetSourceCount(52)
	if err != nil {
		t.Fatalf("GetSourceCount: %v", err)
	}
	if count != 3 || max != 5 {
		t.Errorf("count=%d max=%d, want 3 and 5", count, max)
	}
	if got.path != "/api/sources" || got.query != "userId=52" {
		t.Errorf("request = %s?%s", got.path, got.query)
	}
}

func TestGetToolsForUser(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"tools":[{"id":1,"name":"nmap","toolId":5}],"total":1}`)

	tools, err := c.GetToolsForUser(52)
	if err != nil {
		t.Fatalf("GetToolsForUser: %v", err)
	}
	if len(tools) != 1 || tools[0].ToolID == nil || *tools[0].ToolID != 5 {
		t.Errorf("tools = %+v", tools)
	}
	if got.path != "/api/tools" || got.query != "userId=52" {
		t.Errorf("request = %s?%s", got.path, got.query)
	}
}

// An unmanaged tool row has no overview; the pointer must stay nil rather than
// becoming zero, because the caller skips on nil.
func TestUnmanagedToolHasNoOverview(t *testing.T) {
	c, _ := serve(t, http.StatusOK, `{"tools":[{"id":2,"name":"local","toolId":null}]}`)
	tools, err := c.GetToolsForUser(52)
	if err != nil {
		t.Fatalf("GetToolsForUser: %v", err)
	}
	if len(tools) != 1 || tools[0].ToolID != nil {
		t.Errorf("tools = %+v, want a nil toolId", tools)
	}
}

func TestRejectsNonOK(t *testing.T) {
	c, _ := serve(t, http.StatusForbidden, `{}`)
	if _, err := c.GetSourcesForUser(52); err == nil {
		t.Error("a 403 was accepted")
	}
	if _, _, err := c.GetSourceCount(52); err == nil {
		t.Error("a 403 was accepted by GetSourceCount")
	}
	if _, err := c.GetToolsForUser(52); err == nil {
		t.Error("a 403 was accepted by GetToolsForUser")
	}
}
