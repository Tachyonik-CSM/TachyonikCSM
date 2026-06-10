// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestLock_FileMode is the canary for L1. The lock file must be 0600 so a
// local non-root user cannot take their own flock and DoS the legitimate
// updater.
func TestLock_FileMode(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "update-state.json")

	lk, err := AcquireLock(statePath)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	defer lk.Close()

	info, err := os.Stat(filepath.Join(dir, "update.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("lock file mode = %#o, want 0600", got)
	}
}

// TestLock_FileMode_MigratesFromOld covers the case where a prior release
// created the lock file with 0644. AcquireLock must Chmod it back to 0600.
func TestLock_FileMode_MigratesFromOld(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "update-state.json")
	lockPath := filepath.Join(dir, "update.lock")

	// Pre-create with the loose mode.
	if err := os.WriteFile(lockPath, nil, 0644); err != nil {
		t.Fatal(err)
	}

	lk, err := AcquireLock(statePath)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	defer lk.Close()

	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("lock file mode after migration = %#o, want 0600", got)
	}
}

func TestLock_AcquireRelease(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "update-state.json")

	lk, err := AcquireLock(statePath)
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	if err := lk.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	// After release, another process (here, just another call) should
	// succeed.
	lk2, err := AcquireLock(statePath)
	if err != nil {
		t.Fatalf("second acquire after release failed: %v", err)
	}
	lk2.Close()
}

func TestLock_RejectsConcurrent(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "update-state.json")

	lk, err := AcquireLock(statePath)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer lk.Close()

	// flock(LOCK_EX|LOCK_NB) is per-fd, so a second AcquireLock from the
	// same process opens a new fd and is correctly seen as a contender.
	_, err = AcquireLock(statePath)
	if !errors.Is(err, ErrLockBusy) {
		t.Fatalf("second acquire should return ErrLockBusy, got %v", err)
	}
}

func TestLock_HandlesParallelGoroutines(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "update-state.json")

	var wg sync.WaitGroup
	wg.Add(2)

	// One goroutine holds the lock; the other tries and must fail with
	// ErrLockBusy. The lock is per-fd not per-process, so this exercises
	// the same mechanism a real second process would.
	holder, err := AcquireLock(statePath)
	if err != nil {
		t.Fatal(err)
	}

	var contenderErr error
	go func() {
		defer wg.Done()
		_, contenderErr = AcquireLock(statePath)
	}()
	go func() {
		defer wg.Done()
		// Released after a beat, but the contender has already raced; we
		// don't need to time it precisely because LOCK_NB is non-blocking.
		holder.Close()
	}()
	wg.Wait()

	// contenderErr is allowed to be either ErrLockBusy (if the contender
	// raced before holder.Close) or nil (if it raced after). Either is
	// correct behaviour. The point of the test is that we don't see any
	// other error, especially nothing involving syscall noise.
	if contenderErr != nil && !errors.Is(contenderErr, ErrLockBusy) {
		t.Fatalf("unexpected error: %v", contenderErr)
	}
}
