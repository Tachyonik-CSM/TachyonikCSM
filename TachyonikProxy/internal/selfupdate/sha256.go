// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// VerifyArtifactFile streams the file at path through SHA-256 and compares
// the result to the hex-encoded expected digest from the manifest.
//
// Phase 1 does not download artifacts, but the helper lives here so Phase 2's
// apply path has a single place to call (and so it gets the same test
// coverage as the rest of the trust chain).
func VerifyArtifactFile(path, expectedHex string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open artifact: %w", err)
	}
	defer f.Close()
	return VerifyArtifactReader(f, expectedHex)
}

// VerifyArtifactReader streams r through SHA-256 and compares the result.
func VerifyArtifactReader(r io.Reader, expectedHex string) error {
	expected := strings.ToLower(strings.TrimSpace(expectedHex))
	if len(expected) != 2*sha256.Size {
		return fmt.Errorf("expected sha256 has %d hex chars (want %d)", len(expected), 2*sha256.Size)
	}

	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return fmt.Errorf("hash artifact: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != expected {
		return fmt.Errorf("sha256 mismatch: got %s, expected %s", got, expected)
	}
	return nil
}
