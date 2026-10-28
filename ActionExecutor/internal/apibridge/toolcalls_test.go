// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that the tool calls a routine makes are recorded for the run's audit
// entry: by tool and proxy name as ToolManager answers, a tool's own error as
// a failure with its message, and a call ToolManager refused by the tool
// record's id — still recorded although the routine sees it thrown.

package apibridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dop251/goja"

	"tachyonik/actionexecutor/internal/config"
)

func TestARunsToolCallsAreRecorded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ToolID int64 `json:"toolId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		switch body.ToolID {
		case 1:
			_, _ = w.Write([]byte(`{"toolName": "gmp-get-results", "proxyName": "office", "isError": false}`))
		case 2:
			_, _ = w.Write([]byte(`{"toolName": "gmp-get-assets", "proxyName": "office", "isError": true, "content": "Authentication failed"}`))
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error": "Proxy is not connected (status: offline)."}`))
		}
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{}
	cfg.ToolManager.URL = srv.URL
	log := &ToolCallLog{}
	vm := goja.New()
	_ = vm.Set("api", New(cfg).BuildRecording(vm, AuthContext{UserID: 52}, log))
	if _, err := vm.RunString(`
		api.tools.execute(1, {});
		api.tools.execute(2, {});
		try { api.tools.execute(3, {}); } catch (e) {}`); err != nil {
		t.Fatalf("run: %v", err)
	}

	calls := log.Calls()
	if len(calls) != 3 {
		t.Fatalf("recorded %d calls, want 3: %+v", len(calls), calls)
	}
	if c := calls[0]; c.Tool != "gmp-get-results" || c.Proxy != "office" || !c.OK {
		t.Errorf("first call %+v", c)
	}
	if c := calls[1]; c.OK || c.Reason != "Authentication failed" {
		t.Errorf("a tool's own error was not a failure with its message: %+v", c)
	}
	if c := calls[2]; c.OK || c.Tool != "tool 3" || !strings.Contains(c.Reason, "503") {
		t.Errorf("a refused call %+v", c)
	}
}
