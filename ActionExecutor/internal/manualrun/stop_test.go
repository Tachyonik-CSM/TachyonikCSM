// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins how a stop treats manual runs: a request arriving after it is left
// pending, for the next start to pick up; a run in progress finishes and is
// answered; one still running when the wait is up is reported failed and
// audited as cut off, instead of waiting for AIManager to give up on it.

package manualrun

import (
	"testing"
	"time"

	"tachyonik/actionexecutor/internal/executionrulewatcher"
	"tachyonik/actionexecutor/internal/jsruntime"
	"tachyonik/actionexecutor/internal/run"
	"tachyonik/lib/drain"
)

func TestARequestAfterTheStopIsLeftPending(t *testing.T) {
	reqs := newFakeRequests(request)
	runner := &fakeRunner{out: &jsruntime.ExecResult{Status: "success"}}
	h := New(reqs, ruleFour(), runner)
	var gate drain.Gate
	h.SetGate(&gate)
	gate.Stop()

	h.OnRequested(executionrulewatcher.RunRequest{ID: 9})
	if reqs.claimed[9] || len(runner.runs) != 0 {
		t.Errorf("claimed %v, runs %d: a request after the stop must stay pending", reqs.claimed[9], len(runner.runs))
	}
}

func TestARunInProgressFinishesOrIsReportedCutOff(t *testing.T) {
	for _, c := range []struct {
		name    string
		release bool // the run finishes within the wait
	}{{"finishes", true}, {"cut off", false}} {
		t.Run(c.name, func(t *testing.T) {
			reqs := newFakeRequests(request)
			runner := &fakeRunner{out: &jsruntime.ExecResult{Status: "success", Message: "ok"},
				started: make(chan struct{}), release: make(chan struct{})}
			h := New(reqs, ruleFour(), runner)
			var gate drain.Gate
			h.SetGate(&gate)
			done := make(chan struct{})
			go func() { h.OnRequested(executionrulewatcher.RunRequest{ID: 9}); close(done) }()
			<-runner.started
			gate.Stop()
			if c.release {
				close(runner.release)
			}
			names := gate.Wait(50 * time.Millisecond)
			if c.release {
				<-done
				if names != nil || !reqs.done[9] || reqs.reported[9].Message != "ok" {
					t.Errorf("names %v, reported %+v", names, reqs.reported[9])
				}
				return
			}
			if len(names) != 1 || reqs.done[9] || reqs.reported[9].Error != run.CutOffReason || len(runner.cutOff) != 1 {
				t.Errorf("names %v, reported %+v (done %v), cut off %v", names, reqs.reported[9], reqs.done[9], runner.cutOff)
			}
			close(runner.release)
			<-done
		})
	}
}
