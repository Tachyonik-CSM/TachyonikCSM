// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package importer is the core import engine driven by the daemon's poll loop.
// Each pass it lists sources from ResourceManager, picks those in "Analysed",
// runs the matching AI-generated routine over the file, and writes the assets,
// vulnerabilities and detections it yields to AssetManager, moving the source to
// "Imported" or "Import failed".
//
// A second pass re-imports: a source already "Imported" is revisited when the
// routine that produced it has moved on, and records the new run no longer
// produces are deleted as stale rather than left behind. Sources parked in "No
// import routine" are revisited whenever the rules reload, via a flag the daemon
// shares with the rule watcher.
//
// Transient test sources take a dry-run path that counts what a routine would
// produce and writes that summary into the source's import notes without ever
// touching AssetManager.
package importer

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"tachyonik/lib/logger"
	"tachyonik/sourceimporter/internal/aiimporter"
	"tachyonik/sourceimporter/internal/aimanager"
	"tachyonik/sourceimporter/internal/assetmanager"
	"tachyonik/sourceimporter/internal/rscmanager"
)

// AuditEmitter is the minimal interface Importer needs to report audit events
// to SystemManager. The concrete implementation lives in
// tachyonik/sourceimporter/internal/systemmanager.
type AuditEmitter interface {
	CreateAuditEvent(userID int64, level, module, message string) error
}

type Importer struct {
	assetManagerAPI *assetmanager.Client
	rscManagerAPI   *rscmanager.Client
	aiImporter      *aiimporter.AIImporter // nil if AI not configured
	auditEmitter    AuditEmitter
	// revisit is set (by the daemon's rule-reload paths) to request that the
	// next ProcessSources also re-attempt resources parked in "No import
	// routine" — a newly loaded rule may now match them. One-shot per set.
	revisit *atomic.Bool
}

// New builds an Importer.
//
// It deliberately takes no build version. The version stamped on an imported
// source is the RULE's — aiimporter.GetImporterVersion, "AI-<type>-v<version>"
// — because that is what decides whether a source is worth importing again.
// The daemon's own build version used to be passed here and stored unread,
// which suggested a relationship that does not exist.
func New(assetManagerAPI *assetmanager.Client, rscManagerAPI *rscmanager.Client, aiImporter *aiimporter.AIImporter) *Importer {
	return &Importer{
		assetManagerAPI: assetManagerAPI,
		rscManagerAPI:   rscManagerAPI,
		aiImporter:      aiImporter,
	}
}

// SetAuditEmitter wires the SystemManager audit-event client. Optional.
func (i *Importer) SetAuditEmitter(e AuditEmitter) {
	i.auditEmitter = e
}

// SetRevisitFlag wires the shared "rules changed, re-visit stuck resources"
//
// The flag is a shared atomic rather than a method on this type because the
// import-rule watcher's closures are built in main before the Importer exists
// and set it directly. A RequestRevisit method used to sit here for the same
// job and had no callers for exactly that reason.
// signal. The daemon sets it whenever the import rules (re)load.
func (i *Importer) SetRevisitFlag(f *atomic.Bool) {
	i.revisit = f
}

func (i *Importer) emitAudit(userID int64, level, message string) {
	if i.auditEmitter == nil || userID == 0 {
		return
	}
	if err := i.auditEmitter.CreateAuditEvent(userID, level, "SourceImporter", message); err != nil {
		logger.Warnf("audit event emit failed: %v", err)
	}
}

