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
	"net/http"
	"strings"
	"testing"
	"time"
)

// A rule that remembers the last user it saw, and says so in its action. The
// things it is answered with carry the user's id.
const remembersLastUser = `var routineContract = 2;
var lastUser = "nobody";
var rules = [{
	name: "remembers", ruleId: 3,
	check: function (api) { return true; },
	createAction: function (api) {
		var seen = lastUser;
		lastUser = "user " + api.things.list().things[0].id;
		return { title: "Previous: " + seen, type: "Info", status: "New", priority: 10 };
	}
}];`

func TestAnEvaluationSeesNothingOfAnotherUsers(t *testing.T) {
	e := New(5 * time.Second)
	if err := e.LoadFromString(remembersLastUser); err != nil {
		t.Fatalf("load: %v", err)
	}
	b := thingsBridgeFunc(t, func(r *http.Request) (int, string) {
		return 200, `{"things":[{"id":` + r.Header.Get("X-User-ID") + `,"stale":false}]}`
	})
	pass := b.NewPass()
	for _, user := range []int64{41, 42} {
		actions, unknown, err := e.EvaluateBridge(pass, user, false)
		if err != nil || unknown || len(actions) != 1 {
			t.Fatalf("user %d: %+v, unknown=%v, %v", user, actions, unknown, err)
		}
		if strings.Contains(actions[0].Title, "user 41") {
			t.Errorf("user %d's action reads %q — user 41's evaluation leaked into it", user, actions[0].Title)
		}
	}
}
