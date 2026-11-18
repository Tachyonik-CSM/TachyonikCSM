// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the startup line's wording, which support logs are searched by.

package logger

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

func TestStartupLine(t *testing.T) {
	want := "Starting Tachyonik ActionGenerator 1.4.0-3-gabc1234-dirty (" + runtime.Version() + ", " + runtime.GOOS + "/" + runtime.GOARCH + ")"
	if got := StartupLine("ActionGenerator", "1.4.0-3-gabc1234-dirty"); got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
	var buf bytes.Buffer
	Setup(INFO, &buf)
	Startup("ChatAI", "0.0.0-dev")
	if !strings.Contains(buf.String(), "Starting Tachyonik ChatAI 0.0.0-dev (") {
		t.Errorf("logged %q", buf.String())
	}
}
