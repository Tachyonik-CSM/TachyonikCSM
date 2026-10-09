// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that a run's bookkeeping happens exactly once, whatever the outcome:
// one report to AIManager and one audit entry, with the right level.

package run

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"tachyonik/actionexecutor/internal/aimanager"
	"tachyonik/actionexecutor/internal/apibridge"
	"tachyonik/actionexecutor/internal/config"
	"tachyonik/actionexecutor/internal/jsruntime"
	"tachyonik/actionexecutor/internal/runaudit"
)

type recorder struct {
	mu      sync.Mutex
	reports []string // statuses
	audits  []string // levels
}

func (r *recorder) CreateAuditEvent(userID int64, level, module, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.audits = append(r.audits, level)
	return nil
}

// newRunner points every backend at one server that answers with nothing;
// reports and audit entries land in rec.
func newRunner(t *testing.T, rec *recorder) *Runner {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{}
	for _, u := range []*string{&cfg.AssetManager.URL, &cfg.SystemManager.URL, &cfg.ResourceManager.URL,
		&cfg.ActionManager.URL, &cfg.ToolManager.URL, &cfg.AIManager.URL} {
		*u = srv.URL
	}
	cfg.Execution.TimeoutSeconds = 1
	bridge := apibridge.New(cfg)
	r := New(cfg, bridge, nil, rec)
	r.reporter = reporterFunc(func() { rec.mu.Lock(); rec.reports = append(rec.reports, "report"); rec.mu.Unlock() })
	return r
}

type reporterFunc func()

func (f reporterFunc) ReportLastExecution(int64, aimanager.ReportLastExecutionRequest) error {
	f()
	return nil
}

func routine(t *testing.T, body string) *jsruntime.JSExecutorRuntime {
	t.Helper()
	rt := jsruntime.New()
	if err := rt.LoadFromString(`var executors = [{ name: "e", ruleId: 4, run: function (api) { ` + body + ` } }];`); err != nil {
		t.Fatalf("load: %v", err)
	}
	return rt
}

func TestEveryOutcomeIsReportedAndAuditedOnce(t *testing.T) {
	cases := []struct {
		name, body, level string
	}{
		{"success", `return { status: "success", message: "ok" };`, "Info"},
		{"reported error", `return { status: "error", message: "no appliance" };`, "Warning"},
		{"timeout", `while (true) {}`, "Warning"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := &recorder{}
			r := newRunner(t, rec)
			auth := apibridge.AuthContext{UserID: 52}
			start := time.Now()
			if _, err := r.Execute(4, routine(t, c.body), auth, runaudit.ManualSubject("r", 4)); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if time.Since(start) > 5*time.Second {
				t.Fatal("the run was not bounded by its limit")
			}
			if len(rec.reports) != 1 {
				t.Errorf("reported %d time(s), want once", len(rec.reports))
			}
			if len(rec.audits) != 1 || rec.audits[0] != c.level {
				t.Errorf("audited %v, want one %s entry", rec.audits, c.level)
			}
		})
	}
}
