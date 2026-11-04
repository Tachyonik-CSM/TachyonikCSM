// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests ctx.tools, the user's tool records: a rule about a tool that needs a
// credential but has none linked must validate, which needs mock scenarios in
// which such a tool exists and others in which it does not.

package jsruntime

import (
	"testing"
	"time"
)

const missingCredentialRoutine = `
function toolsMissingCredential(ctx) {
  var out = [];
  for (var i = 0; i < ctx.tools.length; i++) {
    var t = ctx.tools[i];
    if (t.credentialType !== "" && !t.hasCredential) out.push(t);
  }
  return out;
}

var rules = [{
  name: "provide-credentials-for-tools",
  ruleId: 99,
  check: function(ctx) { return toolsMissingCredential(ctx).length > 0; },
  createAction: function(ctx) {
    var names = toolsMissingCredential(ctx).map(function(t) { return t.name + " on " + t.proxyName; });
    return { title: "Provide credentials for tools", description: names.join(", "), type: "Fix", status: "New", priority: 100 };
  }
}];
`

func TestTheMissingCredentialRoutineValidates(t *testing.T) {
	e := New(5 * time.Second)
	if err := e.LoadFromString(missingCredentialRoutine); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	report := e.ValidateWithMockCtxReport()
	if len(report.Errors) > 0 {
		t.Fatalf("the routine is rejected: %v", report.Errors)
	}
}

func TestTheMockScenariosVaryTheToolCredentials(t *testing.T) {
	with, without := 0, 0
	for _, sc := range MockScenarios() {
		missing := false
		for _, tl := range sc.Ctx.Tools {
			if tl["credentialType"] != "" && tl["hasCredential"] == false {
				missing = true
			}
		}
		if missing {
			with++
		} else {
			without++
		}
	}
	if with == 0 || without == 0 {
		t.Errorf("%d scenario(s) with a tool missing its credential and %d without; such a rule needs both", with, without)
	}
}

// No tools is an empty list, never null: ctx.tools.length must work.
func TestToolsAreNeverNull(t *testing.T) {
	m := RuleContext{}.ToMap()
	if l, ok := m["tools"].([]map[string]interface{}); !ok || l == nil {
		t.Errorf("tools = %#v, want an empty list", m["tools"])
	}
}
