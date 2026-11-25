// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package analyzer is the core analysis engine. Sources reach it two ways: by
// id, when ResourceManager announces one as "Analysing" (Enqueue), and by the
// sweep, which goes through the whole source list at startup, on every
// reconnect to ResourceManager, after the rules changed, and every
// sweep_interval_seconds (SA-ANA-19). The sweep selects what needs work
// (status "Analysing", plus re-analysis when the analyser version advanced or
// a rule change asked for a revisit of "Unsupported" resources) and queues it.
//
// One worker (Run) analyses the queue one source after the other. It reads
// each source again first and analyses it only while it is still "Analysing"
// (SA-ANA-20), downloads the leading bytes of its file, runs it through the
// AIAnalyser's rule routines, writes the identified type/status back to
// ResourceManager and emits audit events to SystemManager. A stop ends the
// worker after the source it is on (SA-OPS-09).
package analyzer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"tachyonik/lib/logger"
	"tachyonik/lib/textextract"
	"tachyonik/sourceanalyser/internal/aianalyser"
	"tachyonik/sourceanalyser/internal/config"
	"tachyonik/sourceanalyser/internal/jsruntime"
	"tachyonik/sourceanalyser/internal/resourcemanager"
	"tachyonik/sourceanalyser/internal/version"
)

// AuditEmitter is the minimal interface Analyzer needs to report audit events
// to SystemManager. The daemon satisfies it with the shared client from
// tachyonik/lib/systemmanager.
type AuditEmitter interface {
	CreateAuditEvent(userID int64, level, module, message string) error
}

type Analyzer struct {
	rscMgrClient *resourcemanager.Client
	config       *config.Config
	aiAnalyser   *aianalyser.AIAnalyser
	auditEmitter AuditEmitter
	// revisit is set (by the daemon's rule-reload paths) to request that the
	// next sweep also re-analyses resources parked in "Unsupported"
	// regardless of analyser version — a newly loaded rule may now match them.
	revisit atomic.Bool

	// The queue: ids waiting for the worker, each once (SA-ANA-20). An id is
	// taken off before it is analysed, so a message about it arriving
	// meanwhile queues it again, and the re-read decides.
	mu      sync.Mutex
	queue   []int64
	queued  map[int64]bool
	wake    chan struct{} // something was queued
	sweepCh chan struct{} // a sweep was asked for
}

func New(rscMgrClient *resourcemanager.Client, cfg *config.Config) *Analyzer {
	return &Analyzer{
		rscMgrClient: rscMgrClient,
		config:       cfg,
		queued:       make(map[int64]bool),
		wake:         make(chan struct{}, 1),
		sweepCh:      make(chan struct{}, 1),
	}
}

// RequestRevisit asks for a sweep now that also re-analyses resources
// currently marked Unsupported (SA-ANA-04). Safe to call from any goroutine.
func (a *Analyzer) RequestRevisit() {
	a.revisit.Store(true)
	a.RequestSweep()
}

// RequestSweep asks the worker to go through the whole source list. Requests
// made while one is pending are one sweep. Safe to call from any goroutine.
func (a *Analyzer) RequestSweep() {
	select {
	case a.sweepCh <- struct{}{}:
	default:
	}
}

// Enqueue queues a source for analysis, unless it is queued already. Safe to
// call from any goroutine; it does not block.
func (a *Analyzer) Enqueue(id int64) {
	a.mu.Lock()
	if a.queued[id] {
		a.mu.Unlock()
		return
	}
	a.queued[id] = true
	a.queue = append(a.queue, id)
	a.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// next takes the first queued id off the queue.
func (a *Analyzer) next() (int64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.queue) == 0 {
		return 0, false
	}
	id := a.queue[0]
	a.queue = a.queue[1:]
	delete(a.queued, id)
	return id, true
}

// Run is the worker: it sweeps once, then analyses what is queued and sweeps
// when asked and every sweepInterval, until ctx ends. A stop is noticed
// between two sources, so the source being analysed is finished first
// (SA-OPS-09); what is still queued stays "Analysing" for the next start.
func (a *Analyzer) Run(ctx context.Context, sweepInterval time.Duration) {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	a.RequestSweep()
	for {
		// A stop and a sweep come before the next source.
		select {
		case <-ctx.Done():
			return
		case <-a.sweepCh:
			a.sweep()
			continue
		case <-ticker.C:
			a.sweep()
			continue
		default:
		}
		if id, ok := a.next(); ok {
			a.processQueued(id)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-a.sweepCh:
			a.sweep()
		case <-ticker.C:
			a.sweep()
		case <-a.wake:
		}
	}
}

func (a *Analyzer) sweep() {
	if err := a.Sweep(); err != nil {
		logger.Errorf("Error going through the sources: %v", err)
	}
}

