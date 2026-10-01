// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the protocol against recorded answers from a real appliance.
//
// The fixtures in testdata are verbatim responses from a Greenbone OS 25.0.7
// appliance speaking GMP 22.8 — not hand-written examples. That matters for
// get_assets above all: the real document is 34 KB of permissions, sources and
// repeated identifiers, and it is also the case that showed GMP's default
// paging — 10 of the appliance's 23 hosts.

package gmp

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// fakeTransport replays a canned response and records what was asked.
type fakeTransport struct {
	written  strings.Builder
	response io.Reader
	closed   bool
}

func (f *fakeTransport) Write(p []byte) (int, error) { return f.written.Write(p) }
func (f *fakeTransport) Read(p []byte) (int, error)  { return f.response.Read(p) }
func (f *fakeTransport) Close() error                { f.closed = true; return nil }

func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(data)
}

func clientWith(t *testing.T, response string) (*Client, *fakeTransport) {
	t.Helper()
	ft := &fakeTransport{response: strings.NewReader(response)}
	return New(ft), ft
}

// The document comes back byte for byte: interpreting it is the platform's
// analysis and import rules' job, and anything changed here would be lost to
// them.
func TestHostAssetsAreTheAppliancesOwnDocument(t *testing.T) {
	// The recorded answer is a first page of 10 from 23 assets. Restated as
	// complete, it is what a single-page inventory looks like.
	doc := strings.Replace(fixture(t, "get_assets_host.xml"), "<filtered>23</filtered>", "<filtered>10</filtered>", 1)
	c, ft := clientWith(t, doc)

	assets, err := c.HostAssets()
	if err != nil {
		t.Fatalf("HostAssets: %v", err)
	}
	if string(assets.Document) != strings.TrimLeft(doc, " \t\r\n") {
		t.Error("the document was altered on the way through")
	}
	if assets.Count != 10 {
		t.Errorf("count = %d, want 10", assets.Count)
	}

	sent := ft.written.String()
	for _, want := range []string{`type="host"`, `details="1"`, `rows=-1`} {
		if !strings.Contains(sent, want) {
			t.Errorf("request lacks %s: %s", want, sent)
		}
	}
}

// The regression: GMP pages by default, and the appliance answered with the
// first 10 of its 23 hosts. A partial page must not pass as the inventory.
func TestAPartialPageIsRefused(t *testing.T) {
	c, _ := clientWith(t, fixture(t, "get_assets_host.xml"))
	_, err := c.HostAssets()
	if err == nil || !strings.Contains(err.Error(), "partial page") {
		t.Fatalf("got %v, want the partial page refused", err)
	}
}

func TestAResponseOverTheLimitIsRefused(t *testing.T) {
	c, _ := clientWith(t, fixture(t, "get_assets_host.xml"))
	c.MaxResponseBytes = 4096
	_, err := c.HostAssets()
	var tooLarge *ResponseTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("got %v, want a ResponseTooLargeError", err)
	}
}

func TestAuthenticateAndVersion(t *testing.T) {
	c, ft := clientWith(t, fixture(t, "authenticate_ok.xml"))
	if err := c.Authenticate("demo", "secret"); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	sent := ft.written.String()
	if !strings.Contains(sent, "<username>demo</username>") {
		t.Errorf("username not sent: %s", sent)
	}

	c2, _ := clientWith(t, fixture(t, "get_version.xml"))
	v, err := c2.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v != "22.8" {
		t.Errorf("version = %q, want 22.8", v)
	}
}

// A refused credential is the operator's problem and has to be distinguishable
// from a protocol fault, or the UI cannot say which.
func TestRefusedCredentialIsAnAuthFailure(t *testing.T) {
	c, _ := clientWith(t,
		`<authenticate_response status="400" status_text="Authentication failed"/>`)

	err := c.Authenticate("demo", "wrong")
	if err == nil {
		t.Fatal("a refused authentication was reported as success")
	}
	if !IsAuthFailure(err) {
		t.Errorf("not recognised as an auth failure: %v", err)
	}

	// While a refusal of something else is not.
	c2, _ := clientWith(t, `<get_assets_response status="400" status_text="Bogus command"/>`)
	_, err = c2.HostAssets()
	if err == nil {
		t.Fatal("a refused get_assets was reported as success")
	}
	if IsAuthFailure(err) {
		t.Errorf("a get_assets refusal was mistaken for an auth failure: %v", err)
	}
}

