// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the integrity check on stored routine code.
//
// The hash is computed and stored when a routine is generated. Until this
// check existed it was never read again, so the code this daemon executed was
// whatever AIManager's routines table happened to hold.

package aigenerator

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"tachyonik/actiongenerator/internal/aimanager"
)

func hashOf(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

func TestMatchingHashIsAccepted(t *testing.T) {
	code := "var rules = [];"
	r := &aimanager.Routine{ID: 1, Code: code, SHA256: hashOf(code)}
	if err := verifyRoutineHash(r); err != nil {
		t.Errorf("code matching its hash was refused: %v", err)
	}
}

// The case this exists for: the row was changed after it was generated.
func TestAlteredCodeIsRefused(t *testing.T) {
	original := "var rules = [];"
	r := &aimanager.Routine{
		ID:     1,
		Code:   "var rules = []; /* something else entirely */",
		SHA256: hashOf(original),
	}
	err := verifyRoutineHash(r)
	if err == nil {
		t.Fatal("altered code was accepted")
	}
	if !strings.Contains(err.Error(), "SHA256") {
		t.Errorf("error = %q, want it to name the mismatch", err)
	}
}

// A routine stored before the hash was recorded has none. Refusing those would
// disable every rule on an installation that has not regenerated since.
func TestMissingHashIsAccepted(t *testing.T) {
	r := &aimanager.Routine{ID: 1, Code: "var rules = [];"}
	if err := verifyRoutineHash(r); err != nil {
		t.Errorf("a routine with no recorded hash was refused: %v", err)
	}
}

// A hash that is present but not the code's is refused however it got there —
// including an empty code field against a real hash.
func TestEmptyCodeAgainstAHashIsRefused(t *testing.T) {
	r := &aimanager.Routine{ID: 1, Code: "", SHA256: hashOf("var rules = [];")}
	if err := verifyRoutineHash(r); err == nil {
		t.Error("empty code was accepted against a recorded hash")
	}
}