// ProcessSources processes all sources with "Analysed" status
func (i *Importer) ProcessSources() error {
	// Get all sources via API
	allSources, err := i.rscManagerAPI.ListAllSources()
	if err != nil {
		return fmt.Errorf("failed to get sources: %w", err)
	}

	// When the import rules have just (re)loaded, also re-visit resources that
	// were previously parked in "No import routine" — a rule that now exists
	// may match them. The flag is one-shot (Swap), so ordinary polls don't
	// re-scan terminal resources every tick.
	revisit := i.revisit != nil && i.revisit.Swap(false)

	// Filter to sources that need an import attempt.
	var sources []rscmanager.Source
	for _, source := range allSources {
		if source.TestRoutineID != nil {
			// Our own import-routine test sources are dry-run once, while still
			// in their initial "Analysed" state (they become "Tested" after).
			// Test sources for other modules are never imported.
			if source.IsImporterTest() && source.Status == "Analysed" {
				sources = append(sources, source)
			}
			continue
		}
		if source.Status == "Analysed" || (revisit && source.Status == "No import routine") {
			sources = append(sources, source)
		}
	}

	if len(sources) == 0 {
		return nil
	}

	logger.Infof("Processing %d sources for import...", len(sources))
	for _, source := range sources {
		logger.Infof("  Source %d: %s (type: %s, user: %d)", source.ID, source.Filename, source.SourceType, source.UserID)
	}

	for _, source := range sources {
		if err := i.processSource(&source); err != nil {
			logger.Errorf("Error processing source %d: %v", source.ID, err)
			continue
		}
	}

	return nil
}

func (i *Importer) processSource(source *rscmanager.Source) error {
	// Import-routine test source: dry-run the one routine and write the result
	// summary back — never persist to AssetManager.
	if source.IsImporterTest() {
		return i.processTestSource(source)
	}

	if i.aiImporter != nil {
		if rule := i.aiImporter.FindMatchingRule(source.SourceType); rule != nil {
			if i.aiImporter.HasExecutor(rule.ID) {
				return i.processSourceWithAI(source, rule)
			}
			logger.Infof("No code generated yet for rule %d (%s)", rule.ID, rule.Type)
		}
	}

	// No matching rule or no executor available. If this resource was already
	// parked in "No import routine" (a re-visit after a rule change that still
	// didn't match), leave it quietly — only the first transition merits an
	// audit event and a status write.
	if source.Status == "No import routine" {
		logger.Debugf("Source %d still has no import routine for type: %s", source.ID, source.SourceType)
		return nil
	}
	logger.Infof("Source %d has no import routine for type: %s", source.ID, source.SourceType)
	i.emitAudit(source.UserID, "Warning", fmt.Sprintf(
		"Resource %q (id %d): format %q is not supported (no import routine)",
		source.Filename, source.ID, source.SourceType,
	))
	if _, err := i.rscManagerAPI.UpdateSource(source.ID, source.SourceType, "No import routine", nil, nil); err != nil {
		return fmt.Errorf("failed to update status to No import routine: %w", err)
	}
	return nil
}

// processSourceWithAI handles import using AI-generated JS code
func (i *Importer) processSourceWithAI(source *rscmanager.Source, rule *aimanager.ImportRule) error {
	importerVersion := i.aiImporter.GetImporterVersion(rule)

	// Set status to "Importing"
	logger.Infof("Starting AI import for source %d (%s) using %s", source.ID, source.SourceType, importerVersion)
	if _, err := i.rscManagerAPI.UpdateSource(source.ID, source.SourceType, "Importing", nil, &importerVersion); err != nil {
		return fmt.Errorf("failed to update status to Importing: %w", err)
	}

	// Execute AI import
	importNotes, err := i.aiImporter.ImportSource(source)

	if err != nil {
		logger.Errorf("AI import failed for source %d: %v", source.ID, err)
		i.emitAudit(source.UserID, "Warning", fmt.Sprintf(
			"Import failed for resource %q (id %d): %v", source.Filename, source.ID, err,
		))
		if _, updateErr := i.rscManagerAPI.UpdateSource(source.ID, source.SourceType, "Import failed", nil, &importerVersion); updateErr != nil {
			return fmt.Errorf("failed to update status to Import failed: %w", updateErr)
		}
		return err
	}

	logger.Infof("AI import successful for source %d: %s", source.ID, importNotes)
	i.emitAudit(source.UserID, "Info", fmt.Sprintf(
		"Resource %q (id %d) imported: %s", source.Filename, source.ID, importNotes,
	))
	if _, err := i.rscManagerAPI.UpdateSource(source.ID, source.SourceType, "Imported", &importNotes, &importerVersion); err != nil {
		return fmt.Errorf("failed to update status to Imported: %w", err)
	}

	return nil
}

