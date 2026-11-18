// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins that only the code that was generated runs: an active routine with no
// checksum, or one its code no longer matches, is refused and the rule runs no
// routine; the routine test refuses it too (docs/plans/routine-checksums.md).

package aianalyser

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tachyonik/lib/aicode"
	"tachyonik/sourceanalyser/internal/aimanager"
	"tachyonik/sourceanalyser/internal/config"
	"tachyonik/sourceanalyser/internal/jsruntime"
)

func analyserServing(t *testing.T, sha string) *AIAnalyser {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 5, "rule": 1, "code": sampleAnalysisRoutine, "status": "passed", "sha256": sha})
	}))
	t.Cleanup(srv.Close)
	cfg := &config.Config{}
	cfg.Analyzer.JSExecTimeoutSeconds = 5
	return New(aimanager.NewClient(srv.URL, "k"), nil, cfg)
}

func TestOnlyARoutineMatchingItsChecksumRuns(t *testing.T) {
	for _, c := range []struct {
		name, sha string
		runs      bool
	}{
		{"matching", aicode.Checksum(sampleAnalysisRoutine), true},
		{"missing", "", false},
		{"altered code", aicode.Checksum("var rules = [];"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := analyserServing(t, c.sha)
			active := int64(5)
			a.loadExecutorForRuleLocked(&aimanager.AnalysisRule{ID: 1, Type: "nmap", ActiveRoutine: &active})
			if _, runs := a.executors[1]; runs != c.runs {
				t.Errorf("runs = %v, want %v", runs, c.runs)
			}
			_, err := a.AnalyzeSourceWithRoutine(jsruntime.RuleContext{Filename: "x.xml", Content: "<nmaprun"}, 5)
			if refused := err != nil && strings.Contains(err.Error(), "SHA256"); refused == c.runs {
				t.Errorf("routine test: err = %v", err)
			}
		})
	}
}
