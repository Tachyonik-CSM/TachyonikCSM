// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests how a run request is carried out: claimed once, run for the user and
// role AIManager recorded, and answered — a request someone else took is left
// alone, and one whose rule cannot run is answered with the reason.

package manualrun

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"tachyonik/actionexecutor/internal/aimanager"
	"tachyonik/actionexecutor/internal/executionrulewatcher"
	"tachyonik/actionexecutor/internal/jsruntime"
	"tachyonik/actionexecutor/internal/run"
)

type fakeRequests struct {
	mu       sync.Mutex
	pending  []aimanager.ExecutionRequest
	claimed  map[int64]bool
	reported map[int64]aimanager.ExecutionOutcome
	done     map[int64]bool
}

func newFakeRequests(reqs ...aimanager.ExecutionRequest) *fakeRequests {
	return &fakeRequests{pending: reqs, claimed: map[int64]bool{}, reported: map[int64]aimanager.ExecutionOutcome{}, done: map[int64]bool{}}
}

func (f *fakeRequests) ListPendingExecutionRequests() ([]aimanager.ExecutionRequest, error) {
	return f.pending, nil
}

func (f *fakeRequests) ClaimExecutionRequest(id int64) (*aimanager.ExecutionRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimed[id] {
		return nil, aimanager.ErrNotClaimable
	}
	for _, r := range f.pending {
		if r.ID == id {
			f.claimed[id] = true
			r := r
			return &r, nil
		}
	}
	return nil, errors.New("no such request")
}

func (f *fakeRequests) ReportExecutionRequest(id int64, done bool, outcome aimanager.ExecutionOutcome) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reported[id], f.done[id] = outcome, done
	return nil
}

type fakeRules struct {
	rules    map[int64]*aimanager.ExecutionRule
	runtimes map[int64]*jsruntime.JSExecutorRuntime
	held     map[int64]string
}

func (f fakeRules) GetRule(id int64) *aimanager.ExecutionRule        { return f.rules[id] }
func (f fakeRules) GetRuntime(id int64) *jsruntime.JSExecutorRuntime { return f.runtimes[id] }
func (f fakeRules) HeldBack(id int64) string                         { return f.held[id] }

type fakeRunner struct {
	mu       sync.Mutex
	runs     []run.AuthContext
	subjects []string
	notRun   []string // subject: why
	refused  []string // rule: subject: why
	cutOff   []string // rule: subject
	out      *jsruntime.ExecResult
	err      error
	// started and release, when set, hold Execute until release is closed.
	started, release chan struct{}
}

func (f *fakeRunner) Execute(_ int64, _ *jsruntime.JSExecutorRuntime, auth run.AuthContext, subject string) (*jsruntime.ExecResult, error) {
	if f.release != nil {
		close(f.started)
		<-f.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, auth)
	f.subjects = append(f.subjects, subject)
	return f.out, f.err
}

func (f *fakeRunner) RecordNotRun(_ int64, subject, why string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notRun = append(f.notRun, subject+": "+why)
}

func (f *fakeRunner) RecordCutOff(ruleID, _ int64, subject string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cutOff = append(f.cutOff, fmt.Sprintf("%d: %s", ruleID, subject))
}

func (f *fakeRunner) Refuse(ruleID, _ int64, subject, why string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refused = append(f.refused, fmt.Sprintf("%d: %s: %s", ruleID, subject, why))
}

func ruleFour() fakeRules {
	return fakeRules{
		rules:    map[int64]*aimanager.ExecutionRule{4: {ID: 4, Title: "Retrieve the host assets"}},
		runtimes: map[int64]*jsruntime.JSExecutorRuntime{4: jsruntime.New()},
	}
}

var request = aimanager.ExecutionRequest{ID: 9, ExecutionRuleID: 4, UserID: 52, UserRole: "admin"}

func TestARequestIsRunForItsUserAndAnswered(t *testing.T) {
	reqs := newFakeRequests(request)
	runner := &fakeRunner{out: &jsruntime.ExecResult{Status: "success", Message: "Retrieved 23 host assets"}}
	h := New(reqs, ruleFour(), runner)

	h.OnRequested(executionrulewatcher.RunRequest{ID: 9, ExecutionRuleID: 4, UserID: 52})

	if len(runner.runs) != 1 || runner.runs[0] != (run.AuthContext{UserID: 52, UserRole: "admin"}) {
		t.Fatalf("runs %+v, want one as user 52 with the recorded role", runner.runs)
	}
	got := reqs.reported[9]
	if !reqs.done[9] || !got.Success || got.Message != "Retrieved 23 host assets" {
		t.Errorf("reported %+v (done=%v)", got, reqs.done[9])
	}
}

// Announced twice (the event and a reconnect's pick-up), run once.
func TestARequestRunsOnceEvenWhenHeardTwice(t *testing.T) {
	reqs := newFakeRequests(request)
	runner := &fakeRunner{out: &jsruntime.ExecResult{Status: "noop"}}
	h := New(reqs, ruleFour(), runner)

	h.OnRequested(executionrulewatcher.RunRequest{ID: 9})
	h.PickUpPending()

	if len(runner.runs) != 1 {
		t.Errorf("ran %d times, want once", len(runner.runs))
	}
}

