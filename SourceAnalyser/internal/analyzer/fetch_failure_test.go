// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests of a source whose file cannot be fetched (SA-ANA-13): a missing file
// (404) is written File Not Found, a passing failure leaves the source
// Analysing for the next sweep.

package analyzer

import (
	"net/http"
	"testing"

	"tachyonik/sourceanalyser/internal/config"
	"tachyonik/sourceanalyser/internal/resourcemanager"
)

func TestFetchFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		wantStatus string // the last status written; "" = none
		wantType   string
	}{
		{"a missing file", http.StatusNotFound, "File Not Found", "Error"},
		{"a server error", http.StatusInternalServerError, "", "Plain"},
		{"ResourceManager unavailable", http.StatusServiceUnavailable, "", "Plain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rm, rmClient := newFakeRM(t, resourcemanager.Source{ID: 1, Filename: "x.txt", SourceType: "Plain", Status: "Analysing"})
			rm.fileStatus = tc.status
			cfg := &config.Config{Analyzer: config.AnalyzerConfig{MaxContentBytes: 1024, MaxPDFBytes: 1024, JSExecTimeoutSeconds: 5}}
			a := New(rmClient, cfg)

			err := a.processSource(rm.sources[1])
			written := rm.written(1)
			if tc.wantStatus == "" {
				if err == nil {
					t.Error("processSource returned no error for a passing failure")
				}
				if len(written) != 0 {
					t.Errorf("statuses written = %v, want none (the source stays Analysing)", written)
				}
			} else if len(written) == 0 || written[len(written)-1] != tc.wantStatus {
				t.Errorf("statuses written = %v, want the last to be %q", written, tc.wantStatus)
			}
			if got := rm.sources[1]; got.SourceType != tc.wantType {
				t.Errorf("type = %q, want %q", got.SourceType, tc.wantType)
			}
		})
	}
}
