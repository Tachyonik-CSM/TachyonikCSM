// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests how a user's tool records get into the rule context: named as the
// Tools view names them, with their proxy's name, what their kind needs to
// authenticate (AIManager's catalogue) and whether a credential is linked.

package generator

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"tachyonik/actiongenerator/internal/aimanager"
	"tachyonik/actiongenerator/internal/jsruntime"
	"tachyonik/actiongenerator/internal/resourcemanager"
)

func TestToolsCarryTheirCredentialState(t *testing.T) {
	ai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/internal/tool-overviews" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"overviews":[
			{"id":1,"name":"Greenbone","credentialType":"username-password"},
			{"id":2,"name":"Nmap","credentialType":""}
		]}`))
	}))
	t.Cleanup(ai.Close)
	g := &Generator{aiMgr: aimanager.NewClient(ai.URL, "ai-key")}

	one, two := int64(1), int64(2)
	cred := int64(9)
	ctx := jsruntime.RuleContext{Proxies: []map[string]interface{}{
		jsruntime.ProxyToMap(5, "Office proxy", "online", "inbound", "1.0", nil, false),
	}}
	g.addTools(&ctx, []resourcemanager.Tool{
		{ID: 10, ProxyID: 5, ToolID: &one, Host: "10.0.0.5"},
		{ID: 11, ProxyID: 5, ToolID: &one, Host: "10.0.0.6", CredentialID: &cred},
		{ID: 12, ProxyID: 5, ToolID: &two},
		{ID: 13, ProxyID: 5, Command: "masscan"},
		// Used without a credential by choice: needs nothing here.
		{ID: 14, ProxyID: 5, ToolID: &one, Host: "10.0.0.7", CredentialDeclined: true},
	})

	if len(ctx.Tools) != 5 {
		t.Fatalf("got %d tools, want 5", len(ctx.Tools))
	}
	want := []struct {
		name, credentialType string
		hasCredential        bool
		declined             bool
	}{
		{"Greenbone", "username-password", false, false},
		{"Greenbone", "username-password", true, false},
		{"Nmap", "", false, false},
		{"masscan", "", false, false},
		{"Greenbone", "", false, true},
	}
	for i, w := range want {
		got := ctx.Tools[i]
		if got["name"] != w.name || got["credentialType"] != w.credentialType || got["hasCredential"] != w.hasCredential || got["credentialDeclined"] != w.declined {
			t.Errorf("tool %d = %v, want %+v", i, got, w)
		}
		if got["proxyName"] != "Office proxy" {
			t.Errorf("tool %d proxyName = %v", i, got["proxyName"])
		}
	}
}

func TestToolsAreEmptyWithoutAnyRecords(t *testing.T) {
	g := &Generator{}
	ctx := jsruntime.RuleContext{}
	g.addTools(&ctx, nil)
	if ctx.Tools == nil || len(ctx.Tools) != 0 {
		t.Errorf("tools = %#v, want an empty list", ctx.Tools)
	}
}
