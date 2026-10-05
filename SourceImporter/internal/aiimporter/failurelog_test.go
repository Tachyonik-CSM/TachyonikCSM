// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the failure summary that goes into a resource's import notes, where the
// user reads why an import failed.

package aiimporter

import (
	"errors"
	"testing"
)

func TestFailureLogSummary(t *testing.T) {
	var f failureLog
	if f.summary() != "" {
		t.Errorf("an empty log summarised as %q", f.summary())
	}
	f.add("192.168.178.1", errors.New("unexpected status code: 200"))
	if got, want := f.summary(), "192.168.178.1: unexpected status code: 200"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	f.add("192.168.178.2", errors.New("other"))
	f.add("192.168.178.3", errors.New("other"))
	if got, want := f.summary(), "192.168.178.1: unexpected status code: 200 (and 2 more)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
