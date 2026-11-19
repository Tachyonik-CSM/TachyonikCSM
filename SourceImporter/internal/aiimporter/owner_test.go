// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// A source without an owner is not imported: AssetManager would refuse every
// record of it (AM-SEC-13), and the import notes say why once instead.

package aiimporter

import (
	"strings"
	"testing"

	"tachyonik/sourceimporter/internal/rscmanager"
)

func TestASourceWithoutAnOwnerIsNotImported(t *testing.T) {
	for _, owner := range []int64{0, -1} {
		_, err := (&AIImporter{}).ImportSource(&rscmanager.Source{ID: 196, Filename: "caller-ip.txt", UserID: owner})
		if err == nil || !strings.Contains(err.Error(), "the source has no owner") {
			t.Errorf("owner %d: %v", owner, err)
		}
	}
}
