// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that one user's evaluation cannot see what another's left behind.
//
// A routine used to live in one VM shared by every user it was evaluated for,
// so a global a rule set while looking at one user's data was still there for
// the next — and could end up in that user's action text. Each evaluation now
// has a VM of its own.

package jsruntime

import (
	"strings"
	"testing"
	"time"
)

// A rule that remembers the last user it saw, and says so in its action.
const remembersLastUser = `var lastUser = "nobody";
var rules = [{
	name: "remembers", ruleId: 3,
	check: function (ctx) { return true; },
	createAction: function (ctx) {
		var seen = lastUser;
		lastUser = ctx.user.username;
		return { title: "Previous: " + seen, type: "Info", status: "New", priority: 10 };
	}
}];`

func TestAnEvaluationSeesNothingOfAnotherUsers(t *testing.T) {
	e := New(5 * time.Second)
	if err := e.LoadFromString(remembersLastUser); err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, user := range []string{"alice", "bob"} {
		actions, err := e.EvaluateRules(RuleContext{User: map[string]interface{}{"username": user}}, false)
		if err != nil || len(actions) != 1 {
			t.Fatalf("%s: %+v, %v", user, actions, err)
		}
		if strings.Contains(actions[0].Title, "alice") {
			t.Errorf("%s's action reads %q — alice's evaluation leaked into it", user, actions[0].Title)
		}
	}
}
