// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package selfupdate

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CheckOptions inputs everything the trust-chain check needs without doing
// any I/O. Callers (e.g. cmd/server) are responsible for assembling these
// from config + the running binary's identity.
type CheckOptions struct {
	// ManifestURL must be an https:// URL. Plain http is rejected.
	ManifestURL string
	// Channel selects which manifest "channel" the proxy participates in.
	// A manifest whose channel does not match is treated as not applicable.
	Channel string
	// CurrentVersion is the version string compiled into the running proxy
	// (cmd/server.version). Used for monotonicity checks.
	CurrentVersion string
	// LastAppliedPublishedAt is the publishedAt timestamp of the manifest
	// that produced the currently installed version, if known. Phase 1 has
	// no persistent state, so this is allowed to be the zero value — in
	// which case the publishedAt replay check is a no-op for this run.
	LastAppliedPublishedAt time.Time
	// PubKeys is the set of trusted Ed25519 keys.
	PubKeys []ed25519.PublicKey
	// HTTPClient lets tests inject httptest.NewTLSServer.Client(). Nil
	// means use http.DefaultClient with a 30 s timeout.
	HTTPClient *http.Client
}

// Decision is the outcome of a check. UpdateAvailable is true only when the
// manifest is trusted, applicable, newer, and not a replay. Reason is a
// human-readable string describing why — populated whether or not an update
// is available, so --dry-run can print the same line in either case.
type Decision struct {
	UpdateAvailable bool
	Manifest        *Manifest
	Artifact        *Artifact
	Reason          string
}

// Check performs the full trust-chain validation: fetch manifest +
// signature, verify signature, parse manifest, enforce channel, version
// monotonicity and publishedAt replay protection, and (if those all pass)
// return the artifact entry for the running platform.
//
// Errors returned from Check are operational failures (network, parse,
// signature). A trusted manifest that simply does not represent an update
// returns nil error and Decision.UpdateAvailable == false with a Reason.
func Check(ctx context.Context, opts CheckOptions) (*Decision, error) {
	if !strings.HasPrefix(opts.ManifestURL, "https://") {
		return nil, fmt.Errorf("manifest URL must be https://, got %q", opts.ManifestURL)
	}
	if opts.CurrentVersion == "" {
		return nil, fmt.Errorf("current version is empty")
	}
	if opts.Channel == "" {
		return nil, fmt.Errorf("channel is empty")
	}
	if len(opts.PubKeys) == 0 {
		return nil, fmt.Errorf("no trusted public keys configured")
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	manifestBytes, err := fetch(ctx, client, opts.ManifestURL)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	sigBytes, err := fetch(ctx, client, opts.ManifestURL+".sig")
	if err != nil {
		return nil, fmt.Errorf("fetch manifest signature: %w", err)
	}

	if err := VerifyManifest(manifestBytes, sigBytes, opts.PubKeys); err != nil {
		return nil, fmt.Errorf("verify manifest: %w", err)
	}

	m, err := ParseManifest(manifestBytes)
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	if m.Channel != opts.Channel {
		return &Decision{
			Manifest: m,
			Reason:   fmt.Sprintf("manifest channel %q does not match configured channel %q", m.Channel, opts.Channel),
		}, nil
	}

	cmp, err := compareVersions(m.LatestVersion, opts.CurrentVersion)
	if err != nil {
		return nil, fmt.Errorf("compare versions: %w", err)
	}
	if cmp == 0 {
		return &Decision{
			Manifest: m,
			Reason:   fmt.Sprintf("already on latest version %s", opts.CurrentVersion),
		}, nil
	}
	if cmp < 0 {
		// Refusing a downgrade is a hard rule — this guards against an
		// attacker who somehow obtained a valid signature on an older
		// manifest and is replaying it to pin the fleet to a known-vulnerable
		// version.
		return &Decision{
			Manifest: m,
			Reason:   fmt.Sprintf("manifest version %s is older than installed %s; refusing downgrade", m.LatestVersion, opts.CurrentVersion),
		}, nil
	}

	// publishedAt replay protection: refuse manifests not strictly newer
	// than the one that produced the currently installed version.
	if !opts.LastAppliedPublishedAt.IsZero() && !m.PublishedAt.After(opts.LastAppliedPublishedAt) {
		return &Decision{
			Manifest: m,
			Reason:   fmt.Sprintf("manifest publishedAt %s is not newer than last applied %s; possible replay", m.PublishedAt.Format(time.RFC3339), opts.LastAppliedPublishedAt.Format(time.RFC3339)),
		}, nil
	}

	artifact, err := m.ArtifactForCurrentPlatform()
	if err != nil {
		return &Decision{
			Manifest: m,
			Reason:   err.Error(),
		}, nil
	}

	return &Decision{
		UpdateAvailable: true,
		Manifest:        m,
		Artifact:        artifact,
		Reason:          fmt.Sprintf("update available: %s → %s", opts.CurrentVersion, m.LatestVersion),
	}, nil
}

// fetch performs an HTTP GET and returns the body. Non-2xx is an error.
// Body size is bounded to keep the trust-chain code resilient to a
// pathological CDN response — the manifest and signature are tiny.
const maxFetchBytes = 1 << 20 // 1 MiB

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
		return nil, fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
}

// compareVersions returns -1 / 0 / 1 if a is less / equal / greater than b.
// Versions are dotted decimal strings (e.g. "1.4.0", "1.4.10"). An empty
// component or a non-numeric component is an error — the format we publish
// is under our control and we'd rather fail closed than guess.
func compareVersions(a, b string) (int, error) {
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
		if ai < bi {
			return -1, nil
		}
		if ai > bi {
			return 1, nil
		}
	}
	return 0, nil
}

func parseVersion(s string) ([]int, error) {
	if s == "" {
		return nil, fmt.Errorf("empty")
	}
	parts := strings.Split(s, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("component %q is not numeric", p)
		}
		if n < 0 {
			return nil, fmt.Errorf("component %q is negative", p)
		}
		out[i] = n
	}
	return out, nil
}
