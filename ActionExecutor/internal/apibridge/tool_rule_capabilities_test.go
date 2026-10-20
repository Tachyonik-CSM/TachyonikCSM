// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that api.tools.list gives each of a tool's rules the capabilities
// linked to it.
//
// Without them a routine could only tell a tool's rules apart by their
// wording: once the OPENVAS tool had a host assets rule and a vulnerability
// results rule, "Retrieve host assets" matched "host assets" in the newer
// rule's description and ran that one instead.

package apibridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dop251/goja"

	"tachyonik/actionexecutor/internal/config"
)

func TestToolRulesCarryTheirCapabilities(t *testing.T) {
	answers := map[string]string{
		"/api/tools":                      `{"tools":[{"id":10,"name":"","command":"OPENVAS SCAN","toolId":2}]}`,
		"/api/tool-overview":              `{"toolOverview":[{"id":2,"automatedCapabilityIds":[1,2],"manualCapabilityIds":[]}]}`,
		"/api/internal/tool-capabilities": `{"toolCapabilities":[{"id":1,"name":"Host information"},{"id":2,"name":"Vulnerability information"}]}`,
		// Newest first, as AIManager lists them; the third has no capability.
		"/api/internal/tool-rules": `{"tools":[
			{"id":8,"name":"openvas get latest vulnerability results","description":"… of host assets …","toolOverviewId":2,"automatedCapabilityIds":[2]},
			{"id":6,"name":"openvas get host assets","description":"…","toolOverviewId":2,"automatedCapabilityIds":[1]},
			{"id":5,"name":"older","description":"…","toolOverviewId":2}]}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := answers[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{}
	cfg.ResourceManager.URL = srv.URL
	cfg.AIManager.URL = srv.URL
	vm := goja.New()
	_ = vm.Set("api", New(cfg).Build(vm, AuthContext{UserID: 52}))
	v, err := vm.RunString(`JSON.stringify(api.tools.list()[0].rules)`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var rules []struct {
		ID                    int64    `json:"id"`
		AutomatedCapabilities []string `json:"automatedCapabilities"`
	}
	if err := json.Unmarshal([]byte(v.String()), &rules); err != nil {
		t.Fatalf("decode %s: %v", v.String(), err)
	}
	want := map[int64]string{8: "Vulnerability information", 6: "Host information", 5: ""}
	if len(rules) != len(want) {
		t.Fatalf("got %d rules, want %d: %s", len(rules), len(want), v.String())
	}
	for _, r := range rules {
		w := want[r.ID]
		if w == "" {
			// Present and empty, so a routine can call indexOf on it.
			if r.AutomatedCapabilities == nil || len(r.AutomatedCapabilities) != 0 {
				t.Errorf("rule %d: %v, want []", r.ID, r.AutomatedCapabilities)
			}
			continue
		}
		if len(r.AutomatedCapabilities) != 1 || r.AutomatedCapabilities[0] != w {
			t.Errorf("rule %d: %v, want [%s]", r.ID, r.AutomatedCapabilities, w)
		}
	}
}
