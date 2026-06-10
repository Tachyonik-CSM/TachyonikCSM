// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// signTestManifest generates a fresh keypair and returns (manifestBytes, sig, pubkey).
func signTestManifest(t *testing.T, m Manifest) ([]byte, []byte, ed25519.PublicKey) {
	t.Helper()
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sig := ed25519.Sign(priv, body)
	return body, sig, pub
}

func validManifest() Manifest {
	return Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Channel:       "stable",
		LatestVersion: "1.4.0",
		PublishedAt:   time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
		Artifacts: map[string]Artifact{
			"linux/amd64":  {URL: "https://example/x86", SHA256: strings.Repeat("a", 64)},
			"linux/arm64":  {URL: "https://example/arm", SHA256: strings.Repeat("b", 64)},
			"darwin/amd64": {URL: "https://example/mac", SHA256: strings.Repeat("c", 64)},
			"darwin/arm64": {URL: "https://example/m1", SHA256: strings.Repeat("d", 64)},
		},
	}
}

func TestVerifyManifest_Roundtrip(t *testing.T) {
	body, sig, pub := signTestManifest(t, validManifest())
	if err := VerifyManifest(body, sig, []ed25519.PublicKey{pub}); err != nil {
		t.Fatalf("expected valid signature: %v", err)
	}
}

func TestVerifyManifest_TamperedBody(t *testing.T) {
	body, sig, pub := signTestManifest(t, validManifest())
	body[0] ^= 0x01
	if err := VerifyManifest(body, sig, []ed25519.PublicKey{pub}); err == nil {
		t.Fatal("tampered body must not verify")
	}
}

func TestVerifyManifest_TamperedSig(t *testing.T) {
	body, sig, pub := signTestManifest(t, validManifest())
	sig[0] ^= 0x01
	if err := VerifyManifest(body, sig, []ed25519.PublicKey{pub}); err == nil {
		t.Fatal("tampered signature must not verify")
	}
}

func TestVerifyManifest_TwoKeys_AcceptsEither(t *testing.T) {
	body, sig, pubA := signTestManifest(t, validManifest())
	pubB, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Trusted keys are [A, B]; signature is from A.
	if err := VerifyManifest(body, sig, []ed25519.PublicKey{pubA, pubB}); err != nil {
		t.Fatalf("expected verification to accept signature from key A: %v", err)
	}
	// Trusted keys are [B, A]; signature is from A.
	if err := VerifyManifest(body, sig, []ed25519.PublicKey{pubB, pubA}); err != nil {
		t.Fatalf("expected verification to accept signature from key A regardless of order: %v", err)
	}
}

func TestVerifyManifest_NoTrustedKeys(t *testing.T) {
	body, sig, _ := signTestManifest(t, validManifest())
	if err := VerifyManifest(body, sig, nil); err == nil {
		t.Fatal("must fail when no keys are trusted")
	}
}

func TestParseManifest_Rejects(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(m *Manifest)
		wantSub string
	}{
		{"wrong schema version", func(m *Manifest) { m.SchemaVersion = 99 }, "schema version"},
		{"missing version", func(m *Manifest) { m.LatestVersion = "" }, "latestVersion"},
		{"missing channel", func(m *Manifest) { m.Channel = "" }, "channel"},
		{"missing publishedAt", func(m *Manifest) { m.PublishedAt = time.Time{} }, "publishedAt"},
		{"no artifacts", func(m *Manifest) { m.Artifacts = nil }, "artifacts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validManifest()
			tc.mutate(&m)
			body, _ := json.Marshal(m)
			_, err := ParseManifest(body)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error containing %q, got %v", tc.wantSub, err)
			}
		})
	}
}

func TestArtifactForCurrentPlatform_Missing(t *testing.T) {
	m := validManifest()
	delete(m.Artifacts, "linux/amd64")
	delete(m.Artifacts, "linux/arm64")
	delete(m.Artifacts, "darwin/amd64")
	delete(m.Artifacts, "darwin/arm64")
	if _, err := m.ArtifactForCurrentPlatform(); err == nil {
		t.Fatal("must fail when no artifact matches the current platform")
	}
}

