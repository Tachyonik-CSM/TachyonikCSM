// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that a generated routine becomes the rule's active routine when the
// request asked for it and the routine passed its checks — and only then.
//
// A new routine used to wait until someone picked it by hand, however the
// request was made.

package aiexecutor

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"tachyonik/actionexecutor/internal/aimanager"
	"tachyonik/actionexecutor/internal/config"
	"tachyonik/actionexecutor/internal/executionrulewatcher"
)

// fakeChat answers every request with the same code.
type fakeChat struct{ code string }

func (f fakeChat) Chat(string, string, string) (string, error) { return f.code, nil }

const passingRoutine = `var executors = [{ name: "x", ruleId: 4, run: function (api) { return { status: "success", message: "" }; } }];`

func TestAGeneratedRoutineIsActivatedOnlyWhenAskedAndPassed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		activate bool
		code     string
		want     bool
	}{
		{"asked and passed", true, passingRoutine, true},
		{"not asked", false, passingRoutine, false},
		{"asked but failed", true, `var executors = [ broken`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requested := "2026-10-20T10:00:00Z"
			var mu sync.Mutex
			var clear map[string]interface{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/api/internal/execution-rules":
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"executionRules": []aimanager.ExecutionRule{
						{ID: 4, Title: "retrieve", RulePrompt: "…", GenerateRequestedAt: &requested, GenerateActivate: tc.activate},
					}})
				case r.URL.Path == "/api/internal/routines" && r.Method == "POST":
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"id":77}`))
				case r.URL.Path == "/api/internal/execution-rules/4" && r.Method == "PATCH":
					body, _ := io.ReadAll(r.Body)
					mu.Lock()
					_ = json.Unmarshal(body, &clear)
					mu.Unlock()
					_, _ = w.Write([]byte(`{}`))
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)

			// AIManager does not answer for the module's AI (404 below), so
			// generation falls back to the AI loaded at startup: fakeChat.
			cfg := &config.Config{AI: config.AIConfig{SystemPrompt: "generate", Model: "m"}}
			ag := New(aimanager.NewClient(srv.URL, "k"), fakeChat{code: tc.code}, cfg)
			ag.HandleRuleChange(executionrulewatcher.RuleChangeEvent{Type: "updated", RuleID: 4})

			mu.Lock()
			defer mu.Unlock()
			if clear == nil || clear["clearGenerateRequest"] != true {
				t.Fatalf("the request was not cleared: %v", clear)
			}
			got, has := clear["activeRoutine"]
			if tc.want && (!has || got != float64(77)) {
				t.Errorf("activeRoutine %v, want 77", got)
			}
			if !tc.want && has {
				t.Errorf("activeRoutine %v sent, want none", got)
			}
			if failed := clear["generateError"] != ""; failed != strings.Contains(tc.name, "failed") {
				t.Errorf("generateError %q", clear["generateError"])
			}
		})
	}
}
