// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins AG-GEN-16 on the load path: an active routine runs only when its code
// matches the checksum recorded with it — one with no checksum, or one its
// code no longer matches, is refused and the rule runs no routine. The check
// itself is TachyonikLib's aicode.VerifyChecksum, tested there.

package aigenerator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"tachyonik/actiongenerator/internal/aimanager"
	"tachyonik/actiongenerator/internal/config"
	"tachyonik/lib/aicode"
)

func TestOnlyARoutineMatchingItsChecksumRuns(t *testing.T) {
	for _, c := range []struct {
		name, sha string
		runs      bool
	}{
		{"matching", aicode.Checksum(bridgeRoutine), true},
		{"missing", "", false},
		{"altered code", aicode.Checksum(bridgeRoutine + " /* other */"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": 5, "rule": 1, "code": bridgeRoutine, "status": "passed",
					"sha256": c.sha, "apiOperations": `["things.count"]`})
			}))
			defer srv.Close()
			ag := New(aimanager.NewClient(srv.URL, "k"), nil, &config.Config{AI: config.AIConfig{JSExecTimeoutSeconds: 5}})
			ag.SetCatalogue(thingsCatalogue(t))
			active := int64(5)
			ag.loadActiveRoutine(&aimanager.ActionRule{ID: 1, Title: "Tidy", ActiveRoutine: &active})
			if _, runs := ag.executors[1]; runs != c.runs {
				t.Errorf("runs = %v, want %v", runs, c.runs)
			}
		})
	}
}