func TestVerifyArtifactReader(t *testing.T) {
	data := []byte("hello world")
	const expected = "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"

	if err := VerifyArtifactReader(strings.NewReader(string(data)), expected); err != nil {
		t.Fatalf("matching sha must succeed: %v", err)
	}
	if err := VerifyArtifactReader(strings.NewReader(string(data)), strings.Repeat("0", 64)); err == nil {
		t.Fatal("mismatched sha must fail")
	}
	if err := VerifyArtifactReader(strings.NewReader(string(data)), "tooshort"); err == nil {
		t.Fatal("malformed sha must fail")
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.0.1", "1.0.0", 1},
		{"1.4.0", "1.4.10", -1},
		{"1.10.0", "1.9.9", 1},
		{"2.0", "1.99.99", 1},
		{"1.0.0.0", "1.0.0", 0}, // trailing zero components compare equal
	}
	for _, tc := range cases {
		got, err := compareVersions(tc.a, tc.b)
		if err != nil {
			t.Fatalf("compareVersions(%q,%q) errored: %v", tc.a, tc.b, err)
		}
		if got != tc.want {
			t.Errorf("compareVersions(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}

	if _, err := compareVersions("1.0.0-rc1", "1.0.0"); err == nil {
		t.Fatal("non-numeric component must error")
	}
	if _, err := compareVersions("", "1.0.0"); err == nil {
		t.Fatal("empty version must error")
	}
}

// newSignedTLSServer serves manifest + .sig over TLS using the test server's
// own cert. Returns (server, pubkey). The HTTPClient on the server is
// pre-trusted to verify the test cert.
func newSignedTLSServer(t *testing.T, body, sig []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	})
	mux.HandleFunc("/manifest.json.sig", func(w http.ResponseWriter, r *http.Request) {
		w.Write(sig)
	})
	return httptest.NewTLSServer(mux)
}

func TestCheck_UpdateAvailable(t *testing.T) {
	m := validManifest()
	// Ensure the artifacts map covers the current platform.
	body, sig, pub := signTestManifest(t, m)

	srv := newSignedTLSServer(t, body, sig)
	defer srv.Close()

	d, err := Check(context.Background(), CheckOptions{
		ManifestURL:    srv.URL + "/manifest.json",
		Channel:        "stable",
		CurrentVersion: "1.3.0",
		PubKeys:        []ed25519.PublicKey{pub},
		HTTPClient:     srv.Client(),
	})
	if err != nil {
		t.Fatalf("Check errored: %v", err)
	}
	if !d.UpdateAvailable {
		t.Fatalf("expected update available, got reason: %s", d.Reason)
	}
	if d.Artifact == nil {
		t.Fatal("expected artifact to be populated when update is available")
	}
}

func TestCheck_Downgrade(t *testing.T) {
	m := validManifest()
	body, sig, pub := signTestManifest(t, m)

	srv := newSignedTLSServer(t, body, sig)
	defer srv.Close()

	d, err := Check(context.Background(), CheckOptions{
		ManifestURL:    srv.URL + "/manifest.json",
		Channel:        "stable",
		CurrentVersion: "2.0.0", // higher than manifest's 1.4.0
		PubKeys:        []ed25519.PublicKey{pub},
		HTTPClient:     srv.Client(),
	})
	if err != nil {
		t.Fatalf("Check errored: %v", err)
	}
	if d.UpdateAvailable {
		t.Fatal("downgrade must not be reported as available")
	}
	if !strings.Contains(d.Reason, "downgrade") {
		t.Errorf("expected reason to mention downgrade, got %q", d.Reason)
	}
}

func TestCheck_Replay(t *testing.T) {
	m := validManifest()
	body, sig, pub := signTestManifest(t, m)

	srv := newSignedTLSServer(t, body, sig)
	defer srv.Close()

	// Pretend we already applied a manifest published at the same instant.
	d, err := Check(context.Background(), CheckOptions{
		ManifestURL:            srv.URL + "/manifest.json",
		Channel:                "stable",
		CurrentVersion:         "1.3.0",
		LastAppliedPublishedAt: m.PublishedAt,
		PubKeys:                []ed25519.PublicKey{pub},
		HTTPClient:             srv.Client(),
	})
	if err != nil {
		t.Fatalf("Check errored: %v", err)
	}
	if d.UpdateAvailable {
		t.Fatal("replayed manifest must not be reported as available")
	}
	if !strings.Contains(d.Reason, "replay") {
		t.Errorf("expected reason to mention replay, got %q", d.Reason)
	}
}

func TestCheck_ChannelMismatch(t *testing.T) {
	m := validManifest()
	m.Channel = "beta"
	body, sig, pub := signTestManifest(t, m)

	srv := newSignedTLSServer(t, body, sig)
	defer srv.Close()

	d, err := Check(context.Background(), CheckOptions{
		ManifestURL:    srv.URL + "/manifest.json",
		Channel:        "stable",
		CurrentVersion: "1.3.0",
		PubKeys:        []ed25519.PublicKey{pub},
		HTTPClient:     srv.Client(),
	})
	if err != nil {
		t.Fatalf("Check errored: %v", err)
	}
	if d.UpdateAvailable {
		t.Fatal("channel mismatch must not be reported as available")
	}
	if !strings.Contains(d.Reason, "channel") {
		t.Errorf("expected reason to mention channel, got %q", d.Reason)
	}
}

func TestCheck_BadSignature(t *testing.T) {
	m := validManifest()
	body, sig, _ := signTestManifest(t, m)
	// Trust an unrelated key; signature must not verify.
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	srv := newSignedTLSServer(t, body, sig)
	defer srv.Close()

	_, err = Check(context.Background(), CheckOptions{
		ManifestURL:    srv.URL + "/manifest.json",
		Channel:        "stable",
		CurrentVersion: "1.3.0",
		PubKeys:        []ed25519.PublicKey{otherPub},
		HTTPClient:     srv.Client(),
	})
	if err == nil {
		t.Fatal("expected Check to fail when signature is not from a trusted key")
	}
}

func TestCheck_RejectsHTTPManifestURL(t *testing.T) {
	_, err := Check(context.Background(), CheckOptions{
		ManifestURL:    "http://example.com/manifest.json",
		Channel:        "stable",
		CurrentVersion: "1.3.0",
		PubKeys:        []ed25519.PublicKey{make(ed25519.PublicKey, ed25519.PublicKeySize)},
	})
	if err == nil {
		t.Fatal("expected Check to reject plain-http manifest URL")
	}
}

func TestLoadEmbeddedPubKeys(t *testing.T) {
	keys, err := LoadEmbeddedPubKeys()
	if err != nil {
		t.Fatalf("LoadEmbeddedPubKeys: %v", err)
	}
	if len(keys) == 0 {
		t.Fatal("expected at least one embedded key")
	}
	for i, k := range keys {
		if len(k) != ed25519.PublicKeySize {
			t.Errorf("key %d has length %d (want %d)", i, len(k), ed25519.PublicKeySize)
		}
	}
}
