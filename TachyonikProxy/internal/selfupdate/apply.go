// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package selfupdate

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// ApplyOptions wires together everything Apply needs. The dependency graph
// is wide on purpose so tests can substitute fakes for the parts that
// touch the world (network, service control, health probe).
type ApplyOptions struct {
	Decision *Decision
	Paths    *InstallPaths
	Service  ServiceController
	Health   HealthOptions

	// HTTPClient is used for the artifact download. nil → default.
	HTTPClient *http.Client

	// Now and SleepUntil are seams for tests; nil → real time.
	Now        func() time.Time
	SleepUntil func(t time.Time)
}

// ApplyResult is what Apply returns to the caller for reporting.
type ApplyResult struct {
	NewVersion      string
	PreviousVersion string
	RolledBack      bool
	HealthError     error
}

// Apply runs the full apply state machine: download → stage → swap →
// restart → health probe → success or rollback. It updates the on-disk
// state file at every transition so a crash mid-flight is recoverable.
//
// All decisions about whether the update *should* be applied (signature,
// version monotonicity, replay) belong to Check. Apply trusts Decision —
// it only handles the on-disk and service-state side.
func Apply(ctx context.Context, st *State, opts ApplyOptions) (*ApplyResult, error) {
	if opts.Decision == nil || !opts.Decision.UpdateAvailable {
		return nil, fmt.Errorf("Apply called without an available update")
	}
	if opts.Paths == nil {
		return nil, fmt.Errorf("Apply requires Paths")
	}
	if opts.Service == nil {
		return nil, fmt.Errorf("Apply requires Service controller")
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	newVersion := opts.Decision.Manifest.LatestVersion

	// Sticky rollback: never re-apply a version that previously failed
	// health.
	if st.IsRolledBack(newVersion) {
		return nil, fmt.Errorf("version %s was rolled back previously and will not be auto-applied; "+
			"clear the rollback record explicitly to retry", newVersion)
	}

	currentVersion, _ := opts.Paths.CurrentVersionFromSymlink()
	if currentVersion == newVersion {
		return nil, fmt.Errorf("already on version %s", newVersion)
	}

	// ── Download ───────────────────────────────────────────────────────
	st.Phase = PhaseDownloading
	st.InFlightVersion = newVersion
	st.LastCheck = now()
	if err := SaveState(opts.Paths.StateFile, st); err != nil {
		return nil, fmt.Errorf("save state before download: %w", err)
	}

	if err := os.MkdirAll(opts.Paths.StagingRoot, 0755); err != nil {
		return nil, fmt.Errorf("create staging root: %w", err)
	}
	stageDir := filepath.Join(opts.Paths.StagingRoot, newVersion)
	// Clean any leftover from a previous crashed run; same version dir
	// is reused so we don't accumulate junk.
	_ = os.RemoveAll(stageDir)

	archive, err := DownloadAndVerify(ctx, opts.HTTPClient, opts.Decision.Artifact.URL, stageDir, opts.Decision.Artifact.SHA256)
	if err != nil {
		return nil, recordError(opts.Paths.StateFile, st, fmt.Errorf("download: %w", err))
	}

	// ── Stage ──────────────────────────────────────────────────────────
	st.Phase = PhaseStaging
	if err := SaveState(opts.Paths.StateFile, st); err != nil {
		return nil, fmt.Errorf("save state before staging: %w", err)
	}

	if _, err := ExtractTarGz(archive, stageDir); err != nil {
		return nil, recordError(opts.Paths.StateFile, st, fmt.Errorf("extract: %w", err))
	}

	// Move the version dir into place atomically. We expect the staging
	// root to live on the same filesystem as VersionRoot; both are
	// children of VersionRoot in the standard layout.
	finalDir := opts.Paths.VersionDir(newVersion)
	if err := os.RemoveAll(finalDir); err != nil {
		return nil, recordError(opts.Paths.StateFile, st, fmt.Errorf("clean final dir: %w", err))
	}
	if err := os.Rename(stageDir, finalDir); err != nil {
		return nil, recordError(opts.Paths.StateFile, st, fmt.Errorf("rename staging into place: %w", err))
	}

	// ── Apply ──────────────────────────────────────────────────────────
	st.Phase = PhaseApplying
	if err := SaveState(opts.Paths.StateFile, st); err != nil {
		return nil, fmt.Errorf("save state before applying: %w", err)
	}

	previousVersion := currentVersion
	if err := swapCurrentSymlink(opts.Paths, newVersion); err != nil {
		return nil, recordError(opts.Paths.StateFile, st, fmt.Errorf("swap symlink: %w", err))
	}

	if err := opts.Service.Restart(ctx); err != nil {
		// Restart failed — try to swap back and surface the error. The
		// service may still be running on the old binary because the
		// running process kept its open file handle to the prior inode.
		_ = swapCurrentSymlink(opts.Paths, previousVersion)
		_ = opts.Service.Restart(ctx)
		return nil, recordError(opts.Paths.StateFile, st, fmt.Errorf("service restart: %w", err))
	}

	// ── Verify ─────────────────────────────────────────────────────────
	st.Phase = PhaseVerifying
	if err := SaveState(opts.Paths.StateFile, st); err != nil {
		return nil, fmt.Errorf("save state before verifying: %w", err)
	}

	healthErr := Probe(ctx, opts.Health)
	if healthErr == nil {
		// Success.
		st.Active = newVersion
		st.Previous = previousVersion
		st.LastAppliedPublishedAt = opts.Decision.Manifest.PublishedAt
		st.InFlightVersion = ""
		st.LastError = ""
		st.Phase = PhaseIdle
		if err := SaveState(opts.Paths.StateFile, st); err != nil {
			return nil, fmt.Errorf("save state after success: %w", err)
		}
		return &ApplyResult{
			NewVersion:      newVersion,
			PreviousVersion: previousVersion,
		}, nil
	}

	// ── Rollback ───────────────────────────────────────────────────────
	st.Phase = PhaseRollingBack
	st.LastError = fmt.Sprintf("health probe failed for %s: %v", newVersion, healthErr)
	if err := SaveState(opts.Paths.StateFile, st); err != nil {
		return nil, fmt.Errorf("save state before rollback: %w", err)
	}

	if previousVersion == "" {
		// Nothing to roll back to. We're stuck on the bad version.
		st.Phase = PhaseDegraded
		st.AddRolledBack(newVersion)
		_ = SaveState(opts.Paths.StateFile, st)
		return &ApplyResult{
			NewVersion:  newVersion,
			RolledBack:  false,
			HealthError: healthErr,
		}, fmt.Errorf("health probe failed and no previous version is available; manual intervention required")
	}

	if err := swapCurrentSymlink(opts.Paths, previousVersion); err != nil {
		st.Phase = PhaseDegraded
		_ = SaveState(opts.Paths.StateFile, st)
		return nil, fmt.Errorf("rollback swap failed: %w", err)
	}
	if err := opts.Service.Restart(ctx); err != nil {
		st.Phase = PhaseDegraded
		_ = SaveState(opts.Paths.StateFile, st)
		return nil, fmt.Errorf("rollback restart failed: %w", err)
	}

	rollbackHealthErr := Probe(ctx, opts.Health)
	if rollbackHealthErr != nil {
		st.Phase = PhaseDegraded
		st.LastError = fmt.Sprintf("rollback also unhealthy: %v", rollbackHealthErr)
		st.AddRolledBack(newVersion)
		_ = SaveState(opts.Paths.StateFile, st)
		return &ApplyResult{
			NewVersion:      newVersion,
			PreviousVersion: previousVersion,
			RolledBack:      true,
			HealthError:     healthErr,
		}, fmt.Errorf("update health failed AND rollback health failed: original=%v rollback=%v", healthErr, rollbackHealthErr)
	}

	st.Active = previousVersion
	st.AddRolledBack(newVersion)
	st.InFlightVersion = ""
	st.Phase = PhaseIdle
	if err := SaveState(opts.Paths.StateFile, st); err != nil {
		return nil, fmt.Errorf("save state after rollback: %w", err)
	}
	return &ApplyResult{
		NewVersion:      newVersion,
		PreviousVersion: previousVersion,
		RolledBack:      true,
		HealthError:     healthErr,
	}, nil
}

// swapCurrentSymlink atomically points <VersionRoot>/current at the version
// directory. Implemented as: write a sibling temp symlink, rename. POSIX
// rename of a symlink onto another symlink (same parent dir) is atomic.
func swapCurrentSymlink(p *InstallPaths, version string) error {
	target := p.VersionDir(version)
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("target version dir %s missing: %w", target, err)
	}

	tmp := p.CurrentSymlink + ".swap"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return fmt.Errorf("create swap symlink: %w", err)
	}
	if err := os.Rename(tmp, p.CurrentSymlink); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("atomic rename symlink: %w", err)
	}
	return nil
}

// recordError writes the error to the state file's LastError before
// returning it to the caller. Best-effort: a failure to write the state is
// logged into the returned error chain but does not mask the original.
func recordError(statePath string, st *State, err error) error {
	st.LastError = err.Error()
	if saveErr := SaveState(statePath, st); saveErr != nil {
		return fmt.Errorf("%w (and failed to persist error to state: %v)", err, saveErr)
	}
	return err
}
