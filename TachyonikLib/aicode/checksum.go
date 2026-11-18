// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The checksum a generated routine is stored with, and the check that the code
// about to run is still the code that was generated and validated.
//
// Every generator records Checksum(code) beside the routine; every module that
// runs one, and AIManager when it stores one, calls VerifyChecksum first. A
// routine with no checksum is refused like one whose checksum does not match:
// generation and feed imports always record one, so a row without it was not
// written by either (docs/plans/routine-checksums.md). This is depth, not a
// boundary — whoever can write the routine row can write its checksum too —
// but a checksum that is recorded and never read is decoration.

package aicode

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// ErrNoChecksum is returned for a routine with no recorded checksum.
var ErrNoChecksum = errors.New("no SHA256 is recorded for the stored code")

// Checksum is the SHA-256 of code in lower-case hex, as routines record it.
func Checksum(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// VerifyChecksum reports why code may not run under the checksum recorded with
// it: none is recorded, or it is not the code's. Nil means the code may run.
func VerifyChecksum(code, recorded string) error {
	if recorded == "" {
		return ErrNoChecksum
	}
	if got := Checksum(code); got != recorded {
		return fmt.Errorf("stored code does not match its recorded SHA256 (recorded %s, got %s)", recorded, got)
	}
	return nil
}
