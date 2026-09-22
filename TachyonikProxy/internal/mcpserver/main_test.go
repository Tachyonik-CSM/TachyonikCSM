// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Keeps this package's tests away from the real proxy configuration.
//
// handleConfigUpdate persists after applying a push — that is the point of it,
// so a remotely configured proxy survives a restart. It writes to
// config.GetConfigPath(), which on a developer's machine resolves to the
// config of whatever proxy is installed there. A test that exercises the push
// path therefore overwrites a real, enrolled proxy's config.yaml with the
// half-empty struct the test constructed, wiping its TLS paths, its name and
// its connection mode. That happened: a working proxy stopped starting with
// "TLS not configured — proxy is not enrolled", and the cause was this test
// package.
//
// TACHYONIKPROXY_CONFIG is resolved once per process and cached
// (config.resolveOnce), so it has to be set before any test runs rather than
// per test — which is exactly what TestMain is for.

package mcpserver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mcpserver-config")
	if err != nil {
		panic("cannot create a temporary config directory: " + err.Error())
	}
	// Set before the first test, and left set: the resolved path is cached for
	// the life of the process, so restoring the environment mid-run would only
	// mislead a reader into thinking it mattered.
	os.Setenv("TACHYONIKPROXY_CONFIG", filepath.Join(dir, "config.yaml"))

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
