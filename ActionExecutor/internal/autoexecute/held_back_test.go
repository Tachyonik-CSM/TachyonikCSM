// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that an action qualifying for automatic execution whose rule's routine
// predates the platform API is refused visibly: the rule's last execution is
// reported as failed with the reason, the run is audited as failed, and the
// action is not deleted, so the user still has it.

package autoexecute

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"tachyonik/lib/aicode"
	"testing"

	"tachyonik/actionexecutor/internal/actionwatcher"
	"tachyonik/actionexecutor/internal/aiexecutor"
	"tachyonik/actionexecutor/internal/aimanager"
	"tachyonik/actionexecutor/internal/config"
	"tachyonik/actionexecutor/internal/run"
)

type audits struct {
	mu   sync.Mutex
	text []string
}

func (a *audits) CreateAuditEvent(_ int64, level, _, message string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.text = append(a.text, level+": "+message)
	return nil
}

func TestAnActionWhoseRoutinePredatesTheAPIIsRefused(t *testing.T) {
	routineID := int64(240)
	var mu sync.Mutex
	var reported, deleted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/internal/execution-rules":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"executionRules": []aimanager.ExecutionRule{
				{ID: 4, Title: "retrieve", ActiveRoutine: &routineID},
			}})
		case r.URL.Path == "/api/internal/routines/240":
			code := `var executors = [{ name: "x", ruleId: 4, run: function (api) { return { status: "success" }; } }];`
			_ = json.NewEncoder(w).Encode(aimanager.Routine{ID: 240, Status: "passed", Code: code, SHA256: aicode.Checksum(code)})
		case r.URL.Path == "/api/internal/action-options":
			_, _ = w.Write([]byte(`{"actionOptions":[{"id":1,"actionRuleId":8,"type":"Execution","typeContext":"4"}]}`))
		case r.URL.Path == "/api/user-execution-rule-settings":
			_, _ = w.Write([]byte(`{"settings":[{"executionRuleId":4,"status":"automatic"}]}`))
		case r.URL.Path == "/api/internal/execution-rules/4/last-execution":
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			reported = string(b)
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/api/actions":
			mu.Lock()
			deleted = r.Method
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
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
	trail := &audits{}
	h := New(ai, exec, run.New(cfg, ai, trail), cfg)

	ruleID := int64(8)
	h.OnActionCreated(actionwatcher.Action{ID: 812, UserID: 52, Title: "Retrieve", ActionRuleID: &ruleID})

	want := aiexecutor.PredatesAPIMessage(4, 240)
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(reported, `"status":"error"`) || !strings.Contains(reported, "predates the platform API") {
		t.Errorf("reported %s, want the failure with %q", reported, want)
	}
	if deleted != "" {
		t.Error("the action was deleted although nothing ran")
	}
	trail.mu.Lock()
	defer trail.mu.Unlock()
	if len(trail.text) != 1 || !strings.HasPrefix(trail.text[0], "Warning: ") || !strings.Contains(trail.text[0], "routine 240 predates the platform API") {
		t.Errorf("audited %v", trail.text)
	}
}