func TestPendingRequestsArePickedUp(t *testing.T) {
	reqs := newFakeRequests(request, aimanager.ExecutionRequest{ID: 10, ExecutionRuleID: 4, UserID: 7, UserRole: "user"})
	runner := &fakeRunner{out: &jsruntime.ExecResult{Status: "success"}}
	New(reqs, ruleFour(), runner).PickUpPending()
	if len(runner.runs) != 2 || len(reqs.reported) != 2 {
		t.Errorf("ran %d, reported %d; want both pending requests", len(runner.runs), len(reqs.reported))
	}
}

func TestARoutineReportingAnErrorIsDoneButNotSuccessful(t *testing.T) {
	reqs := newFakeRequests(request)
	runner := &fakeRunner{out: &jsruntime.ExecResult{Status: "error", Message: "no appliance answered"}}
	New(reqs, ruleFour(), runner).OnRequested(executionrulewatcher.RunRequest{ID: 9})
	got := reqs.reported[9]
	if !reqs.done[9] || got.Success || got.Error != "no appliance answered" {
		t.Errorf("reported %+v (done=%v)", got, reqs.done[9])
	}
}

func TestARuleThatCannotRunIsAnsweredWithTheReason(t *testing.T) {
	for name, rules := range map[string]fakeRules{
		"unknown rule": {rules: map[int64]*aimanager.ExecutionRule{}, runtimes: map[int64]*jsruntime.JSExecutorRuntime{}},
		"no routine":   {rules: map[int64]*aimanager.ExecutionRule{4: {ID: 4, Title: "r"}}, runtimes: map[int64]*jsruntime.JSExecutorRuntime{}},
	} {
		reqs := newFakeRequests(request)
		runner := &fakeRunner{}
		New(reqs, rules, runner).OnRequested(executionrulewatcher.RunRequest{ID: 9})
		got := reqs.reported[9]
		if reqs.done[9] || got.Success || got.Error == "" || len(runner.runs) != 0 {
			t.Errorf("%s: reported %+v (done=%v), %d run(s)", name, got, reqs.done[9], len(runner.runs))
		}
		// Not run, but on record: the audit trail says why.
		if len(runner.notRun) != 1 || !strings.Contains(runner.notRun[0], got.Error) {
			t.Errorf("%s: audited %v, want the reason %q", name, runner.notRun, got.Error)
		}
	}
}

func TestAFailedRunIsReportedAsFailed(t *testing.T) {
	reqs := newFakeRequests(request)
	runner := &fakeRunner{err: errors.New("no executors loaded")}
	New(reqs, ruleFour(), runner).OnRequested(executionrulewatcher.RunRequest{ID: 9})
	if got := reqs.reported[9]; reqs.done[9] || got.Error != "no executors loaded" {
		t.Errorf("reported %+v (done=%v)", got, reqs.done[9])
	}
}

// Started from an action, the run is named by the action and the rule.
func TestARunFromAnActionIsNamedByIt(t *testing.T) {
	id := int64(812)
	fromAction := request
	fromAction.ActionID, fromAction.ActionTitle = &id, "Retrieve the host assets of the office"
	reqs := newFakeRequests(fromAction)
	runner := &fakeRunner{out: &jsruntime.ExecResult{Status: "success"}}
	New(reqs, ruleFour(), runner).OnRequested(executionrulewatcher.RunRequest{ID: 9})
	want := `Action "Retrieve the host assets of the office" (id 812) executed manually by execution rule "Retrieve the host assets" (id 4)`
	if len(runner.subjects) != 1 || runner.subjects[0] != want {
		t.Errorf("subjects %q, want %q", runner.subjects, want)
	}
}

// A rule whose routine predates the platform API is refused with the reason
// that held it back: answered, reported as the rule's last execution and
// audited (Refuse), never run.
func TestARuleWhoseRoutinePredatesTheAPIIsRefused(t *testing.T) {
	why := "execution rule 4: routine 240 predates the platform API, which this version no longer runs — generate the rule anew"
	rules := fakeRules{
		rules:    map[int64]*aimanager.ExecutionRule{4: {ID: 4, Title: "r"}},
		runtimes: map[int64]*jsruntime.JSExecutorRuntime{},
		held:     map[int64]string{4: why},
	}
	reqs := newFakeRequests(request)
	runner := &fakeRunner{}
	New(reqs, rules, runner).OnRequested(executionrulewatcher.RunRequest{ID: 9})
	got := reqs.reported[9]
	if reqs.done[9] || got.Success || got.Error != why || len(runner.runs) != 0 {
		t.Errorf("reported %+v (done=%v), %d run(s)", got, reqs.done[9], len(runner.runs))
	}
	if len(runner.refused) != 1 || !strings.HasPrefix(runner.refused[0], "4: ") || !strings.HasSuffix(runner.refused[0], why) {
		t.Errorf("refused %v", runner.refused)
	}
}
