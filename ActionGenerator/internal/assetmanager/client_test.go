// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Characterisation tests for the AssetManager client.
//
// Written against the hand-rolled implementation before it moved onto
// TachyonikLib's restclient, and kept afterwards: they describe method, path,
// the service-key header, the status each call treats as success, and the
// envelope the result is read out of — the contract a rewrite of the request
// plumbing must preserve. Error wording is deliberately not pinned.

package assetmanager

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

func TestGetAssetsForUser(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"assets":[{"id":1,"name":"test-host","score":85},{"id":2,"name":"web","score":40}],"total":2}`)

	assets, err := c.GetAssetsForUser(52)
	if err != nil {
		t.Fatalf("GetAssetsForUser: %v", err)
	}
	if len(assets) != 2 || assets[0].Name != "test-host" {
		t.Errorf("assets = %+v", assets)
	}
	if got.method != "GET" || got.path != "/api/assets" {
		t.Errorf("request = %s %s", got.method, got.path)
	}
	if got.query != "userId=52" {
		t.Errorf("query = %q, want the user id", got.query)
	}
	if got.key != "test-key" {
		t.Errorf("service key header = %q", got.key)
	}
}

func TestGetAssetsRejectsNonOK(t *testing.T) {
	c, _ := serve(t, http.StatusBadGateway, `{}`)
	if _, err := c.GetAssetsForUser(52); err == nil {
		t.Error("a 502 was accepted")
	}
}

// The stats come back flat, not in an envelope.
func TestGetAssetStats(t *testing.T) {
	c, got := serve(t, http.StatusOK, `{"critical":1,"high":2,"medium":3,"low":4,"noThreat":5,"unmanaged":6,"total":21}`)

	stats, err := c.GetAssetStats(52)
	if err != nil {
		t.Fatalf("GetAssetStats: %v", err)
	}
	if stats.Critical != 1 || stats.Total != 21 || stats.Unmanaged != 6 {
		t.Errorf("stats = %+v", stats)
	}
	if got.path != "/api/assets/stats" || got.query != "userId=52" {
		t.Errorf("request = %s?%s", got.path, got.query)
	}
}

func TestMalformedBodyIsAnError(t *testing.T) {
	c, _ := serve(t, http.StatusOK, `{"assets": not json}`)
	if _, err := c.GetAssetsForUser(52); err == nil {
		t.Error("a malformed body was accepted")
	}
}

// A redirect must never be followed.
//
// Go strips Authorization across hosts but forwards custom headers verbatim,
// so a 302 would hand X-Internal-Service-Key to whatever host it names. No
// manager legitimately redirects. The guarantee comes from restclient, which
// all five of this daemon's clients now share — this test is here so a client
// quietly reverting to a bare http.Client would be caught.
func TestRedirectIsNotFollowed(t *testing.T) {
	var leaked string
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("X-Internal-Service-Key")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"assets":[]}`))
	}))
	defer elsewhere.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/api/assets", http.StatusFound)
	}))
	defer srv.Close()

	if _, err := NewClient(srv.URL, "secret-key").GetAssetsForUser(1); err == nil {
		t.Error("a redirect was followed and its body treated as the answer")
	}
	if leaked != "" {
		t.Errorf("the service key reached the redirect target: %q", leaked)
	}
}
