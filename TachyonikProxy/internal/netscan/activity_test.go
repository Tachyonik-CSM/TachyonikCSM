// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests what the sweep reports about itself while it runs
// (docs/plans/proxy-activity.md): nothing before and after, and during a sweep
// its networks, ports, start and progress.

package netscan

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestActivityWhileASweepRuns(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	host, port := splitHostPort(t, srv.URL)

	s, err := New(Config{Network: host + "/32", Ports: []int{port}, Concurrency: 2, TimeoutSeconds: 10})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a := s.Activity(); a.Scanning || !a.LastFinishedAt.IsZero() {
		t.Fatalf("activity before any sweep: %+v", a)
	}

	done := make(chan struct{})
	before := time.Now()
	go func() { s.ScanOnce(context.Background()); close(done) }()

	var a Activity
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if a = s.Activity(); a.Scanning {
			break
		}
	}
	if !a.Scanning || a.Total != 1 || a.Done != 0 || len(a.Networks) != 1 || len(a.Ports) != 1 || a.StartedAt.Before(before.Add(-time.Second)) || !a.LastFinishedAt.IsZero() {
		t.Fatalf("activity during the sweep: %+v", a)
	}

	close(release)
	<-done
	a = s.Activity()
	if a.Scanning || a.Total != 0 || a.LastFinishedAt.Before(before) {
		t.Fatalf("activity after the sweep: %+v", a)
	}

	// A second sweep moves the finish time on: the platform's cue to sync.
	first := a.LastFinishedAt
	time.Sleep(2 * time.Millisecond)
	s.ScanOnce(context.Background())
	if a := s.Activity(); !a.LastFinishedAt.After(first) {
		t.Fatalf("the finish time did not move: %v then %v", first, a.LastFinishedAt)
	}
}

func TestNoSweepReportsNoActivity(t *testing.T) {
	var s *Scanner
	if a := s.Activity(); a.Scanning {
		t.Fatalf("a nil scanner reports %+v", a)
	}
}
