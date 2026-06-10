// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package execenv builds the sanitized environment used when the proxy
// executes external commands — both configured tools (internal/tools) and
// ToolManager-supplied scan routines' exec() (internal/toolscan). Keeping it
// in one place ensures the two security-sensitive exec paths cannot drift.
package execenv

import "os"

// MinimalEnv returns a sanitized environment for executed commands: a small
// allowlist of non-secret variables only, never the proxy's full os.Environ()
// (which may carry enrollment keys, tokens, etc.). PATH is always present so
// commands remain locatable even if the daemon was started without one.
func MinimalEnv() []string {
	allow := []string{
		"PATH", "HOME", "TMPDIR", "TZ", "TERM", "LANG", "LC_ALL", "LC_CTYPE",
		// Windows essentials (absent on unix; LookupEnv skips them there).
		"SystemRoot", "SystemDrive", "windir", "TEMP", "TMP", "PATHEXT", "ComSpec",
	}
	env := make([]string, 0, len(allow))
	havePath := false
	for _, k := range allow {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
			if k == "PATH" {
				havePath = true
			}
		}
	}
	if !havePath {
		env = append(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
	return env
}
