// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package selfupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock is an exclusive flock-based mutex that prevents two self-update
// invocations from racing. Acquired by the apply path; release on Close.
//
// flock is POSIX-only. On Windows the lock degrades to a no-op and the
// caller is responsible for serialising invocations elsewhere — Phase 2/3
// already excludes Windows from auto-update apply, so this is consistent.
type Lock struct {
	f *os.File
}

// AcquireLock takes an exclusive non-blocking flock on <stateDir>/update.lock.
// If another process holds the lock, returns ErrLockBusy.
//
// The caller MUST call Close to release the lock. Closing the *os.File
// releases the flock automatically; we do not unlink the lock file because
// flock is keyed on the inode and unlinking-while-held would defeat the
// next call.
func AcquireLock(stateFile string) (*Lock, error) {
	dir := filepath.Dir(stateFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	path := filepath.Join(dir, "update.lock")
	// 0600: a local non-root user must not be able to take their own
	// flock on this file. With world-read, any local account could
	// indefinitely block the legitimate updater (denial of service).
	// Chmod after Open in case the file was created by a prior release
	// that used 0644 — Chmod on a held fd does not affect the flock.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	_ = f.Chmod(0600)
	if err := flockExclusiveNonblocking(f); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLockBusy
		}
		return nil, fmt.Errorf("acquire flock: %w", err)
	}
	return &Lock{f: f}, nil
}

// Close releases the flock and closes the file.
func (l *Lock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	// Closing releases the advisory lock automatically; no explicit
	// LOCK_UN needed.
	return l.f.Close()
}

// ErrLockBusy is returned by AcquireLock when another process holds the
// lock. Callers should treat this as a friendly "another update is in
// progress" condition, not a fatal error.
var ErrLockBusy = errors.New("another self-update invocation is already running")
