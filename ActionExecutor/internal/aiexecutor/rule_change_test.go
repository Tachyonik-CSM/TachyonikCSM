// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that a change to one execution rule reloads that rule only.
//
// Every edit used to re-fetch every rule's active routine from AIManager, one
// request per rule, although each rule's change is announced on its own.

package aiexecutor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"tachyonik/actionexecutor/internal/aimanager"
	"tachyonik/actionexecutor/internal/config"
	"tachyonik/actionexecutor/internal/executionrulewatcher"
)

func TestAChangedRuleReloadsOnlyItself(t *testing.T) {
	r3, r4 := int64(103), int64(104)
	var routineFetches atomic.Int32
	var fetched atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/internal/execution-rules":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"executionRules": []aimanager.ExecutionRule{
				{ID: 3, Title: "other", ActiveRoutine: &r3},
				{ID: 4, Title: "changed", ActiveRoutine: &r4},
			}})
		case strings.HasPrefix(r.URL.Path, "/api/internal/routines/"):
			routineFetches.Add(1)
			if r.URL.Path == "/api/internal/routines/104" {
				fetched.Store(104)
			}
			_ = json.NewEncoder(w).Encode(aimanager.Routine{ID: 104, Code: sampleExecRoutine, Status: "passed"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	ag := New(aimanager.NewClient(srv.URL, "k"), nil, &config.Config{})
	ag.HandleRuleChange(executionrulewatcher.RuleChangeEvent{Type: "updated", RuleID: 4})

	if n := routineFetches.Load(); n != 1 || fetched.Load() != 104 {
		t.Errorf("fetched %d routine(s), want only the changed rule's (104)", n)
	}
	if ag.GetRuntime(4) == nil {
		t.Error("the changed rule has no routine loaded")
	}
	if ag.GetRule(3) != nil {
		t.Error("an unchanged rule was loaded by another rule's change")
	}
}
