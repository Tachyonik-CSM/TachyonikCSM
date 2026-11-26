// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the analysis worker against a stand-in ResourceManager: a source
// announced as waiting is analysed without a sweep, an id queued twice is
// analysed once, a source that is no longer waiting when it is read again is
// skipped, a rule change sweeps at once with the revisit, and a stop ends the
// worker after the source it is on (docs/plans/sourceanalyser-source-events.md).

package analyzer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"tachyonik/sourceanalyser/internal/config"
	"tachyonik/sourceanalyser/internal/resourcemanager"
)

// fakeRM holds sources and records every result written.
type fakeRM struct {
	mu       sync.Mutex
	sources  map[int64]*resourcemanager.Source
	listed   int
	patches  map[int64][]string // id → statuses written
	download chan struct{}      // when set, each file download waits for it
	fileStatus int              // when set, each file download fails with it
}

func newFakeRM(t *testing.T, sources ...resourcemanager.Source) (*fakeRM, *resourcemanager.Client) {
	t.Helper()
	f := &fakeRM{sources: map[int64]*resourcemanager.Source{}, patches: map[int64][]string{}}
	for i := range sources {
		s := sources[i]
		f.sources[s.ID] = &s
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, resourcemanager.NewClient(srv.URL, "key")
}

func (f *fakeRM) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/sources")
	if path == "" && r.Method == "GET" {
		f.mu.Lock()
		f.listed++
		list := []resourcemanager.Source{}
		for _, s := range f.sources {
			list = append(list, *s)
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(resourcemanager.ListSourcesResponse{Sources: list, Total: len(list)})
		return
	}
	rest := strings.TrimPrefix(path, "/")
	file := strings.HasSuffix(rest, "/file")
	id, _ := strconv.ParseInt(strings.TrimSuffix(rest, "/file"), 10, 64)
	f.mu.Lock()
	s, ok := f.sources[id]
	f.mu.Unlock()
	if !ok {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	switch {
	case file:
		if f.download != nil {
			<-f.download
		}
		if f.fileStatus != 0 {
			http.Error(w, `{"error":"file"}`, f.fileStatus)
			return
		}
		_, _ = w.Write([]byte("plain text nobody recognises"))
	case r.Method == "GET":
		f.mu.Lock()
		_ = json.NewEncoder(w).Encode(s)
		f.mu.Unlock()
	case r.Method == "PATCH":
		var u resourcemanager.UpdateSourceRequest
		_ = json.NewDecoder(r.Body).Decode(&u)
		f.mu.Lock()
		s.SourceType, s.Status = u.SourceType, u.Status
		f.patches[id] = append(f.patches[id], u.Status)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(s)
	}
}

func (f *fakeRM) written(id int64) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.patches[id]...)
}

func (f *fakeRM) listings() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listed
}

// startWorker runs the worker with a sweep interval long enough not to fire.
func startWorker(t *testing.T, a *Analyzer) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); a.Run(ctx, time.Hour) }()
	stopped := false
	stop = func() {
		if !stopped {
			stopped = true
			cancel()
			<-done
		}
	}
	t.Cleanup(stop)
	return stop
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func analyzerFor(rm *resourcemanager.Client) *Analyzer {
	return New(rm, &config.Config{Analyzer: config.AnalyzerConfig{MaxContentBytes: 1024, MaxPDFBytes: 1024}})
}

// SA-ANA-01: a source announced as waiting is analysed without a sweep.
func TestAnAnnouncedSourceIsAnalysedWithoutASweep(t *testing.T) {
	f, rm := newFakeRM(t)
	a := analyzerFor(rm)
	startWorker(t, a)
	eventually(t, "the startup sweep", func() bool { return f.listings() == 1 })

	f.mu.Lock()
	f.sources[7] = &resourcemanager.Source{ID: 7, Filename: "notes.txt", Status: "Analysing", UserID: 1}
	f.mu.Unlock()
	a.Enqueue(7)
	eventually(t, "the result", func() bool { return len(f.written(7)) == 1 })
	if got := f.written(7); got[0] != "Analysed" || f.listings() != 1 {
		t.Errorf("written %q after %d listings; want Analysed, without another listing", got, f.listings())
	}
}

// SA-ANA-20: an id queued twice is analysed once; a source no longer waiting
// at the re-read, or gone, is skipped.
func TestTheQueueAnalysesEachSourceOnceAndOnlyWhileItWaits(t *testing.T) {
	f, rm := newFakeRM(t,
		resourcemanager.Source{ID: 1, Filename: "a.txt", Status: "Analysing"},
		resourcemanager.Source{ID: 2, Filename: "b.xml", Status: "Analysed", SourceType: "OpenVAS Report"},
	)
	f.download = make(chan struct{})
	a := analyzerFor(rm)
	// Queued before the worker runs, so the startup sweep finds 1 queued already.
	a.Enqueue(1)
	a.Enqueue(1)
	a.Enqueue(2)  // announced, but analysed meanwhile
	a.Enqueue(99) // announced, but deleted meanwhile
	startWorker(t, a)
	close(f.download)
	eventually(t, "source 1", func() bool { return len(f.written(1)) == 1 })
	time.Sleep(50 * time.Millisecond)
	if got := f.written(1); len(got) != 1 {
		t.Errorf("source 1 written %q, want once", got)
	}
	if got := f.written(2); len(got) != 0 {
		t.Errorf("source 2 written %q, want nothing: it is not waiting", got)
	}
}

// SA-ANA-04, SA-ANA-19: a rule change sweeps at once and revisits Unsupported.
func TestARuleChangeSweepsAtOnceWithTheRevisit(t *testing.T) {
	f, rm := newFakeRM(t, resourcemanager.Source{ID: 3, Filename: "c.bin", Status: "Analysed", SourceType: "Unsupported", AnalyserVersion: ptr("99.0.0")})
	a := analyzerFor(rm)
	startWorker(t, a)
	eventually(t, "the startup sweep", func() bool { return f.listings() == 1 })
	if got := f.written(3); len(got) != 0 {
		t.Fatalf("source 3 written %q before any rule changed", got)
	}
	a.RequestRevisit()
	eventually(t, "the revisit", func() bool { return len(f.written(3)) == 2 })
	if got := f.written(3); got[0] != "Analysing" || got[1] != "Analysed" {
		t.Errorf("source 3 written %q, want Analysing then Analysed", got)
	}
}

// SA-OPS-09: a stop ends the worker after the source it is on; the rest stays
// waiting.
func TestAStopFinishesTheCurrentSourceOnly(t *testing.T) {
	f, rm := newFakeRM(t,
		resourcemanager.Source{ID: 1, Filename: "a.txt", Status: "Analysing"},
		resourcemanager.Source{ID: 2, Filename: "b.txt", Status: "Analysing"},
	)
	f.download = make(chan struct{})
	a := analyzerFor(rm)
	a.Enqueue(1)
	a.Enqueue(2)
	stop := startWorker(t, a)
	time.Sleep(50 * time.Millisecond) // the worker waits in source 1's download
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	time.Sleep(20 * time.Millisecond)
	close(f.download)
	<-stopped
	if len(f.written(1)) != 1 || len(f.written(2)) != 0 {
		t.Errorf("written: source 1 %q, source 2 %q; want source 1 finished and source 2 left", f.written(1), f.written(2))
	}
}

func ptr(s string) *string { return &s }
