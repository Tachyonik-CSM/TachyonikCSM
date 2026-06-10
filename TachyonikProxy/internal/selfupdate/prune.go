// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PruneResult summarises what a prune call kept and removed. Returned
// instead of logged-from-here so callers control the output format and
// tests can assert exact behaviour.
type PruneResult struct {
	Kept           []string // version directory names that survived
	Removed        []string // version directory names that were removed
	StagingRemoved []string // .staging/<v> entries cleared
}

// PruneOldVersions removes obsolete version directories under
// VersionRoot to bound disk usage.
//
// Keep-set:
//   - the version pointed at by the `current` symlink
//   - state.Active and state.Previous
//   - every entry in state.RolledBack[]
//   - the most-recently-modified `keepVersions` directories on top of the
//     above (steady state with no rollbacks: 1 active + 1 previous +
//     (keepVersions - 2) older historical, capped at keepVersions total)
//
// Defence in depth: only directories that contain a `tachyonikproxy`
// binary are eligible for removal. A stray directory an operator placed
// under VersionRoot will never be touched.
//
// Also sweeps everything under <VersionRoot>/.staging/ — the file lock
// guarantees no concurrent apply, so anything there is a crash artifact.
//
// keepVersions semantics:
//   - keepVersions <= 0 means "keep only the safety overrides".
//   - The cap is a *minimum* — the safety overrides are always kept even
//     if they would push the on-disk count above keepVersions. The intent
//     of the cap is to prevent unbounded growth, not to evict things the
//     updater needs for correctness.
func PruneOldVersions(paths *InstallPaths, state *State, keepVersions int) (*PruneResult, error) {
	if paths == nil {
		return nil, fmt.Errorf("PruneOldVersions: nil paths")
	}
	if state == nil {
		state = &State{}
	}

	// Build the always-keep set.
	keep := map[string]bool{}
	if state.Active != "" {
		keep[state.Active] = true
	}
	if state.Previous != "" {
		keep[state.Previous] = true
	}
	for _, v := range state.RolledBack {
		keep[v] = true
	}
	if cur, err := paths.CurrentVersionFromSymlink(); err == nil && cur != "" {
		keep[cur] = true
	}

	// Enumerate candidate version directories.
	entries, err := os.ReadDir(paths.VersionRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return &PruneResult{}, nil // nothing to prune
		}
		return nil, fmt.Errorf("read version root: %w", err)
	}

	type candidate struct {
		name  string
		mtime time.Time
	}
	var candidates []candidate
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() {
			continue
		}
		if name == "current" || strings.HasPrefix(name, ".") {
			continue
		}
		// Defence in depth: refuse to touch anything that doesn't look
		// like our own version directory.
		if _, err := os.Stat(filepath.Join(paths.VersionRoot, name, "tachyonikproxy")); err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{name: name, mtime: info.ModTime()})
	}

	// Sort newest-first by mtime.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].mtime.After(candidates[j].mtime)
	})

	// Add the top keepVersions to the keep set.
	if keepVersions < 0 {
		keepVersions = 0
	}
	for i := 0; i < keepVersions && i < len(candidates); i++ {
		keep[candidates[i].name] = true
	}

	res := &PruneResult{}
	for _, c := range candidates {
		if keep[c.name] {
			res.Kept = append(res.Kept, c.name)
			continue
		}
		path := filepath.Join(paths.VersionRoot, c.name)
		if err := os.RemoveAll(path); err != nil {
			// Best-effort: skip on failure, do not abort the prune.
			// The next run will retry.
			continue
		}
		res.Removed = append(res.Removed, c.name)
	}

	// Sweep stale staging dirs. The lock guarantees no concurrent apply,
	// so any subdirectory under .staging/ is a crash artifact.
	if stEntries, err := os.ReadDir(paths.StagingRoot); err == nil {
		for _, e := range stEntries {
			if !e.IsDir() {
				continue
			}
			path := filepath.Join(paths.StagingRoot, e.Name())
			if err := os.RemoveAll(path); err == nil {
				res.StagingRemoved = append(res.StagingRemoved, e.Name())
			}
		}
	}

	return res, nil
}
