// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package main

// platformMain on non-Windows just runs the server directly.
func platformMain() {
	runServer()
}

// printPlatformHelp prints platform-specific help (none for Linux/macOS).
func printPlatformHelp() {}