// processQueued reads a queued source again and analyses it while it is still
// waiting (SA-ANA-20): one deleted, analysed meanwhile, or another module's
// test source is skipped.
func (a *Analyzer) processQueued(id int64) {
	source, err := a.rscMgrClient.GetSource(id)
	if errors.Is(err, resourcemanager.ErrNotFound) {
		logger.Debugf("Source %d is gone; not analysed", id)
		return
	}
	if err != nil {
		// Still "Analysing": the next sweep finds it.
		logger.Errorf("Failed to read source %d, leaving it for the next sweep: %v", id, err)
		return
	}
	if source.Status != "Analysing" {
		logger.Debugf("Source %d is %q now; not analysed", id, source.Status)
		return
	}
	if source.TestRoutineID != nil && !source.IsAnalyserTest() {
		return
	}
	if err := a.processSource(source); err != nil {
		logger.Errorf("Error processing source %d: %v", source.ID, err)
	}
}

// SetAIAnalyser sets the AI analyser for per-rule analysis
func (a *Analyzer) SetAIAnalyser(analyser *aianalyser.AIAnalyser) {
	a.aiAnalyser = analyser
}

// SetAuditEmitter wires the SystemManager audit-event client. Optional: if
// nil, audit emission is silently skipped.
func (a *Analyzer) SetAuditEmitter(e AuditEmitter) {
	a.auditEmitter = e
}

func (a *Analyzer) emitAudit(userID int64, level, message string) {
	if a.auditEmitter == nil || userID == 0 {
		return
	}
	if err := a.auditEmitter.CreateAuditEvent(userID, level, "SourceAnalyser", message); err != nil {
		logger.Warnf("audit event emit failed: %v", err)
	}
}

// updateSource updates a source via the ResourceManager API
// StatusAnalysed is the status of a source whose analysis is done, recognised
// or Unsupported; SourceImporter imports only these (SA-IF-05).
const StatusAnalysed = "Analysed"

func (a *Analyzer) updateSource(id int64, sourceType, status string) error {
	// Include analyser version only when status is "Analysed"
	var analyserVersion *string
	if status == "Analysed" {
		v := version.Version
		analyserVersion = &v
	}

	// Update via API (triggers WebSocket broadcast)
	err := a.rscMgrClient.UpdateSource(id, sourceType, status, analyserVersion)
	if err != nil {
		return fmt.Errorf("failed to update source %d: %w", id, err)
	}

	if analyserVersion != nil {
		logger.Infof("Updated source %d via API: type=%s, status=%s, version=%s", id, sourceType, status, *analyserVersion)
	} else {
		logger.Infof("Updated source %d via API: type=%s, status=%s", id, sourceType, status)
	}

	return nil
}

// Sweep goes through every source and queues those that need analysis
// (SA-ANA-19):
// 1. Entries with status "Analysing"
// 2. Entries that need re-analysis (Analysed + Unsupported + old/empty version,
// or any Unsupported after a rule change), set to "Analysing" first.
func (a *Analyzer) Sweep() error {
	// Get all sources via API
	entries, err := a.rscMgrClient.ListAllSources()
	if err != nil {
		return fmt.Errorf("failed to get entries: %w", err)
	}

	if len(entries) == 0 {
		return nil
	}

	// When the analysis rules have just (re)loaded, also re-analyse resources
	// parked in "Unsupported" regardless of analyser version — a rule that now
	// exists may identify them. One-shot (Swap), so ordinary polls don't
	// re-scan terminal resources every tick.
	revisit := a.revisit.Swap(false)

	// Filter entries: keep "Analysing" and those that need re-analysis
	var toProcess []resourcemanager.Source
	for _, source := range entries {
		// A test source belonging to another module (e.g. a SourceImporter
		// routine test) is never ours to classify — leave it untouched.
		if source.TestRoutineID != nil && !source.IsAnalyserTest() {
			continue
		}

		// Get analyser version as string
		analyserVer := ""
		if source.AnalyserVersion != nil {
			analyserVer = *source.AnalyserVersion
		}

		// Re-analyse when the analyser binary advanced (version-based) or when
		// the rules just changed and this resource is still typed "Unsupported".
		// Match Unsupported in EITHER terminal status: "Analysed" (SourceAnalyser's
		// own resting state) OR "No import routine" (SourceImporter moves it there,
		// since "Unsupported" has no import rule) — otherwise a rule change would
		// never revisit resources that already passed through the importer.
		revisitUnsupported := revisit && source.SourceType == "Unsupported" &&
			(source.Status == "Analysed" || source.Status == "No import routine")
		needReanalysis := version.NeedsReanalysis(source.Status, source.SourceType, analyserVer) ||
			revisitUnsupported

		if needReanalysis {
			logger.Infof("Re-analyzing source %d (%s) - previous version: %s, current version: %s",
				source.ID, source.Filename, analyserVer, version.Version)
			// Set status to "Analysing" to mark it as being processed
			if err := a.updateSource(source.ID, source.SourceType, "Analysing"); err != nil {
				logger.Errorf("Failed to update status for re-analysis: %v", err)
				continue
			}
			source.Status = "Analysing" // Update local copy
			toProcess = append(toProcess, source)
		} else if source.Status == "Analysing" {
			toProcess = append(toProcess, source)
		}
	}

	if len(toProcess) == 0 {
		return nil
	}

	logger.Infof("Queueing %d sources for analysis", len(toProcess))
	for _, source := range toProcess {
		a.Enqueue(source.ID)
	}
	return nil
}

