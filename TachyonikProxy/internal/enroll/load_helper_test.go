// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// A test helper: load a configuration and fail the test when it cannot be used.

package enroll

import (
	"testing"

	"tachyonik/tachyonikproxy/internal/config"
)

func mustLoadFrom(t *testing.T, path string) *config.Config {
	t.Helper()
	cfg, err := config.LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom(%s): %v", path, err)
	}
	return cfg
}
