// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package selfupdate

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// pruneFixture builds a temp VersionRoot with a set of version dirs, each
// containing a tachyonikproxy binary and stamped with the requested mtime
// (so the prune logic can deterministically choose newest-first).
type pruneFixture struct {
	root  string
	paths *InstallPaths
}

func newPruneFixture(t *testing.T, versions map[string]time.Time, currentTarget string) *pruneFixture {
	t.Helper()
	root := t.TempDir()
	for name, mtime := range versions {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(dir, "tachyonikproxy")
		if err := os.WriteFile(bin, []byte("BIN-"+name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	if currentTarget != "" {
		if err := os.Symlink(filepath.Join(root, currentTarget), filepath.Join(root, "current")); err != nil {
			t.Fatal(err)
		}
	}
	return &pruneFixture{
		root: root,
		paths: &InstallPaths{
			Mode:           ModeUser,
			VersionRoot:    root,
			CurrentSymlink: filepath.Join(root, "current"),
			StateFile:      filepath.Join(root, "update-state.json"),
			StagingRoot:    filepath.Join(root, ".staging"),
		},
	}
}

func dirsRemaining(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != "current" && e.Name() != ".staging" {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func TestPrune_KeepsActivePreviousAndRolledBack(t *testing.T) {
	now := time.Now()
	fx := newPruneFixture(t, map[string]time.Time{
		"1.0.0": now.Add(-100 * time.Hour),
		"1.1.0": now.Add(-80 * time.Hour),
		"1.2.0": now.Add(-60 * time.Hour),
		"1.3.0": now.Add(-40 * time.Hour), // previous
		"1.4.0": now.Add(-20 * time.Hour), // active
	}, "1.4.0")

	st := &State{
		Active:     "1.4.0",
		Previous:   "1.3.0",
		RolledBack: []string{"1.1.0"}, // sticky
	}

	// keepVersions=3 → top 3 by mtime: 1.4.0, 1.3.0, 1.2.0. Plus
	// always-keep: 1.4.0, 1.3.0, 1.1.0. Union: {1.4.0, 1.3.0, 1.2.0, 1.1.0}.
	// 1.0.0 is the only one removed.
	res, err := PruneOldVersions(fx.paths, st, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := dirsRemaining(t, fx.root), []string{"1.1.0", "1.2.0", "1.3.0", "1.4.0"}; !equalSlices(got, want) {
		t.Errorf("remaining dirs = %v, want %v (removed=%v)", got, want, res.Removed)
	}
	if got, want := res.Removed, []string{"1.0.0"}; !equalSlices(got, want) {
		t.Errorf("removed = %v, want %v", got, want)
	}
}

func TestPrune_HardCapAtKeepVersions_NoRollback(t *testing.T) {
	now := time.Now()
	fx := newPruneFixture(t, map[string]time.Time{
		"1.0.0": now.Add(-100 * time.Hour),
		"1.1.0": now.Add(-80 * time.Hour),
		"1.2.0": now.Add(-60 * time.Hour),
		"1.3.0": now.Add(-40 * time.Hour),
		"1.4.0": now.Add(-20 * time.Hour),
	}, "1.4.0")

	st := &State{
		Active:   "1.4.0",
		Previous: "1.3.0",
	}

	// keepVersions=3, no rolled-back: keep top-3 by mtime = 1.4.0, 1.3.0,
	// 1.2.0. Active (1.4.0) and Previous (1.3.0) are also in the set;
	// nothing additional. Final: 3 dirs.
	res, err := PruneOldVersions(fx.paths, st, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := dirsRemaining(t, fx.root), []string{"1.2.0", "1.3.0", "1.4.0"}; !equalSlices(got, want) {
		t.Errorf("remaining = %v, want %v (removed=%v)", got, want, res.Removed)
	}
}

func TestPrune_KeepZero_KeepsOnlySafetySet(t *testing.T) {
	now := time.Now()
	fx := newPruneFixture(t, map[string]time.Time{
		"1.0.0": now.Add(-100 * time.Hour),
		"1.1.0": now.Add(-80 * time.Hour),
		"1.2.0": now.Add(-60 * time.Hour),
	}, "1.2.0")

	st := &State{Active: "1.2.0", Previous: "1.1.0"}
	res, err := PruneOldVersions(fx.paths, st, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := dirsRemaining(t, fx.root), []string{"1.1.0", "1.2.0"}; !equalSlices(got, want) {
		t.Errorf("remaining = %v, want %v (removed=%v)", got, want, res.Removed)
	}
}

func TestPrune_RefusesNonVersionDirectory(t *testing.T) {
	now := time.Now()
	fx := newPruneFixture(t, map[string]time.Time{
		"1.0.0": now.Add(-50 * time.Hour),
		"1.1.0": now.Add(-30 * time.Hour),
	}, "1.1.0")

	// An operator-created directory without our binary inside.
	if err := os.MkdirAll(filepath.Join(fx.root, "operator-stuff"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fx.root, "operator-stuff", "notes.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}

	st := &State{Active: "1.1.0", Previous: "1.0.0"}
	_, err := PruneOldVersions(fx.paths, st, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fx.root, "operator-stuff")); err != nil {
		t.Errorf("operator-stuff/ was removed; defence-in-depth failed: %v", err)
	}
}

func TestPrune_SweepsStaleStaging(t *testing.T) {
	now := time.Now()
	fx := newPruneFixture(t, map[string]time.Time{
		"1.0.0": now.Add(-50 * time.Hour),
	}, "1.0.0")

	stage := filepath.Join(fx.paths.StagingRoot, "1.1.0-crashed")
	if err := os.MkdirAll(stage, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "tachyonikproxy"), []byte("ABC"), 0755); err != nil {
		t.Fatal(err)
	}

	st := &State{Active: "1.0.0"}
	res, err := PruneOldVersions(fx.paths, st, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Errorf("stale staging dir was not removed: %v", err)
	}
	if got, want := res.StagingRemoved, []string{"1.1.0-crashed"}; !equalSlices(got, want) {
		t.Errorf("StagingRemoved = %v, want %v", got, want)
	}
}

func TestPrune_MissingCurrentSymlink_FallsBackToState(t *testing.T) {
	now := time.Now()
	// No current symlink, but state.Active is set.
	fx := newPruneFixture(t, map[string]time.Time{
		"1.0.0": now.Add(-50 * time.Hour),
		"1.1.0": now.Add(-30 * time.Hour),
		"1.2.0": now.Add(-10 * time.Hour),
	}, "")

	st := &State{Active: "1.2.0", Previous: "1.1.0"}
	res, err := PruneOldVersions(fx.paths, st, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Without keepVersions safety, only Active+Previous remain.
	if got, want := dirsRemaining(t, fx.root), []string{"1.1.0", "1.2.0"}; !equalSlices(got, want) {
		t.Errorf("remaining = %v, want %v (removed=%v)", got, want, res.Removed)
	}
}

func TestPrune_EmptyVersionRoot_NoOp(t *testing.T) {
	root := t.TempDir()
	paths := &InstallPaths{
		VersionRoot:    root,
		CurrentSymlink: filepath.Join(root, "current"),
		StagingRoot:    filepath.Join(root, ".staging"),
	}
	res, err := PruneOldVersions(paths, &State{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 0 || len(res.Kept) != 0 {
		t.Errorf("expected empty result on empty root, got %+v", res)
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a2 := append([]string(nil), a...)
	b2 := append([]string(nil), b...)
	sort.Strings(a2)
	sort.Strings(b2)
	for i := range a2 {
		if a2[i] != b2[i] {
			return false
		}
	}
	return true
}
