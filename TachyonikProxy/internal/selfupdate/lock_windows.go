// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package selfupdate

import "os"

// flockExclusiveNonblocking is a no-op on Windows. Auto-update apply is not
// supported on Windows yet (ResolveInstallPaths returns an error there), so
// the lock cannot be exercised on this platform.
func flockExclusiveNonblocking(f *os.File) error {
	return nil
}
