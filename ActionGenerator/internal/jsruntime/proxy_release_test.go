// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests ctx.proxies[].updateAvailable and ctx.proxyRelease: a rule about
// outdated proxies must validate, which needs scenarios with and without one,
// and the release must read as "" — not undefined — while it is not known.

package jsruntime

import (
	"testing"
	"time"
)

const outdatedProxiesRoutine = `
function outdated(ctx) {
  var out = [];
  for (var i = 0; i < ctx.proxies.length; i++) {
    if (ctx.proxies[i].updateAvailable) out.push(ctx.proxies[i]);
  }
  return out;
}

var rules = [{
  name: "update-outdated-proxies",
  ruleId: 98,
  check: function(ctx) { return outdated(ctx).length > 0; },
  createAction: function(ctx) {
    var names = outdated(ctx).map(function(p) { return p.name + " (" + p.version + ")"; });
    return { title: "Update outdated proxies",
             description: names.join(", ") + " → " + ctx.proxyRelease.latestVersion,
             type: "Fix", status: "New", priority: 100 };
  }
}];
`

func TestTheOutdatedProxiesRoutineValidates(t *testing.T) {
	e := New(5 * time.Second)
	if err := e.LoadFromString(outdatedProxiesRoutine); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	report := e.ValidateWithMockCtxReport()
	if len(report.Errors) > 0 {
		t.Fatalf("the routine is rejected: %v", report.Errors)
	}
}

// Being outdated must not coincide with being offline in the scenarios, or a
// rule about one could not be told from a rule about the other.
func TestTheMockScenariosVaryOutdatedApartFromOffline(t *testing.T) {
	outdated, outdatedOnline := 0, 0
	for _, sc := range MockScenarios() {
		for _, p := range sc.Ctx.Proxies {
			if p["updateAvailable"] == true {
				outdated++
				if p["status"] == "online" {
					outdatedOnline++
				}
			}
		}
	}
	if outdated == 0 || outdatedOnline == 0 {
		t.Errorf("%d outdated proxies, %d of them online; need both an online and an offline one", outdated, outdatedOnline)
	}
}

func TestTheProxyReleaseIsNeverUndefined(t *testing.T) {
	m := RuleContext{}.ToMap()
	r, ok := m["proxyRelease"].(map[string]interface{})
	if !ok || r["latestVersion"] != "" {
		t.Errorf("proxyRelease = %#v, want {latestVersion: \"\"}", m["proxyRelease"])
	}
}