// A credential containing XML must not become part of the request's structure.
func TestCredentialsAreEscaped(t *testing.T) {
	c, ft := clientWith(t, fixture(t, "authenticate_ok.xml"))
	if err := c.Authenticate("a&b", "p<ss>\"'w&rd"); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	sent := ft.written.String()
	if strings.Contains(sent, "<ss>") {
		t.Errorf("a password containing a tag was sent unescaped: %s", sent)
	}
	if !strings.Contains(sent, "a&amp;b") {
		t.Errorf("the username was not escaped: %s", sent)
	}
}

// Responses carry no length, so reading stops when the document balances —
// not when the first read returns, and not when the connection closes.
func TestReadStopsAtTheEndOfTheDocument(t *testing.T) {
	// One response, delivered a few bytes at a time, followed by nothing.
	body := `<get_version_response status="200" status_text="OK"><version>22.8</version></get_version_response>`
	c := New(&fakeTransport{response: iotest(body, 7)})

	v, err := c.Version()
	if err != nil {
		t.Fatalf("Version across short reads: %v", err)
	}
	if v != "22.8" {
		t.Errorf("version = %q", v)
	}
}

// A connection that drops mid-response is a truncated answer, and saying so is
// the difference between a bug report and a network check.
func TestTruncatedResponseIsReported(t *testing.T) {
	c := New(&fakeTransport{response: strings.NewReader(`<get_version_response status="200"><ver`)})

	_, err := c.Version()
	if err == nil {
		t.Fatal("a truncated response was accepted")
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("error does not say the response was incomplete: %v", err)
	}
}

func TestCloseClosesTheTransport(t *testing.T) {
	ft := &fakeTransport{response: strings.NewReader("")}
	if err := New(ft).Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !ft.closed {
		t.Error("the transport was left open")
	}
}

// Host-key policy: an unexpected key must stop the connection, and the
// fingerprint has to come back so it can be shown and pinned.
func TestHostKeyPolicy(t *testing.T) {
	t.Run("unknown key is refused by default", func(t *testing.T) {
		cb := hostKeyCallback(SSHConfig{})
		err := cb("appliance", nil, testKey(t))
		var unknown *UnknownHostKeyError
		if !errors.As(err, &unknown) {
			t.Fatalf("got %v, want an UnknownHostKeyError", err)
		}
		if !strings.HasPrefix(unknown.Got, "SHA256:") {
			t.Errorf("fingerprint not reported: %q", unknown.Got)
		}
	})

	t.Run("a matching fingerprint is accepted", func(t *testing.T) {
		key := testKey(t)
		cb := hostKeyCallback(SSHConfig{HostKeyFingerprint: fingerprintOf(key)})
		if err := cb("appliance", nil, key); err != nil {
			t.Errorf("a matching key was refused: %v", err)
		}
	})

	t.Run("a different fingerprint is refused", func(t *testing.T) {
		cb := hostKeyCallback(SSHConfig{HostKeyFingerprint: "SHA256:something-else"})
		if err := cb("appliance", nil, testKey(t)); err == nil {
			t.Error("a key that does not match the pinned fingerprint was accepted")
		}
	})

	t.Run("unknown is allowed only when asked for", func(t *testing.T) {
		cb := hostKeyCallback(SSHConfig{AllowUnknownHostKey: true})
		if err := cb("appliance", nil, testKey(t)); err != nil {
			t.Errorf("AllowUnknownHostKey did not allow it: %v", err)
		}
	})
}

// iotest returns a reader that hands out the string in small pieces.
func iotest(s string, chunk int) io.Reader {
	return &chunkedReader{data: s, chunk: chunk}
}

type chunkedReader struct {
	data  string
	chunk int
	pos   int
}

func (c *chunkedReader) Read(p []byte) (int, error) {
	if c.pos >= len(c.data) {
		return 0, io.EOF
	}
	end := c.pos + c.chunk
	if end > len(c.data) {
		end = len(c.data)
	}
	n := copy(p, c.data[c.pos:end])
	c.pos += n
	return n, nil
}

// testKey is a throwaway host key for the policy tests. Generated per run
// rather than checked in: a key in the repository, even a test one, is a key
// somebody will eventually reuse.
func testKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return signer.PublicKey()
}

func fingerprintOf(key ssh.PublicKey) string { return ssh.FingerprintSHA256(key) }
