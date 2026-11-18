// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins that only the code that was generated runs: an active routine with no
// checksum, or one its code no longer matches, is refused and the rule runs no
// routine; the import test refuses it too (docs/plans/routine-checksums.md).

package aiimporter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tachyonik/lib/aicode"
	"tachyonik/sourceimporter/internal/aimanager"
	"tachyonik/sourceimporter/internal/config"
	"tachyonik/sourceimporter/internal/rscmanager"
)

func TestOnlyARoutineMatchingItsChecksumRuns(t *testing.T) {
	for _, c := range []struct {
		name, sha string
		runs      bool
	}{
		{"matching", aicode.Checksum(sampleImportRoutine), true},
		{"missing", "", false},
		{"altered code", aicode.Checksum("var rules = [];"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": 5, "rule": 1, "code": sampleImportRoutine, "status": "passed", "sha256": c.sha})
			}))
			defer srv.Close()
			ai := New(nil, aimanager.NewClient(srv.URL, "k"), nil, nil, &config.Config{})
			active := int64(5)
			ai.loadExecutorForRuleLocked(&aimanager.ImportRule{ID: 1, Type: "nmap", ActiveRoutine: &active})
			if _, runs := ai.executors[1]; runs != c.runs {
				t.Errorf("runs = %v, want %v", runs, c.runs)
			}
			if !c.runs {
				_, err := ai.RunImportTest(&rscmanager.Source{ID: 9, Filename: "x.xml"}, 5)
				if err == nil || !strings.Contains(err.Error(), "SHA256") {
					t.Errorf("import test: err = %v, want a refusal naming the checksum", err)
				}
			}
		})
	}
}