// processTestSource dry-runs a single import routine against a transient test
// source and writes the {assets, vulnerabilities, detections, assetTypes}
// summary back into the source's import notes (status "Tested"), or an error
// message (status "Test failed"). It never writes to AssetManager and emits no
// audit events. The WebUI reads the summary and then deletes the test source.
func (i *Importer) processTestSource(source *rscmanager.Source) error {
	if i.aiImporter == nil {
		notes := "AI importer unavailable"
		if _, err := i.rscManagerAPI.UpdateSource(source.ID, source.SourceType, "Test failed", &notes, nil); err != nil {
			return fmt.Errorf("failed to update test status: %w", err)
		}
		return nil
	}

	summary, err := i.aiImporter.RunImportTest(source, *source.TestRoutineID)
	if err != nil {
		logger.Errorf("Import test routine %d failed for source %d: %v", *source.TestRoutineID, source.ID, err)
		notes := err.Error()
		if _, uerr := i.rscManagerAPI.UpdateSource(source.ID, source.SourceType, "Test failed", &notes, nil); uerr != nil {
			return fmt.Errorf("failed to update test status: %w", uerr)
		}
		return nil
	}

	payload, merr := json.Marshal(summary)
	if merr != nil {
		notes := "failed to encode test result"
		if _, uerr := i.rscManagerAPI.UpdateSource(source.ID, source.SourceType, "Test failed", &notes, nil); uerr != nil {
			return fmt.Errorf("failed to update test status: %w", uerr)
		}
		return nil
	}

	notes := string(payload)
	logger.Infof("Import test routine %d for source %d: %s", *source.TestRoutineID, source.ID, notes)
	if _, err := i.rscManagerAPI.UpdateSource(source.ID, source.SourceType, "Tested", &notes, nil); err != nil {
		return fmt.Errorf("failed to update test status: %w", err)
	}
	return nil
}

// getImporterVersion returns the current importer version for a given source type.
func (i *Importer) getImporterVersion(sourceType string) string {
	if i.aiImporter != nil {
		if rule := i.aiImporter.FindMatchingRule(sourceType); rule != nil {
			if i.aiImporter.HasExecutor(rule.ID) {
				return i.aiImporter.GetImporterVersion(rule)
			}
		}
	}
	return ""
}

// ProcessReImports checks for sources with status "Imported" that need re-importing
// due to a newer importer version being available
func (i *Importer) ProcessReImports() error {
	// Get all sources via API
	allSources, err := i.rscManagerAPI.ListAllSources()
	if err != nil {
		return fmt.Errorf("failed to get sources: %w", err)
	}

	// Filter sources with "Imported" status that have an outdated importer version
	var sourcesToReImport []rscmanager.Source
	for _, source := range allSources {
		// Test sources are never (re-)imported.
		if source.TestRoutineID != nil {
			continue
		}
		if source.Status != "Imported" {
			continue
		}

		// Get the current importer version for this source type
		currentVersion := i.getImporterVersion(source.SourceType)
		if currentVersion == "" {
			continue
		}

		// Check if the source was imported with an older version
		if source.ImporterVersion == nil || *source.ImporterVersion != currentVersion {
			sourcesToReImport = append(sourcesToReImport, source)
		}
	}

	if len(sourcesToReImport) == 0 {
		return nil
	}

	logger.Infof("Found %d sources that need re-importing due to newer importer versions...", len(sourcesToReImport))

	for _, source := range sourcesToReImport {
		if err := i.reImportSource(&source); err != nil {
			logger.Errorf("Error re-importing source %d: %v", source.ID, err)
			continue
		}
	}

	return nil
}

