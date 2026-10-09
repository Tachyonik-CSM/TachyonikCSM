// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that what runs is what AIManager says is active.
//
// A rule whose active routine was switched to one that could not be loaded —
// not "passed", gone, or not valid JavaScript — kept the routine it had loaded
// before, so an older routine went on running under a rule that no longer named
// it. Now such a rule has no routine loaded, and running it is refused.

package aiexecutor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"tachyonik/actionexecutor/internal/aimanager"
	"tachyonik/actionexecutor/internal/config"
	"tachyonik/actionexecutor/internal/jsruntime"
)

// withLoadedRoutine is an executor with rule 4 running sampleExecRoutine, and
// an AIManager that answers routine lookups with the given routine (or 404).
func withLoadedRoutine(t *testing.T, routine *aimanager.Routine) *AIActionExecutor {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if routine == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(routine)
	}))
	t.Cleanup(srv.Close)

	ag := New(aimanager.NewClient(srv.URL, "k"), nil, &config.Config{})
	rt := jsruntime.New()
	if err := rt.LoadFromString(sampleExecRoutine); err != nil {
		t.Fatalf("load: %v", err)
	}
	ag.runtimes[4] = rt
	return ag
}

func TestARoutineThatIsNotPassedLeavesNoRoutineLoaded(t *testing.T) {
	ag := withLoadedRoutine(t, &aimanager.Routine{ID: 240, Code: sampleExecRoutine, Status: "failed"})
	next := int64(240)
	ag.mu.Lock()
	ag.loadActiveRoutine(&aimanager.ExecutionRule{ID: 4, Title: "r", ActiveRoutine: &next})
	ag.mu.Unlock()
	if ag.GetRuntime(4) != nil {
		t.Error("the previous routine is still loaded after activating one that is not passed")
	}
}

func TestARoutineThatCannotBeFetchedLeavesNoRoutineLoaded(t *testing.T) {
	ag := withLoadedRoutine(t, nil)
	next := int64(240)
	ag.mu.Lock()
	ag.loadActiveRoutine(&aimanager.ExecutionRule{ID: 4, Title: "r", ActiveRoutine: &next})
	ag.mu.Unlock()
	if ag.GetRuntime(4) != nil {
		t.Error("the previous routine is still loaded after its successor could not be fetched")
	}
}

func TestAPassedRoutineReplacesThePreviousOne(t *testing.T) {
	ag := withLoadedRoutine(t, &aimanager.Routine{ID: 240, Code: sampleExecRoutine, Status: "passed"})
	before := ag.GetRuntime(4)
	next := int64(240)
	ag.mu.Lock()
	ag.loadActiveRoutine(&aimanager.ExecutionRule{ID: 4, Title: "r", ActiveRoutine: &next})
	ag.mu.Unlock()
	if got := ag.GetRuntime(4); got == nil || got == before {
		t.Error("a passed routine did not replace the previous one")
	}
}
