// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests of the status written for a recognised source: Analysed, whatever the
// routine returned (SA-IF-04, SA-IF-05).

package analyzer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"tachyonik/lib/aicode"
	"tachyonik/sourceanalyser/internal/aianalyser"
	"tachyonik/sourceanalyser/internal/aimanager"
	"tachyonik/sourceanalyser/internal/config"
	"tachyonik/sourceanalyser/internal/resourcemanager"
)

// A routine that recognises everything and claims a status of its own.
const routineWithOwnStatus = `var rules = [{
  name: "anything",
  ruleId: 1,
  analyze: function (ctx) { return { sourceType: "Example Format", status: "Imported" }; }
}];`

// SA-IF-05: a routine that returns another status (here "Imported") still
// leaves its source Analysed with the routine's type — before the decision
// such a source was written "Imported", never imported and never revisited.
func TestARecognisedSourceIsAnalysedWhateverTheRoutineSays(t *testing.T) {
	routineID := int64(7)
	aim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/internal/routines/7" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(aimanager.Routine{ID: routineID, Code: routineWithOwnStatus, SHA256: aicode.Checksum(routineWithOwnStatus)})
	}))
	t.Cleanup(aim.Close)

	source := resourcemanager.Source{ID: 1, Filename: "x.txt", Status: "Analysing", TestRoutineID: &routineID}
	rm, rmClient := newFakeRM(t, source)
	cfg := &config.Config{Analyzer: config.AnalyzerConfig{MaxContentBytes: 1024, MaxPDFBytes: 1024, JSExecTimeoutSeconds: 5}}
	a := New(rmClient, cfg)
	a.SetAIAnalyser(aianalyser.New(aimanager.NewClient(aim.URL, "key"), nil, cfg))

	s := rm.sources[1]
	if err := a.processSource(s); err != nil {
		t.Fatalf("processSource: %v", err)
	}
	written := rm.written(1)
	if len(written) == 0 || written[len(written)-1] != StatusAnalysed {
		t.Fatalf("statuses written = %v, want the last to be %q", written, StatusAnalysed)
	}
	if s.SourceType != "Example Format" {
		t.Errorf("type = %q, want the routine's %q", s.SourceType, "Example Format")
	}
}
