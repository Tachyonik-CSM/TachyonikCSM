// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package selfupdate implements the trust chain and orchestration for the
// proxy's auto-update flow. Phase 1 ships verification only — the apply path
// and rollback live in Phase 2 / Phase 3.
package selfupdate

import (
	"crypto/ed25519"
	"crypto/x509"
	"embed"
	"encoding/pem"
	"fmt"
	"io/fs"
	"strings"
)

// pubKeysFS embeds every PEM file under pubkeys/ into the binary. Multiple
// keys are allowed so that signing-key rotation does not require a flag day:
// during overlap, manifests are signed with both old and new keys and the
// updater accepts a signature from either.
//
//go:embed pubkeys/*.pem
var pubKeysFS embed.FS

// LoadEmbeddedPubKeys parses every *.pem under the embedded pubkeys directory
// as an Ed25519 public key. It returns an error if any file is unparseable;
// failing closed is preferable to silently degrading the trust chain.
func LoadEmbeddedPubKeys() ([]ed25519.PublicKey, error) {
	entries, err := fs.ReadDir(pubKeysFS, "pubkeys")
	if err != nil {
		return nil, fmt.Errorf("read embedded pubkeys dir: %w", err)
	}

	var keys []ed25519.PublicKey
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pem") {
			continue
		}
		data, err := fs.ReadFile(pubKeysFS, "pubkeys/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("read embedded pubkey %s: %w", e.Name(), err)
		}
		key, err := parseEd25519PublicKeyPEM(data)
		if err != nil {
			return nil, fmt.Errorf("parse embedded pubkey %s: %w", e.Name(), err)
		}
		keys = append(keys, key)
	}

	if len(keys) == 0 {
		return nil, fmt.Errorf("no embedded auto-update public keys found")
	}
	return keys, nil
}

// parseEd25519PublicKeyPEM parses a PKIX-formatted Ed25519 public key PEM
// block (the format produced by `openssl pkey -pubout`).
func parseEd25519PublicKeyPEM(data []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("not a PEM block")
	}
	if block.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("unexpected PEM block type %q (want PUBLIC KEY)", block.Type)
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKIX public key: %w", err)
	}
	edPub, ok := pub.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is %T, not Ed25519", pub)
	}
	return edPub, nil
}
