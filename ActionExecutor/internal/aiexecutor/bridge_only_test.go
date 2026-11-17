// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that only bridge routines are loaded and generated.
//
// An active routine without `var routineContract = 2;` was written for the
// api object this module no longer has: it is held back as it loads, with a
// reason naming the rule and routine for the runs asked of the rule. A
// generated routine without the declaration fails validation, and a system
// prompt that does not ask for the catalogue is not sent to the AI at all.

package aiexecutor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"tachyonik/actionexecutor/internal/aimanager"
	"tachyonik/actionexecutor/internal/config"
	libbridge "tachyonik/lib/apibridge"
	"tachyonik/lib/apiholder"
)

// emptyCatalogue is a loaded catalogue with no operations.
func emptyCatalogue(t *testing.T) *apiholder.Holder {
	t.Helper()
	h := apiholder.New(nil, libbridge.ReadWrite, libbridge.Options{})
	if err := h.Load("fp", nil); err != nil {
		t.Fatal(err)
	}
	return h
}

const legacyRoutine = `var executors = [{ name: "x", ruleId: 4, run: function (api) { return { status: "success", message: api.user.get().username }; } }];`

func TestARoutineThatPredatesThePlatformAPIIsHeldBack(t *testing.T) {
	ag := withLoadedRoutine(t, &aimanager.Routine{ID: 240, Code: legacyRoutine, Status: "passed"})
	next := int64(240)
	ag.mu.Lock()
	ag.loadActiveRoutine(&aimanager.ExecutionRule{ID: 4, Title: "r", ActiveRoutine: &next})
	ag.mu.Unlock()
	if ag.GetRuntime(4) != nil {
		t.Error("a routine without the routine contract was loaded")
	}
	if got, want := ag.HeldBack(4), PredatesAPIMessage(4, 240); got != want {
		t.Errorf("held back for %q, want %q", got, want)
	}
	if !strings.Contains(PredatesAPIMessage(4, 240), "execution rule 4: routine 240 predates the platform API") {
		t.Errorf("message %q", PredatesAPIMessage(4, 240))
	}

	// Its successor loads, and the reason goes.
	ag2 := withLoadedRoutine(t, &aimanager.Routine{ID: 241, Code: sampleExecRoutine, Status: "passed"})
	ag2.heldBack[4] = "old"
	next = 241
	ag2.mu.Lock()
	ag2.loadActiveRoutine(&aimanager.ExecutionRule{ID: 4, Title: "r", ActiveRoutine: &next})
	ag2.mu.Unlock()
	if ag2.GetRuntime(4) == nil || ag2.HeldBack(4) != "" {
		t.Errorf("a bridge routine did not replace the held-back one: held %q", ag2.HeldBack(4))
	}
}

// countingChat answers with code and counts the requests.
type countingChat struct {
	code  string
	calls *atomic.Int32
}

func (c countingChat) Chat(string, string, string) (string, error) {
	c.calls.Add(1)
	return c.code, nil
}

// generationServer is an AIManager that stores routines and keeps the last
// one's status and log.
func generationServer(t *testing.T, stored *aimanager.CreateRoutineRequest) *aimanager.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/internal/routines" && r.Method == "POST" {
			_ = json.NewDecoder(r.Body).Decode(stored)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":77}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return aimanager.NewClient(srv.URL, "k")
}

func TestAGeneratedRoutineWithoutTheContractFailsValidation(t *testing.T) {
	var stored aimanager.CreateRoutineRequest
	var calls atomic.Int32
	cfg := &config.Config{AI: config.AIConfig{SystemPrompt: "write " + APICataloguePlaceholder, Model: "m"}}
	ag := New(generationServer(t, &stored), countingChat{code: legacyRoutine, calls: &calls}, cfg)
	ag.SetCatalogue(emptyCatalogue(t))

	_, err := ag.GenerateForRule(&aimanager.ExecutionRule{ID: 4, Title: "r"})
	if err == nil || !strings.Contains(err.Error(), "the routine must begin with var routineContract = 2;") {
		t.Fatalf("err = %v", err)
	}
	if stored.Status != "failed" || !strings.Contains(stored.Log, "var routineContract = 2;") {
		t.Errorf("stored %q with log %q, want failed saying why", stored.Status, stored.Log)
	}
}

func TestAPromptThatPredatesThePlatformAPIIsNotSentToTheAI(t *testing.T) {
	var stored aimanager.CreateRoutineRequest
	var calls atomic.Int32
	cfg := &config.Config{AI: config.AIConfig{SystemPrompt: "api.user.get() returns the user", Model: "m"}}
	ag := New(generationServer(t, &stored), countingChat{code: sampleExecRoutine, calls: &calls}, cfg)
	ag.SetCatalogue(emptyCatalogue(t))

	_, err := ag.GenerateForRule(&aimanager.ExecutionRule{ID: 4, Title: "r"})
	if err == nil || !strings.Contains(err.Error(), "predates the platform API") || !strings.Contains(err.Error(), "prompts/actionexecutor.txt") {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("the AI was asked %d time(s)", calls.Load())
	}
	if stored.Code != "" {
		t.Error("a routine was stored")
	}
}
