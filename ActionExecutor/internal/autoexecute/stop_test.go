// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins that an action arriving while ActionExecutor stops is not executed:
// nothing runs, nothing is reported or audited, and the action is kept.

package autoexecute

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"tachyonik/actionexecutor/internal/actionwatcher"
	"tachyonik/actionexecutor/internal/aiexecutor"
	"tachyonik/actionexecutor/internal/aimanager"
	"tachyonik/actionexecutor/internal/config"
	"tachyonik/actionexecutor/internal/run"
	"tachyonik/lib/aicode"
	"tachyonik/lib/drain"
)

func TestNoAutomaticRunStartsAfterTheStop(t *testing.T) {
	routineID := int64(241)
	code := "var routineContract = 2;\nvar executors = [{ name: \"x\", ruleId: 4, run: function (api) { return { status: \"success\" }; } }];"
	var mu sync.Mutex
	var touched []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/internal/execution-rules":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"executionRules": []aimanager.ExecutionRule{
				{ID: 4, Title: "retrieve", ActiveRoutine: &routineID},
			}})
		case r.URL.Path == "/api/internal/routines/241":
			_ = json.NewEncoder(w).Encode(aimanager.Routine{ID: 241, Status: "passed", Code: code, SHA256: aicode.Checksum(code)})
		case r.URL.Path == "/api/internal/action-options":
			_, _ = w.Write([]byte(`{"actionOptions":[{"id":1,"actionRuleId":8,"type":"Execution","typeContext":"4"}]}`))
		case r.URL.Path == "/api/user-execution-rule-settings":
			_, _ = w.Write([]byte(`{"settings":[{"executionRuleId":4,"status":"automatic"}]}`))
		default:
			mu.Lock()
			touched = append(touched, r.Method+" "+r.URL.Path)
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{}
	cfg.SystemManager.URL, cfg.ActionManager.URL = srv.URL, srv.URL
	ai := aimanager.NewClient(srv.URL, "k")
	exec := aiexecutor.New(ai, nil, cfg)
	if err := exec.LoadRules(); err != nil {
		t.Fatal(err)
	}
	if exec.GetRuntime(4) == nil {
		t.Fatalf("the routine did not load: %s", exec.HeldBack(4))
	}
	trail := &audits{}
	h := New(ai, exec, run.New(cfg, ai, trail), cfg)
	var gate drain.Gate
	h.SetGate(&gate)
	gate.Stop()

	ruleID := int64(8)
	h.OnActionCreated(actionwatcher.Action{ID: 813, UserID: 52, Title: "Retrieve", ActionRuleID: &ruleID})
	mu.Lock()
	defer mu.Unlock()
	trail.mu.Lock()
	defer trail.mu.Unlock()
	if len(touched) != 0 || len(trail.text) != 0 {
		t.Errorf("after the stop: requests %v, audited %v", touched, trail.text)
	}
}
