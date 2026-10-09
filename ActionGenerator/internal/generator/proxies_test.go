// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests how a user's proxies get into the rule context: fetched from
// ResourceManager's service-only route, mapped to the fields a rule reads, and
// an empty list — not a missing one — when ResourceManager does not answer.

package generator

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"tachyonik/actiongenerator/internal/jsruntime"
	"tachyonik/actiongenerator/internal/resourcemanager"
	"tachyonik/actiongenerator/internal/systemmanager"
)

func generatorWithResourceManager(t *testing.T, handler http.HandlerFunc) *Generator {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Generator{resourceMgr: resourcemanager.NewClient(srv.URL, "rm-key")}
}

func TestProxiesComeFromResourceManager(t *testing.T) {
	var gotPath, gotKey string
	g := generatorWithResourceManager(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.RequestURI(), r.Header.Get("X-Internal-Service-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"proxies":[
			{"id":6,"name":"Feature test Proxy","status":"offline","connectionMode":"inbound",
			 "version":"0.9.2","lastSeen":"2026-10-02T00:33:21+02:00","ipAddress":"192.168.178.20",
			 "netscanNetworks":"192.168.178.0/24"},
			{"id":7,"name":"New","status":"pending_enrollment","connectionMode":"outbound","lastSeen":null}
		],"total":2}`))
	})

	ctx := jsruntime.RuleContext{}
	g.addProxies(&ctx, &systemmanager.User{ID: 52})

	if gotPath != "/api/internal/proxies?userId=52" {
		t.Errorf("requested %s", gotPath)
	}
	if gotKey != "rm-key" {
		t.Errorf("service key %q", gotKey)
	}
	if len(ctx.Proxies) != 2 {
		t.Fatalf("got %d proxies, want 2", len(ctx.Proxies))
	}
	first := ctx.Proxies[0]
	if first["status"] != "offline" || first["name"] != "Feature test Proxy" || first["lastSeen"] != "2026-10-02T00:33:21+02:00" {
		t.Errorf("first proxy = %v", first)
	}
	// Only the fields a rule reasons about.
	for _, leaked := range []string{"ipAddress", "netscanNetworks"} {
		if _, ok := first[leaked]; ok {
			t.Errorf("%s reached the rule context", leaked)
		}
	}
	if ctx.Proxies[1]["lastSeen"] != "" {
		t.Errorf("a never-seen proxy has lastSeen %#v, want \"\"", ctx.Proxies[1]["lastSeen"])
	}
}

func TestProxiesAreEmptyWhenResourceManagerFails(t *testing.T) {
	g := generatorWithResourceManager(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})

	ctx := jsruntime.RuleContext{}
	g.addProxies(&ctx, &systemmanager.User{ID: 52})
	if ctx.Proxies == nil || len(ctx.Proxies) != 0 {
		t.Errorf("proxies = %#v, want an empty list", ctx.Proxies)
	}
}
