// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests ctx.proxies, the user's TachyonikProxy installations.
//
// The case these are written around happened: action rule 19, "All proxies are
// online", was generated into a routine that read ctx.proxies — the obvious
// field for its trigger — and validation rejected it, because the context had
// no proxies at all and the check could never fire. The routine below is that
// generation's code, check() verbatim.

package jsruntime

import (
	"testing"
	"time"
)

const rule19Routine = `
function countOfflineProxies(ctx) {
  var list = ctx.proxies;
  if (!list || typeof list.length !== "number") {
    return 0;
  }
  var n = 0;
  for (var i = 0; i < list.length; i++) {
    var s = list[i] && list[i].status;
    if (s && s.toLowerCase() === "offline") {
      n++;
    }
  }
  return n;
}

var rules = [{
  name: "all-proxies-are-online",
  ruleId: 19,
  check: function(ctx) { return countOfflineProxies(ctx) > 0; },
  createAction: function(ctx) {
    return { title: "All proxies are online", type: "Fix", status: "New", priority: 100 };
  }
}];
`

func TestTheProxyOfflineRoutineValidates(t *testing.T) {
	e := New(5 * time.Second)
	if err := e.LoadFromString(rule19Routine); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	report := e.ValidateWithMockCtxReport()
	if len(report.Errors) > 0 {
		t.Fatalf("rule 19's routine is still rejected: %v", report.Errors)
	}
}

// The mock scenarios have to give a proxy rule something to tell apart, or a
// correct one is rejected as never firing — the second of the two errors.
func TestTheMockScenariosVaryTheProxies(t *testing.T) {
	withOffline, withoutOffline := 0, 0
	for _, sc := range MockScenarios() {
		offline := false
		for _, p := range sc.Ctx.Proxies {
			if p["status"] == "offline" {
				offline = true
			}
		}
		if offline {
			withOffline++
		} else {
			withoutOffline++
		}
	}
	if withOffline == 0 || withoutOffline == 0 {
		t.Errorf("%d scenario(s) with an offline proxy and %d without; a proxy rule needs both", withOffline, withoutOffline)
	}
}

// No proxies is an empty list, never null or undefined: ctx.proxies.length
// must work on a context where the lookup failed or the user has none.
func TestProxiesAreAlwaysAList(t *testing.T) {
	if v := evalAgainst(t, RuleContext{}, `ctx.proxies.length`); v.ToInteger() != 0 {
		t.Errorf("ctx.proxies.length = %v on an empty context, want 0", v)
	}
}

// A proxy that never connected has no last-seen time; it reads as "", like
// every other absent string in the context.
func TestANeverSeenProxyHasAnEmptyLastSeen(t *testing.T) {
	p := ProxyToMap(3, "New proxy", "pending_enrollment", "inbound", "", nil)
	if p["lastSeen"] != "" {
		t.Errorf("lastSeen = %#v, want \"\"", p["lastSeen"])
	}
}
