// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests which of ResourceManager's messages hand a source on: only a source
// created or updated with status "Analysing" — so SourceAnalyser's own result,
// written back as SOURCE_UPDATED, causes no loop (SA-ANA-01, SA-IF-10).

package resourcemanager

import (
	"encoding/json"
	"testing"

	"tachyonik/lib/wswatcher"
)

func TestOnlyAWaitingSourceIsHandedOn(t *testing.T) {
	for _, c := range []struct {
		typ, payload string
		want         int64
	}{
		{"SOURCE_CREATED", `{"id":5,"status":"Analysing"}`, 5},
		{"SOURCE_UPDATED", `{"id":6,"status":"Analysing","testRoutineId":3}`, 6},
		{"SOURCE_UPDATED", `{"id":6,"status":"Analysed","sourceType":"Unsupported"}`, 0},
		{"SOURCE_UPDATED", `{"id":6,"status":"File Not Found"}`, 0},
		{"SOURCE_CREATED", `{"id":7,"status":"Analysed","testModule":"SourceImporter"}`, 0},
		{"SOURCE_DELETED", `{"id":5}`, 0},
		{"TOOL_UPDATED", `{"id":5,"status":"Analysing"}`, 0},
		{"SOURCE_CREATED", `not json`, 0},
		{"SOURCE_CREATED", `{"status":"Analysing"}`, 0},
	} {
		id, ok := waitingSource(wswatcher.Message{Type: c.typ, Payload: json.RawMessage(c.payload)})
		if (c.want != 0) != ok || id != c.want {
			t.Errorf("%s %s: %d, %v; want %d", c.typ, c.payload, id, ok, c.want)
		}
	}
}
