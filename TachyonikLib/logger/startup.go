// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The line every module writes first when it starts: its name, its exact
// version, and the Go version and platform it was built for. Worded the same
// in every module, so a support log names what ran and grep "Starting
// Tachyonik" finds each restart (docs/plans/startup-version-line.md).

package logger

import (
	"fmt"
	"runtime"
)

// StartupLine is the text Startup logs.
func StartupLine(module, version string) string {
	return fmt.Sprintf("Starting Tachyonik %s %s (%s, %s/%s)", module, version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// Startup logs the startup line at INFO. Call it right after logging is set
// up, before anything else is logged.
func Startup(module, version string) {
	// Straight to log, not through Infof: the line names the module's main as
	// its caller only at this call depth.
	if globalLogger != nil {
		globalLogger.log(INFO, "%s", StartupLine(module, version))
	}
}
