// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the routine checksum: the SHA-256 in lower-case hex, and the refusal of
// a missing or wrong one.

package aicode

import (
	"errors"
	"strings"
	"testing"
)

func TestChecksum(t *testing.T) {
	// echo -n "var rules = [];" | sha256sum
	const code = "var rules = [];"
	want := Checksum(code)
	if len(want) != 64 || strings.ToLower(want) != want {
		t.Fatalf("checksum %q is not lower-case hex SHA-256", want)
	}
	if err := VerifyChecksum(code, want); err != nil {
		t.Errorf("matching checksum refused: %v", err)
	}
	if err := VerifyChecksum(code, ""); !errors.Is(err, ErrNoChecksum) {
		t.Errorf("missing checksum: %v", err)
	}
	if err := VerifyChecksum(code+" ", want); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("changed code: %v", err)
	}
	if err := VerifyChecksum("", want); err == nil {
		t.Error("empty code accepted against a checksum")
	}
}