func (a *Analyzer) processSource(source *resourcemanager.Source) error {
	// Download enough to identify the source. For a non-PDF this window is also
	// what routines match against; for a PDF it is only a type sniff — the full
	// file is re-fetched below so its text can be extracted.
	maxContent := a.config.Analyzer.MaxContentBytes
	if maxContent <= 0 {
		maxContent = 64 * 1024
	}
	raw, _, err := a.rscMgrClient.DownloadSourceFile(source.ID, maxContent)
	if err != nil {
		logger.Errorf("Failed to download file for source %d: %v", source.ID, err)
		a.emitAudit(source.UserID, "Warning", fmt.Sprintf(
			"Failed to identify resource %q (id %d): file not found", source.Filename, source.ID,
		))
		return a.updateSource(source.ID, "Error", "File Not Found")
	}

	// A PDF's text lives in compressed streams, so a prefix is not enough. When
	// we sniff a PDF that filled the content window (i.e. may be truncated),
	// re-download the whole file (capped) before extracting.
	if textextract.DetectMIME(raw) == textextract.MIMEPDF && int64(len(raw)) >= maxContent {
		if full, _, derr := a.rscMgrClient.DownloadSourceFile(source.ID, a.config.Analyzer.MaxPDFBytes); derr != nil {
			logger.Errorf("Failed to download full PDF for source %d: %v", source.ID, derr)
		} else {
			raw = full
		}
	}

	// Convert to the text routines match against (extracted text for PDFs, raw
	// bytes otherwise) and the detected media type.
	content, mimeType := textextract.Analyze(raw)

	// Build JS rule context
	ctx := jsruntime.RuleContext{
		Filename: source.Filename,
		Content:  content,
		FileSize: source.FileSize,
		MimeType: mimeType,
	}

	// Test source: classify with ONLY the hinted routine, bypassing the active
	// set. The WebUI created this transient source to test one specific routine;
	// SourceImporter skips it, so the result is captured and the source deleted.
	if source.IsAnalyserTest() {
		return a.processTestSource(source, ctx, *source.TestRoutineID)
	}

	// Try AI analyser if set
	if a.aiAnalyser != nil && a.aiAnalyser.RuleCount() > 0 {
		result, err := a.aiAnalyser.AnalyzeSource(ctx)
		if err != nil {
			logger.Errorf("AI analyser error for source %d: %v", source.ID, err)
		}

		if result != nil {
			logger.Infof("Rule '%s' matched source %d (%s): type=%s",
				result.RuleName, source.ID, source.Filename, result.SourceType)
			a.emitAudit(source.UserID, "Info", fmt.Sprintf(
				"Resource %q (id %d) analyzed: format %q", source.Filename, source.ID, result.SourceType,
			))
			// A recognised source is analysed: the status is ours to write,
			// not the routine's (SA-IF-05).
			return a.updateSource(source.ID, result.SourceType, StatusAnalysed)
		}
	}

	// No match — mark as Unsupported. If it was already Unsupported (a re-visit
	// after a rule change that still didn't match), stay quiet: only the first
	// time a resource becomes Unsupported is worth an audit event.
	if source.SourceType == "Unsupported" {
		logger.Infof("Source %d (%s) still Unsupported after re-analysis", source.ID, source.Filename)
		return a.updateSource(source.ID, "Unsupported", "Analysed")
	}
	logger.Infof("No rule matched for source: %s (ID: %d), marking as Unsupported", source.Filename, source.ID)
	a.emitAudit(source.UserID, "Warning", fmt.Sprintf(
		"Failed to identify resource %q (id %d): no matching analysis rule (Unsupported)", source.Filename, source.ID,
	))
	return a.updateSource(source.ID, "Unsupported", "Analysed")
}

// processTestSource classifies a transient test source with a single routine
// and writes back the outcome — its type with Analysed on a match, or
// Unsupported/Analysed on no match — which the WebUI reads via SOURCE_UPDATED.
// No audit events are emitted: test runs must not clutter the audit trail.
func (a *Analyzer) processTestSource(source *resourcemanager.Source, ctx jsruntime.RuleContext, routineID int64) error {
	if a.aiAnalyser == nil {
		logger.Errorf("Cannot run test routine %d for source %d: AI analyser unavailable", routineID, source.ID)
		return a.updateSource(source.ID, "Error", "Test failed")
	}

	result, err := a.aiAnalyser.AnalyzeSourceWithRoutine(ctx, routineID)
	if err != nil {
		logger.Errorf("Test routine %d failed for source %d: %v", routineID, source.ID, err)
		return a.updateSource(source.ID, "Error", "Test failed")
	}

	if result != nil {
		logger.Infof("Test routine %d matched source %d (%s): type=%s",
			routineID, source.ID, source.Filename, result.SourceType)
		return a.updateSource(source.ID, result.SourceType, StatusAnalysed) // SA-IF-05
	}

	logger.Infof("Test routine %d did not match source %d (%s)", routineID, source.ID, source.Filename)
	return a.updateSource(source.ID, "Unsupported", "Analysed")
}
