// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package selfupdate

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"time"
)

// Manifest is the published update manifest. The signature lives in a
// sibling .sig file and is verified with the proxy's embedded public keys.
//
// schemaVersion lets the manifest format evolve without breaking older
// proxies: a proxy that does not recognise the schema refuses the update
// rather than guessing.
type Manifest struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Channel       string              `json:"channel"`
	LatestVersion string              `json:"latestVersion"`
	PublishedAt   time.Time           `json:"publishedAt"`
	Artifacts     map[string]Artifact `json:"artifacts"`
}

// Artifact is the per-os/arch entry in a manifest. The key in the
// Manifest.Artifacts map is "<goos>/<goarch>", e.g. "linux/amd64".
type Artifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"` // hex-encoded
}

// ManifestSchemaVersion is the current schema version this proxy understands.
const ManifestSchemaVersion = 1

// VerifyManifest checks the detached Ed25519 signature against any of the
// trusted public keys. It does NOT parse the manifest — that's
// ParseManifest's job — so a forged manifest cannot trigger parser-side
// vulnerabilities before its signature has been validated.
func VerifyManifest(manifestBytes, signature []byte, pubKeys []ed25519.PublicKey) error {
	if len(pubKeys) == 0 {
		return errors.New("no trusted public keys")
	}
	if len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("signature length %d (expected %d)", len(signature), ed25519.SignatureSize)
	}
	for _, k := range pubKeys {
		if ed25519.Verify(k, manifestBytes, signature) {
			return nil
		}
	}
	return errors.New("signature does not match any trusted key")
}

// ParseManifest decodes the JSON manifest. Callers MUST call VerifyManifest
// on the raw bytes first; ParseManifest does not perform any trust check.
func ParseManifest(manifestBytes []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if m.SchemaVersion != ManifestSchemaVersion {
		return nil, fmt.Errorf("manifest schema version %d not supported (want %d)",
			m.SchemaVersion, ManifestSchemaVersion)
	}
	if m.LatestVersion == "" {
		return nil, errors.New("manifest missing latestVersion")
	}
	if m.Channel == "" {
		return nil, errors.New("manifest missing channel")
	}
	if m.PublishedAt.IsZero() {
		return nil, errors.New("manifest missing publishedAt")
	}
	if len(m.Artifacts) == 0 {
		return nil, errors.New("manifest has no artifacts")
	}
	return &m, nil
}

// ArtifactForCurrentPlatform returns the artifact entry for the running
// binary's GOOS/GOARCH. It returns an error if the current platform is not
// listed — the operator is told explicitly rather than silently skipped.
func (m *Manifest) ArtifactForCurrentPlatform() (*Artifact, error) {
	return m.ArtifactFor(runtime.GOOS, runtime.GOARCH)
}

// ArtifactFor returns the artifact entry for the given GOOS/GOARCH.
func (m *Manifest) ArtifactFor(goos, goarch string) (*Artifact, error) {
	key := goos + "/" + goarch
	a, ok := m.Artifacts[key]
	if !ok {
		return nil, fmt.Errorf("manifest has no artifact for %s", key)
	}
	if a.URL == "" {
		return nil, fmt.Errorf("manifest artifact %s has empty URL", key)
	}
	if a.SHA256 == "" {
		return nil, fmt.Errorf("manifest artifact %s has empty sha256", key)
	}
	return &a, nil
}
