// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package selfupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Phase is a high-level state-machine label written to the state file before
// each transition. On a clean run it cycles back to PhaseIdle. Any other
// phase observed at startup means the previous run crashed mid-flight.
type Phase string

const (
	PhaseIdle        Phase = "idle"
	PhaseChecking    Phase = "checking"
	PhaseDownloading Phase = "downloading"
	PhaseStaging     Phase = "staging"
	PhaseApplying    Phase = "applying"
	PhaseVerifying   Phase = "verifying"
	PhaseRollingBack Phase = "rolling-back"
	PhaseDegraded    Phase = "degraded"
)

// State is the persistent on-disk view of the auto-updater's recent
// activity. It is metadata only; it cannot bypass signature/SHA checks.
type State struct {
	// Active is the version the current symlink should point at after a
	// successful run. Updated only after health verification passes.
	Active string `json:"active,omitempty"`

	// Previous is the version that current pointed at before Active. Used
	// for rollback. Cleared after the post-rollback grace period.
	Previous string `json:"previous,omitempty"`

	// LastAppliedPublishedAt is the manifest publishedAt timestamp that
	// produced Active. Drives replay protection.
	LastAppliedPublishedAt time.Time `json:"lastAppliedPublishedAt,omitempty"`

	// LastCheck is when Check was last run, irrespective of outcome.
	LastCheck time.Time `json:"lastCheck,omitempty"`

	// LastError is a human-readable note from the most recent failure.
	// Cleared on a successful run.
	LastError string `json:"lastError,omitempty"`

	// Phase is the state-machine label written before each transition.
	// On a clean exit it is PhaseIdle. Anything else at startup means
	// the previous run crashed mid-flight.
	Phase Phase `json:"phase"`

	// InFlightVersion is the version a crashed run was trying to reach.
	// Used by crash recovery to either resume or roll back.
	InFlightVersion string `json:"inFlightVersion,omitempty"`

	// RolledBack is the list of versions whose health check failed and
	// were rolled back. Sticky: the updater will refuse to auto-apply any
	// version on this list, even if the manifest still points at it.
	RolledBack []string `json:"rolledBack,omitempty"`
}

// IsRolledBack reports whether v has been rolled back before.
func (s *State) IsRolledBack(v string) bool {
	for _, r := range s.RolledBack {
		if r == v {
			return true
		}
	}
	return false
}

// AddRolledBack appends v to the rolled-back list if not already present.
func (s *State) AddRolledBack(v string) {
	if !s.IsRolledBack(v) && v != "" {
		s.RolledBack = append(s.RolledBack, v)
	}
}

// LoadState reads the state file. A missing file is not an error — it
// returns a fresh State{Phase: PhaseIdle} so first-run behaviour is the
// same as the on-disk-state-says-idle case.
func LoadState(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &State{Phase: PhaseIdle}, nil
		}
		return nil, fmt.Errorf("read state file: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		// A corrupt state file is recoverable: the worst it can do is
		// force an unnecessary re-check or skip a known-bad warning.
		// Refuse to honour it so a crash-corrupted file doesn't pin the
		// updater into a bad state.
		return nil, fmt.Errorf("parse state file: %w", err)
	}
	if s.Phase == "" {
		s.Phase = PhaseIdle
	}
	return &s, nil
}

// SaveState writes the state file atomically: write to a sibling temp file,
// fsync, rename. A crash mid-write leaves either the old or the new file,
// never a half-written one.
func SaveState(path string, s *State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-state.*.tmp")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write temp state file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("sync temp state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp state file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename state file: %w", err)
	}
	return nil
}
