// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the release check: a manifest is believed only with a valid signature
// from a trusted key, on the stable channel, naming a numeric version; and
// versions compare numerically.

package proxyrelease

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const goodManifest = `{
  "schemaVersion": 1,
  "channel": "stable",
  "latestVersion": "1.4.0",
  "publishedAt": "2026-11-03T15:59:41Z",
  "artifacts": {}
}`

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestVerifyAcceptsASignedManifest(t *testing.T) {
	pub, priv := newKey(t)
	r, err := Verify([]byte(goodManifest), ed25519.Sign(priv, []byte(goodManifest)), []ed25519.PublicKey{pub})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if r.LatestVersion != "1.4.0" || r.PublishedAt.IsZero() {
		t.Errorf("release = %+v", r)
	}
}

func TestVerifyRefuses(t *testing.T) {
	pub, priv := newKey(t)
	_, otherPriv := newKey(t)
	signed := func(body string) []byte { return ed25519.Sign(priv, []byte(body)) }
	cases := map[string]struct {
		body string
		sig  []byte
	}{
		"a signature by another key": {goodManifest, ed25519.Sign(otherPriv, []byte(goodManifest))},
		"an altered body":            {strings.Replace(goodManifest, "1.4.0", "9.9.9", 1), signed(goodManifest)},
		"a truncated signature":      {goodManifest, signed(goodManifest)[:10]},
		"another channel":            {strings.Replace(goodManifest, "stable", "beta", 1), signed(strings.Replace(goodManifest, "stable", "beta", 1))},
		"another schema":             {strings.Replace(goodManifest, `"schemaVersion": 1`, `"schemaVersion": 2`, 1), signed(strings.Replace(goodManifest, `"schemaVersion": 1`, `"schemaVersion": 2`, 1))},
		"a non-numeric version":      {strings.Replace(goodManifest, "1.4.0", "1.4.0-rc1", 1), signed(strings.Replace(goodManifest, "1.4.0", "1.4.0-rc1", 1))},
	}
	for name, c := range cases {
		if _, err := Verify([]byte(c.body), c.sig, []ed25519.PublicKey{pub}); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestFetchReadsTheManifestAndItsSignature(t *testing.T) {
	pub, priv := newKey(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write([]byte(goodManifest))
		case "/manifest.json.sig":
			_, _ = w.Write(ed25519.Sign(priv, []byte(goodManifest)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	r, err := Fetch(context.Background(), srv.Client(), srv.URL+"/manifest.json", []ed25519.PublicKey{pub})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if r.LatestVersion != "1.4.0" {
		t.Errorf("latest = %q", r.LatestVersion)
	}
}

// The signature missing — as when the download space answers 403 for it — is
// an error, never an unsigned release.
func TestFetchRefusesAMissingSignature(t *testing.T) {
	pub, _ := newKey(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest.json" {
			_, _ = w.Write([]byte(goodManifest))
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	if _, err := Fetch(context.Background(), srv.Client(), srv.URL+"/manifest.json", []ed25519.PublicKey{pub}); err == nil {
		t.Fatal("a manifest without its signature was accepted")
	}
}

func TestFetchRefusesPlainHTTP(t *testing.T) {
	if _, err := Fetch(context.Background(), http.DefaultClient, "http://example.com/manifest.json", nil); err == nil {
		t.Fatal("an http:// manifest URL was accepted")
	}
}

func TestTheEmbeddedKeyParses(t *testing.T) {
	keys, err := EmbeddedKeys()
	if err != nil || len(keys) == 0 {
		t.Fatalf("EmbeddedKeys: %v (%d keys)", err, len(keys))
	}
}

func TestUpdateAvailable(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"1.3.0", "1.4.0", true},
		{"1.9.0", "1.10.0", true},
		{"1.4", "1.4.1", true},
		{"1.4.0", "1.4.0", false},
		{"1.4.0", "1.4", false},
		{"1.5.0", "1.4.0", false},
		{"", "1.4.0", false},
		{"0.0.0-dev", "1.4.0", false},
		{"0.0.0", "1.4.0", false},
		{"1.3.0", "", false},
		{"1.3.0", "1.4.0-rc1", false},
		// Development builds, by the release they were built from.
		{"1.2.0-2-g8882cc9-dirty", "1.4.0", true},
		{"1.2.0-2-g8882cc9", "1.4.0", true},
		{"1.2.0-dirty", "1.4.0", true},
		{"1.4.0-3-gabc123", "1.4.0", false},
		{"1.4.0-dirty", "1.4.0", false},
		{"1.4.0-3-gabc123", "1.5.0", true},
		// Other forms name no release.
		{"1.2.0-beta", "1.4.0", false},
		{"1.2.0-2-gXYZ", "1.4.0", false},
		{"v1.2.0", "1.4.0", false},
	}
	for _, c := range cases {
		if got := UpdateAvailable(c.current, c.latest); got != c.want {
			t.Errorf("UpdateAvailable(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}
