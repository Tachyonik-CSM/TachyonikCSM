// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The platform tells which proxy release is the latest from the same signed
// manifest the proxy updates from, and trusts it with its own copy of the
// release-signing keys (TachyonikLib/proxyrelease/pubkeys). The two copies
// must name the same keys: after a rotation done in only one place, either
// the proxy or the platform would stop believing the manifest.

package selfupdate

import (
	"bytes"
	"testing"

	"tachyonik/lib/proxyrelease"
)

func TestThePlatformTrustsTheSameReleaseKeys(t *testing.T) {
	ours, err := LoadEmbeddedPubKeys()
	if err != nil {
		t.Fatalf("proxy keys: %v", err)
	}
	theirs, err := proxyrelease.EmbeddedKeys()
	if err != nil {
		t.Fatalf("TachyonikLib/proxyrelease keys: %v", err)
	}
	if len(ours) != len(theirs) {
		t.Fatalf("proxy embeds %d key(s), TachyonikLib/proxyrelease %d", len(ours), len(theirs))
	}
	for _, k := range ours {
		found := false
		for _, l := range theirs {
			if bytes.Equal(k, l) {
				found = true
			}
		}
		if !found {
			t.Errorf("a proxy key is missing from TachyonikLib/proxyrelease/pubkeys")
		}
	}
}
