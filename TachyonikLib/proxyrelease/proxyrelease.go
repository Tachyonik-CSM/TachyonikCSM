// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package proxyrelease tells which TachyonikProxy version is the latest
// release, from the signed auto-update manifest the proxies themselves update
// from (https://tachyonik.com/download/proxy/manifest.json and its detached
// Ed25519 signature, manifest.json.sig).
//
// The manifest is trusted only when its signature verifies against a public
// key embedded here — the key the proxy embeds too (pubkeys/; the proxy's
// selfupdate tests check the two copies agree). Without that, whoever could
// alter the download space could make every installation tell its users to
// update to a version that does not exist. Only a manifest of the known
// schema, on the stable channel, naming a purely numeric version is accepted.
//
// It also compares versions as the proxy does: dotted decimal, numerically,
// so 1.10.0 is newer than 1.9.0.
package proxyrelease

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"embed"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultManifestURL is where the proxies' auto-update manifest is published.
const DefaultManifestURL = "https://tachyonik.com/download/proxy/manifest.json"

// Channel is the only channel a release is taken from.
const Channel = "stable"

// schemaVersion is the manifest format understood here.
const schemaVersion = 1

// maxFetchBytes bounds what is read from the server; the manifest and its
// signature are a few hundred bytes.
const maxFetchBytes = 1 << 20

//go:embed pubkeys/*.pem
var pubKeysFS embed.FS

// Release is the latest published proxy release.
type Release struct {
	LatestVersion string    `json:"latestVersion"`
	PublishedAt   time.Time `json:"publishedAt"`
}

type manifest struct {
	SchemaVersion int       `json:"schemaVersion"`
	Channel       string    `json:"channel"`
	LatestVersion string    `json:"latestVersion"`
	PublishedAt   time.Time `json:"publishedAt"`
}

// EmbeddedKeys returns the trusted release-signing public keys. An
// unparseable key is an error: failing closed beats a weakened trust chain.
func EmbeddedKeys() ([]ed25519.PublicKey, error) {
	entries, err := fs.ReadDir(pubKeysFS, "pubkeys")
	if err != nil {
		return nil, fmt.Errorf("read embedded pubkeys: %w", err)
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
		key, err := ParsePublicKeyPEM(data)
		if err != nil {
			return nil, fmt.Errorf("parse embedded pubkey %s: %w", e.Name(), err)
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, errors.New("no embedded release-signing keys")
	}
	return keys, nil
}

// ParsePublicKeyPEM parses a PKIX Ed25519 public key PEM ("PUBLIC KEY", as
// `openssl pkey -pubout` writes it).
func ParsePublicKeyPEM(data []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("not a PEM block")
	}
	if block.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("unexpected PEM block type %q (want PUBLIC KEY)", block.Type)
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKIX public key: %w", err)
	}
	ed, ok := pub.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is %T, not Ed25519", pub)
	}
	return ed, nil
}

// Fetch reads the manifest at manifestURL and its signature at
// manifestURL+".sig", verifies the signature against keys, and returns the
// release it names. manifestURL must be https://.
func Fetch(ctx context.Context, client *http.Client, manifestURL string, keys []ed25519.PublicKey) (*Release, error) {
	if !strings.HasPrefix(manifestURL, "https://") {
		return nil, fmt.Errorf("manifest URL must be https://, got %q", manifestURL)
	}
	body, err := fetch(ctx, client, manifestURL)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	sig, err := fetch(ctx, client, manifestURL+".sig")
	if err != nil {
		return nil, fmt.Errorf("fetch manifest signature: %w", err)
	}
	return Verify(body, sig, keys)
}

// Verify checks the detached signature over body and returns the release the
// manifest names. The body is parsed only after the signature verifies.
func Verify(body, sig []byte, keys []ed25519.PublicKey) (*Release, error) {
	if len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("signature length %d (expected %d)", len(sig), ed25519.SignatureSize)
	}
	trusted := false
	for _, k := range keys {
		if ed25519.Verify(k, body, sig) {
			trusted = true
			break
		}
	}
	if !trusted {
		return nil, errors.New("signature does not match any trusted key")
	}
	var m manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if m.SchemaVersion != schemaVersion {
		return nil, fmt.Errorf("manifest schema %d not understood (want %d)", m.SchemaVersion, schemaVersion)
	}
	if m.Channel != Channel {
		return nil, fmt.Errorf("manifest channel %q is not %q", m.Channel, Channel)
	}
	if _, err := parseVersion(m.LatestVersion); err != nil {
		return nil, fmt.Errorf("manifest version %q: %w", m.LatestVersion, err)
	}
	return &Release{LatestVersion: m.LatestVersion, PublishedAt: m.PublishedAt}, nil
}

func fetch(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
}

// CompareVersions returns -1, 0 or 1 as a is older than, equal to or newer
// than b. Both must be dotted decimal ("1.4.0"); anything else is an error.
func CompareVersions(a, b string) (int, error) {
	pa, err := parseVersion(a)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", a, err)
	}
	pb, err := parseVersion(b)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", b, err)
	}
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var ai, bi int
		if i < len(pa) {
			ai = pa[i]
		}
		if i < len(pb) {
			bi = pb[i]
		}
		switch {
		case ai < bi:
			return -1, nil
		case ai > bi:
			return 1, nil
		}
	}
	return 0, nil
}

// UpdateAvailable reports whether a proxy on version current is older than
// latest, a release version.
//
// A development build is judged by the release it was built from: `git
// describe` names it "<release>-<commits>-g<hash>", with "-dirty" for a tree
// with uncommitted changes. "1.2.0-2-g8882cc9-dirty" is a little newer than
// 1.2.0 but older than any later release, so it is outdated once 1.4.0 is
// out; "1.4.0-3-gabc1234" is newer than 1.4.0 and is not.
//
// False when current is unknown or has no release in it — "", "0.0.0-dev"
// (a build from no tag at all), or any other form — and when latest is not a
// release version: such a proxy is not told to update.
func UpdateAvailable(current, latest string) bool {
	base, ok := releaseBase(current)
	if !ok {
		return false
	}
	c, err := CompareVersions(base, latest)
	return err == nil && c < 0
}

// describeSuffix matches what `git describe --dirty` appends to a release:
// commits since it and their hash, and/or "-dirty".
var describeSuffix = regexp.MustCompile(`^(-[0-9]+-g[0-9a-f]+)?(-dirty)?$`)

// releaseBase returns the release a version names or was built from, and
// false when it names none. A base of only zeros (the "0.0.0-dev" fallback of
// an untagged build) names none.
func releaseBase(v string) (string, bool) {
	i := strings.IndexByte(v, '-')
	base, suffix := v, ""
	if i >= 0 {
		base, suffix = v[:i], v[i:]
	}
	parts, err := parseVersion(base)
	if err != nil || !describeSuffix.MatchString(suffix) {
		return "", false
	}
	for _, n := range parts {
		if n != 0 {
			return base, true
		}
	}
	return "", false
}

func parseVersion(s string) ([]int, error) {
	if s == "" {
		return nil, errors.New("empty")
	}
	parts := strings.Split(s, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		if p == "" || strings.TrimLeft(p, "0123456789") != "" {
			return nil, fmt.Errorf("component %q is not a number", p)
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("component %q: %w", p, err)
		}
		out[i] = n
	}
	return out, nil
}
