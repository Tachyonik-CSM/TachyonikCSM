// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests wiring the logger from a service's configuration.
//
// Seven services now share this, so the cases that matter are the ones each of
// them relied on: the file is opened for append and handed back to be closed, a
// file that cannot be opened is an error rather than a silent downgrade to
// console-only, and both destinations off is allowed.

package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupWritesToTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")

	f, err := SetupFromOptions(FileOptions{ToFile: true, FilePath: path, Level: "INFO"})
	if err != nil {
		t.Fatalf("SetupFromOptions: %v", err)
	}
	if f == nil {
		t.Fatal("no file handle returned; the caller has nothing to close")
	}
	defer f.Close()

	Info("hello from the test")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(raw), "hello from the test") {
		t.Errorf("log file does not contain the line: %q", raw)
	}
}

// Existing content is kept: a restart must not discard the log.
func TestSetupAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	if err := os.WriteFile(path, []byte("earlier run\n"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	f, err := SetupFromOptions(FileOptions{ToFile: true, FilePath: path, Level: "INFO"})
	if err != nil {
		t.Fatalf("SetupFromOptions: %v", err)
	}
	defer f.Close()
	Info("later run")

	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "earlier run") {
		t.Errorf("the previous run's log was discarded: %q", raw)
	}
	if !strings.Contains(string(raw), "later run") {
		t.Errorf("the new line is missing: %q", raw)
	}
}

// A path that cannot be opened is an error. The services using this treat file
// logging as something the operator asked for, so starting up having silently
// failed to honour it is worse than not starting.
func TestSetupReportsAnUnopenableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-directory", "service.log")

	f, err := SetupFromOptions(FileOptions{ToConsole: true, ToFile: true, FilePath: path, Level: "INFO"})
	if err == nil {
		t.Fatal("an unopenable log file was accepted")
	}
	if f != nil {
		t.Errorf("a file handle was returned alongside the error: %v", f)
	}
}

// File logging off means no file and no error — the common case for a service
// run in the foreground.
func TestSetupConsoleOnly(t *testing.T) {
	f, err := SetupFromOptions(FileOptions{ToConsole: true, Level: "DEBUG"})
	if err != nil {
		t.Fatalf("SetupFromOptions: %v", err)
	}
	if f != nil {
		t.Errorf("a file was opened with ToFile false: %v", f)
		f.Close()
	}
}

// Both off is a choice an operator can make, not an error to correct.
func TestSetupWithNoDestinations(t *testing.T) {
	f, err := SetupFromOptions(FileOptions{Level: "INFO"})
	if err != nil {
		t.Fatalf("SetupFromOptions: %v", err)
	}
	if f != nil {
		t.Errorf("a file was opened: %v", f)
		f.Close()
	}
	Info("this goes nowhere and must not panic")
}

// The level is read case-insensitively, and anything unrecognised is INFO.
func TestSetupLevel(t *testing.T) {
	for _, c := range []struct {
		in   string
		want LogLevel
	}{
		{"DEBUG", DEBUG}, {"debug", DEBUG}, {"WARN", WARN},
		{"ERROR", ERROR}, {"", INFO}, {"nonsense", INFO},
	} {
		if got := ParseLogLevel(c.in); got != c.want {
			t.Errorf("ParseLogLevel(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