// reImportSource performs a re-import of a source, updating existing records and cleaning up stale ones
func (i *Importer) reImportSource(source *rscmanager.Source) error {
	// Record the start time for stale record cleanup
	reImportStartTime := time.Now()

	// Get the current importer version
	importerVersion := i.getImporterVersion(source.SourceType)
	oldVersion := "unknown"
	if source.ImporterVersion != nil {
		oldVersion = *source.ImporterVersion
	}

	logger.Infof("Starting re-import for source %d (%s): upgrading from %s to %s",
		source.ID, source.SourceType, oldVersion, importerVersion)

	// Set status to "Re-importing"
	if _, err := i.rscManagerAPI.UpdateSource(source.ID, source.SourceType, "Re-importing", nil, &importerVersion); err != nil {
		return fmt.Errorf("failed to update status to Re-importing: %w", err)
	}

	// Execute import via AI routine
	var importErr error
	var importNotes string

	if i.aiImporter != nil {
		if rule := i.aiImporter.FindMatchingRule(source.SourceType); rule != nil {
			if i.aiImporter.HasExecutor(rule.ID) {
				importNotes, importErr = i.aiImporter.ImportSource(source)
			} else {
				importErr = fmt.Errorf("no code generated for rule %d (%s)", rule.ID, rule.Type)
			}
		} else {
			importErr = fmt.Errorf("no import rule for type: %s", source.SourceType)
		}
	} else {
		importErr = fmt.Errorf("AI importer not available")
	}
	// If import failed, update status and return
	if importErr != nil {
		logger.Errorf("Re-import failed for source %d: %v", source.ID, importErr)
		if _, err := i.rscManagerAPI.UpdateSource(source.ID, source.SourceType, "Re-import failed", nil, &importerVersion); err != nil {
			return fmt.Errorf("failed to update status to Re-import failed: %w", err)
		}
		return importErr
	}

	// Clean up stale records (those not updated during re-import)
	sourceRef := fmt.Sprintf("%s (ID: %d)", source.Filename, source.ID)

	staleAssets, err := i.assetManagerAPI.DeleteStaleAssets(sourceRef, reImportStartTime)
	if err != nil {
		logger.Warnf("Failed to delete stale assets for source %d: %v", source.ID, err)
	} else if staleAssets > 0 {
		logger.Infof("Deleted %d stale assets for source %d", staleAssets, source.ID)
	}

	staleVulns, err := i.assetManagerAPI.DeleteStaleVulnerabilities(sourceRef, reImportStartTime)
	if err != nil {
		logger.Warnf("Failed to delete stale vulnerabilities for source %d: %v", source.ID, err)
	} else if staleVulns > 0 {
		logger.Infof("Deleted %d stale vulnerabilities for source %d", staleVulns, source.ID)
	}

	staleDetections, err := i.assetManagerAPI.DeleteStaleDetections(sourceRef, reImportStartTime)
	if err != nil {
		logger.Warnf("Failed to delete stale detections for source %d: %v", source.ID, err)
	} else if staleDetections > 0 {
		logger.Infof("Deleted %d stale detections for source %d", staleDetections, source.ID)
	}

	// Update import notes with cleanup info
	if staleAssets > 0 || staleVulns > 0 || staleDetections > 0 {
		importNotes = fmt.Sprintf("%s; Cleaned up: %d assets, %d vulns, %d detections",
			importNotes, staleAssets, staleVulns, staleDetections)
	}

	// Update status to "Imported" with new version
	logger.Infof("Re-import successful for source %d: %s", source.ID, importNotes)
	if _, err := i.rscManagerAPI.UpdateSource(source.ID, source.SourceType, "Imported", &importNotes, &importerVersion); err != nil {
		return fmt.Errorf("failed to update status to Imported: %w", err)
	}

	return nil
}
